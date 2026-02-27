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
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

// RoundTableReconciler reconciles a RoundTable object.
type RoundTableReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	nc *nats.Conn
	js nats.JetStreamContext
	mu sync.Mutex
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch
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

	// 4. Cost Budget Check
	phase := r.computePhase(rt, readyCount, total, totalCost)
	rt.Status.Phase = phase

	// 5. Active Missions count
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
func (r *RoundTableReconciler) discoverKnights(ctx context.Context, rt *aiv1alpha1.RoundTable) ([]aiv1alpha1.Knight, error) {
	knightList := &aiv1alpha1.KnightList{}
	listOpts := []client.ListOption{
		client.InNamespace(rt.Namespace),
	}

	if rt.Spec.KnightSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(rt.Spec.KnightSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid knightSelector: %w", err)
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := r.List(ctx, knightList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list knights: %w", err)
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
	if err := r.ensureNATSConnection(rt.Spec.NATS.URL); err != nil {
		return err
	}

	retention := nats.WorkQueuePolicy
	switch rt.Spec.NATS.StreamRetention {
	case "Limits":
		retention = nats.LimitsPolicy
	case "Interest":
		retention = nats.InterestPolicy
	}

	prefix := strings.ReplaceAll(rt.Spec.NATS.SubjectPrefix, "-", "_")

	// Tasks stream
	tasksStreamName := rt.Spec.NATS.TasksStream
	tasksSubject := fmt.Sprintf("%s.tasks.>", rt.Spec.NATS.SubjectPrefix)
	if err := r.ensureStream(tasksStreamName, tasksSubject, retention, prefix); err != nil {
		return fmt.Errorf("tasks stream: %w", err)
	}

	// Results stream
	resultsStreamName := rt.Spec.NATS.ResultsStream
	resultsSubject := fmt.Sprintf("%s.results.>", rt.Spec.NATS.SubjectPrefix)
	if err := r.ensureStream(resultsStreamName, resultsSubject, retention, prefix); err != nil {
		return fmt.Errorf("results stream: %w", err)
	}

	return nil
}

// ensureStream creates a JetStream stream if it doesn't exist.
func (r *RoundTableReconciler) ensureStream(name, subject string, retention nats.RetentionPolicy, _ string) error {
	_, err := r.js.StreamInfo(name)
	if err == nil {
		return nil // stream already exists
	}

	_, err = r.js.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  []string{subject},
		Retention: retention,
		Storage:   nats.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("failed to create stream %s: %w", name, err)
	}

	return nil
}

// ensureNATSConnection connects to NATS if not already connected.
func (r *RoundTableReconciler) ensureNATSConnection(natsURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.nc != nil && r.nc.IsConnected() {
		return nil
	}

	if natsURL == "" {
		natsURL = "nats://nats.database.svc:4222"
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return fmt.Errorf("NATS connect failed: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("JetStream context failed: %w", err)
	}

	r.nc = nc
	r.js = js
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoundTableReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.RoundTable{}).
		Named("roundtable").
		Complete(r)
}

