/*
Copyright 2026 dapperdivers.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

// RoundTableReconciler reconciles a RoundTable object.
type RoundTableReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	NATS *natspkg.Provider
}

// natsClient returns the shared NATS client, or an error if the provider is not configured.
func (r *RoundTableReconciler) natsClient() (natspkg.Client, error) {
	if r.NATS == nil {
		return nil, fmt.Errorf("NATS provider not configured")
	}
	return r.NATS.Client()
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions,verbs=get;list;watch

func (r *RoundTableReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	rt := &aiv1alpha1.RoundTable{}
	if err := r.Get(ctx, req.NamespacedName, rt); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle suspended state
	if rt.Spec.Suspended {
		rt.Status.Phase = aiv1alpha1.RoundTablePhaseSuspended
		meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "Suspended",
			Message:            "RoundTable is suspended",
			ObservedGeneration: rt.Generation,
		})
		rt.Status.ObservedGeneration = rt.Generation
		if err := r.Status().Update(ctx, rt); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	// 1. Knight Discovery
	knights, err := r.discoverKnights(ctx, rt)
	if err != nil {
		log.Error(err, "Failed to discover knights")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// 2. Health Aggregation
	var readyCount int32
	knightSummaries := make([]aiv1alpha1.RoundTableKnightSummary, 0, len(knights))
	var totalTasksCompleted int64
	var totalCost float64

	for _, k := range knights {
		summary := aiv1alpha1.RoundTableKnightSummary{
			Name:  k.Name,
			Phase: k.Status.Phase,
			Ready: k.Status.Ready,
		}
		knightSummaries = append(knightSummaries, summary)
		if k.Status.Ready {
			readyCount++
		}
		totalTasksCompleted += k.Status.TasksCompleted
		if k.Status.TotalCost != "" {
			if cost, err := strconv.ParseFloat(k.Status.TotalCost, 64); err == nil {
				totalCost += cost
			}
		}
	}

	total := int32(len(knights))
	rt.Status.KnightsTotal = total
	rt.Status.KnightsReady = readyCount
	rt.Status.Knights = knightSummaries
	rt.Status.TotalTasksCompleted = totalTasksCompleted
	rt.Status.TotalCost = fmt.Sprintf("%.4f", totalCost)

	// 3. NATS Stream Management
	if rt.Spec.NATS.CreateStreams {
		if err := r.ensureStreams(ctx, rt); err != nil {
			log.Error(err, "Failed to ensure NATS streams")
			meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
				Type:               "NATSReady",
				Status:             metav1.ConditionFalse,
				Reason:             "StreamError",
				Message:            err.Error(),
				ObservedGeneration: rt.Generation,
			})
		} else {
			meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
				Type:               "NATSReady",
				Status:             metav1.ConditionTrue,
				Reason:             "StreamsReady",
				Message:            "JetStream streams are configured",
				ObservedGeneration: rt.Generation,
			})
		}
	}

	// 4. Warm Pool Reconciliation
	if rt.Spec.WarmPool != nil && rt.Spec.WarmPool.Size > 0 {
		if err := r.reconcileWarmPool(ctx, rt); err != nil {
			log.Error(err, "Failed to reconcile warm pool")
		}
	}

	// 5. Cost Budget Check
	phase := r.computePhase(rt, readyCount, total, totalCost)
	rt.Status.Phase = phase

	// 6. Active Missions count
	activeMissions, err := r.countActiveMissions(ctx, rt)
	if err != nil {
		log.Error(err, "Failed to count active missions")
	}
	rt.Status.ActiveMissions = activeMissions

	// Set availability condition
	switch phase {
	case aiv1alpha1.RoundTablePhaseReady:
		meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionTrue,
			Reason:             "AllKnightsReady",
			Message:            fmt.Sprintf("All %d knights are ready", total),
			ObservedGeneration: rt.Generation,
		})
	case aiv1alpha1.RoundTablePhaseDegraded:
		meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "KnightsDegraded",
			Message:            fmt.Sprintf("%d/%d knights ready", readyCount, total),
			ObservedGeneration: rt.Generation,
		})
	case aiv1alpha1.RoundTablePhaseOverBudget:
		meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "OverBudget",
			Message:            fmt.Sprintf("Cost %.4f exceeds budget %s", totalCost, rt.Spec.Policies.CostBudgetUSD),
			ObservedGeneration: rt.Generation,
		})
	default:
		meta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "Provisioning",
			Message:            "RoundTable is provisioning",
			ObservedGeneration: rt.Generation,
		})
	}

	rt.Status.ObservedGeneration = rt.Generation
	if err := r.Status().Update(ctx, rt); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// discoverKnights lists Knight CRs matching the RoundTable's knightSelector.
// For ephemeral RoundTables, it returns only knights with the matching round-table label.
// For non-ephemeral RoundTables, it excludes all ephemeral knights.
func (r *RoundTableReconciler) discoverKnights(ctx context.Context, rt *aiv1alpha1.RoundTable) ([]aiv1alpha1.Knight, error) {
	knightList := &aiv1alpha1.KnightList{}
	listOpts := []client.ListOption{
		client.InNamespace(rt.Namespace),
	}

	if rt.Spec.Ephemeral {
		// Ephemeral RoundTable: only manage knights that belong to this specific table
		listOpts = append(listOpts, client.MatchingLabels{
			aiv1alpha1.LabelRoundTable: rt.Name,
		})
	} else {
		// Non-ephemeral RoundTable: manage all non-ephemeral knights
		// Apply knight selector if specified
		if rt.Spec.KnightSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(rt.Spec.KnightSelector)
			if err != nil {
				return nil, fmt.Errorf("invalid knightSelector: %w", err)
			}
			listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
		}
	}

	if err := r.List(ctx, knightList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list knights: %w", err)
	}

	// For non-ephemeral RoundTables, filter out any ephemeral knights
	// (in case knightSelector didn't exclude them)
	if !rt.Spec.Ephemeral {
		filtered := make([]aiv1alpha1.Knight, 0, len(knightList.Items))
		for _, knight := range knightList.Items {
			if knight.Labels[aiv1alpha1.LabelEphemeral] != "true" {
				filtered = append(filtered, knight)
			}
		}
		return filtered, nil
	}

	return knightList.Items, nil
}

// computePhase determines the RoundTable phase based on knight health and cost.
func (r *RoundTableReconciler) computePhase(rt *aiv1alpha1.RoundTable, readyCount, total int32, totalCost float64) aiv1alpha1.RoundTablePhase {
	// Check cost budget
	if rt.Spec.Policies != nil && rt.Spec.Policies.CostBudgetUSD != "" && rt.Spec.Policies.CostBudgetUSD != "0" {
		budget, err := strconv.ParseFloat(rt.Spec.Policies.CostBudgetUSD, 64)
		if err == nil && totalCost > budget {
			return aiv1alpha1.RoundTablePhaseOverBudget
		}
	}

	if total == 0 {
		return aiv1alpha1.RoundTablePhaseProvisioning
	}

	if readyCount == total {
		return aiv1alpha1.RoundTablePhaseReady
	}

	return aiv1alpha1.RoundTablePhaseDegraded
}

// countActiveMissions counts missions referencing this RoundTable that are in active phases.
func (r *RoundTableReconciler) countActiveMissions(ctx context.Context, rt *aiv1alpha1.RoundTable) (int32, error) {
	missionList := &aiv1alpha1.MissionList{}
	if err := r.List(ctx, missionList, client.InNamespace(rt.Namespace)); err != nil {
		return 0, err
	}

	var count int32
	for _, m := range missionList.Items {
		if m.Spec.RoundTableRef == rt.Name {
			switch m.Status.Phase {
			case aiv1alpha1.MissionPhaseAssembling, aiv1alpha1.MissionPhaseBriefing, aiv1alpha1.MissionPhaseActive:
				count++
			}
		}
	}
	return count, nil
}

// ensureStreams creates or verifies JetStream streams for this RoundTable.
func (r *RoundTableReconciler) ensureStreams(ctx context.Context, rt *aiv1alpha1.RoundTable) error {
	// Get shared NATS client
	client, err := r.natsClient()
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	// Map retention policy string to enum
	retention := natspkg.RetentionWorkQueue
	switch rt.Spec.NATS.StreamRetention {
	case "Limits":
		retention = natspkg.RetentionLimits
	case "Interest":
		retention = natspkg.RetentionInterest
	}

	// Tasks stream
	tasksSubject := natspkg.StreamSubject(rt.Spec.NATS.SubjectPrefix, "tasks")
	tasksStreamConfig := natspkg.StreamConfig{
		Name:      rt.Spec.NATS.TasksStream,
		Subjects:  []string{tasksSubject},
		Retention: retention,
		Storage:   natspkg.StorageFile,
	}
	if err := client.CreateStream(tasksStreamConfig); err != nil {
		return fmt.Errorf("tasks stream: %w", err)
	}

	// Results stream
	resultsSubject := natspkg.StreamSubject(rt.Spec.NATS.SubjectPrefix, "results")
	resultsStreamConfig := natspkg.StreamConfig{
		Name:      rt.Spec.NATS.ResultsStream,
		Subjects:  []string{resultsSubject},
		Retention: retention,
		Storage:   natspkg.StorageFile,
	}
	if err := client.CreateStream(resultsStreamConfig); err != nil {
		return fmt.Errorf("results stream: %w", err)
	}

	return nil
}

// reconcileWarmPool ensures the warm pool has the desired number of pre-warmed knights.
// It creates new warm knights when the pool is below capacity and recycles idle ones.
func (r *RoundTableReconciler) reconcileWarmPool(ctx context.Context, rt *aiv1alpha1.RoundTable) error {
	log := logf.FromContext(ctx)
	wp := rt.Spec.WarmPool

	// List all unclaimed warm pool knights for this RoundTable
	unclaimedKnights, err := r.listWarmPoolKnights(ctx, rt, false)
	if err != nil {
		return fmt.Errorf("failed to list warm pool knights: %w", err)
	}

	// Count available (Ready) and provisioning (non-Ready)
	var available, provisioning int32
	for _, k := range unclaimedKnights {
		if k.Status.Phase == aiv1alpha1.KnightPhaseReady && k.Status.Ready {
			available++
		} else {
			provisioning++
		}
	}

	// Idle recycling: delete warm knights that have been idle too long
	if wp.MaxIdleTime != "" {
		maxIdle, err := time.ParseDuration(wp.MaxIdleTime)
		if err == nil {
			for i := range unclaimedKnights {
				k := &unclaimedKnights[i]
				createdAtStr, ok := k.Annotations[aiv1alpha1.AnnotationWarmPoolCreatedAt]
				if !ok {
					continue
				}
				createdAt, err := time.Parse(time.RFC3339, createdAtStr)
				if err != nil {
					continue
				}
				if time.Since(createdAt) > maxIdle {
					log.Info("Recycling idle warm pool knight", "knight", k.Name,
						"age", time.Since(createdAt).String())
					if err := r.Delete(ctx, k); err != nil {
						log.Error(err, "Failed to delete idle warm knight", "knight", k.Name)
					} else {
						// Adjust counts — the deleted knight is no longer available
						if k.Status.Phase == aiv1alpha1.KnightPhaseReady && k.Status.Ready {
							available--
						} else {
							provisioning--
						}
					}
				}
			}
		}
	}

	// Create new warm knights if pool is under capacity
	deficit := wp.Size - (available + provisioning)
	for i := int32(0); i < deficit; i++ {
		if err := r.createWarmKnight(ctx, rt); err != nil {
			log.Error(err, "Failed to create warm pool knight")
			return err
		}
		provisioning++
	}

	// Count claimed knights for status
	claimedKnights, err := r.listWarmPoolKnights(ctx, rt, true)
	if err != nil {
		log.Error(err, "Failed to count claimed warm pool knights")
	}

	// Update warm pool status
	rt.Status.WarmPool = &aiv1alpha1.WarmPoolStatus{
		Available:    available,
		Provisioning: provisioning,
		Claimed:      int32(len(claimedKnights)),
	}

	log.Info("Warm pool reconciled",
		"desired", wp.Size,
		"available", available,
		"provisioning", provisioning,
		"claimed", len(claimedKnights))

	return nil
}

// listWarmPoolKnights lists warm pool knights for this RoundTable.
// If claimed is true, returns claimed knights; if false, returns unclaimed.
func (r *RoundTableReconciler) listWarmPoolKnights(ctx context.Context, rt *aiv1alpha1.RoundTable, claimed bool) ([]aiv1alpha1.Knight, error) {
	knightList := &aiv1alpha1.KnightList{}
	claimedVal := "false"
	if claimed {
		claimedVal = "true"
	}
	if err := r.List(ctx, knightList,
		client.InNamespace(rt.Namespace),
		client.MatchingLabels{
			aiv1alpha1.LabelWarmPool:        "true",
			aiv1alpha1.LabelWarmPoolClaimed:  claimedVal,
			aiv1alpha1.LabelRoundTable:       rt.Name,
		},
	); err != nil {
		return nil, err
	}
	return knightList.Items, nil
}

// createWarmKnight creates a single warm pool Knight from the RoundTable's warm pool template.
func (r *RoundTableReconciler) createWarmKnight(ctx context.Context, rt *aiv1alpha1.RoundTable) error {
	log := logf.FromContext(ctx)
	wp := rt.Spec.WarmPool

	// Generate random suffix
	randBytes := make([]byte, 3)
	if _, err := rand.Read(randBytes); err != nil {
		return fmt.Errorf("failed to generate random suffix: %w", err)
	}
	name := fmt.Sprintf("%s-warm-%s", rt.Name, hex.EncodeToString(randBytes))

	// Build the knight spec from the template
	spec := wp.Template.DeepCopy()

	// Set runtime from warm pool config
	if wp.Runtime != "" {
		spec.Runtime = wp.Runtime
	}

	// Set domain to warm-pool if not specified in template
	if spec.Domain == "" {
		spec.Domain = "warm-pool"
	}

	// Ensure NATS config uses the RoundTable's infrastructure
	if spec.NATS.URL == "" {
		spec.NATS.URL = rt.Spec.NATS.URL
	}
	if spec.NATS.Stream == "" {
		spec.NATS.Stream = rt.Spec.NATS.TasksStream
	}
	if spec.NATS.ResultsStream == "" {
		spec.NATS.ResultsStream = rt.Spec.NATS.ResultsStream
	}
	// Give warm pool knights a generic subject — missions will patch this on claim
	if len(spec.NATS.Subjects) == 0 {
		spec.NATS.Subjects = []string{fmt.Sprintf("%s.tasks.warm-pool.>", rt.Spec.NATS.SubjectPrefix)}
	}

	// Ensure not suspended
	spec.Suspended = false

	knight := &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: rt.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelWarmPool:       "true",
				aiv1alpha1.LabelWarmPoolClaimed: "false",
				aiv1alpha1.LabelRoundTable:      rt.Name,
			},
			Annotations: map[string]string{
				aiv1alpha1.AnnotationWarmPoolCreatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
		Spec: *spec,
	}

	// Set owner reference so warm knights get GC'd with the RoundTable
	if err := controllerutil.SetControllerReference(rt, knight, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	log.Info("Creating warm pool knight", "knight", name, "roundTable", rt.Name)
	return r.Create(ctx, knight)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoundTableReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.RoundTable{}).
		Named("roundtable").
		Complete(r)
}
