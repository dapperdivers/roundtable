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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

const (
	missionFinalizer = "ai.roundtable.io/mission-finalizer"
)

// BriefingPayload is the JSON payload published to NATS for mission briefings.
type BriefingPayload struct {
	MissionName string   `json:"missionName"`
	Objective   string   `json:"objective"`
	Briefing    string   `json:"briefing"`
	Knights     []string `json:"knights"`
}

// MissionReconciler reconciles a Mission object.
type MissionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	natsClient natspkg.Client
	mu         sync.Mutex
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *MissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	mission := &aiv1alpha1.Mission{}
	if err := r.Get(ctx, req.NamespacedName, mission); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if mission.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(mission, missionFinalizer) {
			log.Info("Cleaning up mission resources", "mission", mission.Name)
			controllerutil.RemoveFinalizer(mission, missionFinalizer)
			if err := r.Update(ctx, mission); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(mission, missionFinalizer) {
		controllerutil.AddFinalizer(mission, missionFinalizer)
		if err := r.Update(ctx, mission); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Initialize status
	if mission.Status.Phase == "" {
		mission.Status.Phase = aiv1alpha1.MissionPhaseAssembling
		now := metav1.Now()
		mission.Status.StartedAt = &now
		expiresAt := metav1.NewTime(now.Add(time.Duration(mission.Spec.TTL) * time.Second))
		mission.Status.ExpiresAt = &expiresAt
		r.initKnightStatuses(mission)
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Check TTL expiration in any non-terminal phase
	if mission.Status.ExpiresAt != nil && time.Now().After(mission.Status.ExpiresAt.Time) {
		if mission.Status.Phase != aiv1alpha1.MissionPhaseCleaningUp &&
			mission.Status.Phase != aiv1alpha1.MissionPhaseExpired {
			log.Info("Mission TTL expired", "mission", mission.Name)
			mission.Status.Phase = aiv1alpha1.MissionPhaseExpired
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "Mission expired (TTL exceeded)"
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Expired",
				Message:            "Mission TTL expired",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			if err := r.Status().Update(ctx, mission); err != nil {
				return ctrl.Result{}, err
			}
			// Transition to cleanup
			mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
			return ctrl.Result{RequeueAfter: 5 * time.Second}, r.Status().Update(ctx, mission)
		}
	}

	switch mission.Status.Phase {
	case aiv1alpha1.MissionPhaseAssembling:
		return r.reconcileAssembling(ctx, mission)
	case aiv1alpha1.MissionPhaseBriefing:
		return r.reconcileBriefing(ctx, mission)
	case aiv1alpha1.MissionPhaseActive:
		return r.reconcileActive(ctx, mission)
	case aiv1alpha1.MissionPhaseSucceeded, aiv1alpha1.MissionPhaseFailed:
		// Transition to cleanup
		mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.Status().Update(ctx, mission)
	case aiv1alpha1.MissionPhaseCleaningUp:
		return r.reconcileCleaningUp(ctx, mission)
	case aiv1alpha1.MissionPhaseExpired:
		// Already handled above, but if we get here directly just clean up
		mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.Status().Update(ctx, mission)
	}

	return ctrl.Result{}, nil
}

// initKnightStatuses initializes knight status entries.
func (r *MissionReconciler) initKnightStatuses(mission *aiv1alpha1.Mission) {
	mission.Status.KnightStatuses = make([]aiv1alpha1.MissionKnightStatus, len(mission.Spec.Knights))
	for i, mk := range mission.Spec.Knights {
		mission.Status.KnightStatuses[i] = aiv1alpha1.MissionKnightStatus{
			Name:      mk.Name,
			Ephemeral: mk.Ephemeral,
		}
	}
}

// natsPrefix returns the NATS subject prefix for this mission.
func natsPrefix(mission *aiv1alpha1.Mission) string {
	if mission.Spec.NATSPrefix != "" {
		return mission.Spec.NATSPrefix
	}
	return fmt.Sprintf("mission-%s", mission.Name)
}

// reconcileAssembling validates knight references and waits for readiness.
func (r *MissionReconciler) reconcileAssembling(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	allReady := true
	for i, mk := range mission.Spec.Knights {
		if mk.Ephemeral {
			// v1: skip ephemeral knights (not implemented)
			log.Info("Skipping ephemeral knight (v2 feature)", "knight", mk.Name)
			mission.Status.KnightStatuses[i].Ready = false
			allReady = false
			continue
		}

		// Validate knight exists
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      mk.Name,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			log.Error(err, "Knight not found", "knight", mk.Name)
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "KnightsReady",
				Status:             metav1.ConditionFalse,
				Reason:             "KnightNotFound",
				Message:            fmt.Sprintf("Knight %q not found: %v", mk.Name, err),
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			_ = r.Status().Update(ctx, mission)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Check knight readiness
		if knight.Status.Phase == aiv1alpha1.KnightPhaseReady && knight.Status.Ready {
			mission.Status.KnightStatuses[i].Ready = true
		} else {
			mission.Status.KnightStatuses[i].Ready = false
			allReady = false
			log.Info("Knight not ready yet", "knight", mk.Name, "phase", knight.Status.Phase)
		}
	}

	if allReady && len(mission.Spec.Knights) > 0 {
		// Check we don't have only ephemeral knights (which we skip in v1)
		hasNonEphemeral := false
		for _, mk := range mission.Spec.Knights {
			if !mk.Ephemeral {
				hasNonEphemeral = true
				break
			}
		}
		if !hasNonEphemeral {
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "KnightsReady",
				Status:             metav1.ConditionFalse,
				Reason:             "NoValidKnights",
				Message:            "All knights are ephemeral (not supported in v1)",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "No valid knights available"
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}

		log.Info("All knights assembled, transitioning to Briefing", "mission", mission.Name)
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "KnightsReady",
			Status:             metav1.ConditionTrue,
			Reason:             "AllKnightsReady",
			Message:            "All referenced knights are ready",
			ObservedGeneration: mission.Generation,
		})
		mission.Status.Phase = aiv1alpha1.MissionPhaseBriefing
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
	}

	mission.Status.ObservedGeneration = mission.Generation
	_ = r.Status().Update(ctx, mission)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileBriefing publishes the mission briefing to NATS and transitions to Active.
func (r *MissionReconciler) reconcileBriefing(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Publish briefing to NATS
	if mission.Spec.Briefing != "" {
		if err := r.publishBriefing(ctx, mission); err != nil {
			log.Error(err, "Failed to publish briefing, will retry")
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "BriefingPublished",
				Status:             metav1.ConditionFalse,
				Reason:             "PublishFailed",
				Message:            fmt.Sprintf("Failed to publish briefing: %v", err),
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			_ = r.Status().Update(ctx, mission)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		log.Info("Briefing published", "mission", mission.Name)
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "BriefingPublished",
			Status:             metav1.ConditionTrue,
			Reason:             "Published",
			Message:            "Mission briefing published to all knights",
			ObservedGeneration: mission.Generation,
		})
	} else {
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "BriefingPublished",
			Status:             metav1.ConditionTrue,
			Reason:             "NoBriefing",
			Message:            "No briefing text configured",
			ObservedGeneration: mission.Generation,
		})
	}

	mission.Status.Phase = aiv1alpha1.MissionPhaseActive
	mission.Status.ObservedGeneration = mission.Generation
	return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
}

// reconcileActive monitors chain execution, timeout, and knight status.
func (r *MissionReconciler) reconcileActive(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check timeout
	if mission.Status.StartedAt != nil {
		elapsed := time.Since(mission.Status.StartedAt.Time)
		if elapsed > time.Duration(mission.Spec.Timeout)*time.Second {
			log.Info("Mission timed out", "mission", mission.Name, "elapsed", elapsed)
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = fmt.Sprintf("Mission timed out after %ds", mission.Spec.Timeout)
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Timeout",
				Message:            fmt.Sprintf("Mission timed out after %ds", mission.Spec.Timeout),
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}
	}

	// Check cost budget
	if mission.Spec.CostBudgetUSD != "" && mission.Spec.CostBudgetUSD != "0" {
		totalCost, err := r.aggregateMissionCost(ctx, mission)
		if err != nil {
			log.Error(err, "Failed to aggregate mission cost")
		} else {
			mission.Status.TotalCost = fmt.Sprintf("%.4f", totalCost)
			
			// Parse budget
			var budget float64
			if _, err := fmt.Sscanf(mission.Spec.CostBudgetUSD, "%f", &budget); err == nil {
				if totalCost > budget {
					log.Info("Mission cost budget exceeded", "totalCost", totalCost, "budget", budget)
					mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
					now := metav1.Now()
					mission.Status.CompletedAt = &now
					mission.Status.Result = fmt.Sprintf("Cost budget exceeded: $%.2f > $%.2f", totalCost, budget)
					meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
						Type:               "Complete",
						Status:             metav1.ConditionTrue,
						Reason:             "OverBudget",
						Message:            fmt.Sprintf("Cost $%.2f exceeded budget $%.2f", totalCost, budget),
						ObservedGeneration: mission.Generation,
					})
					mission.Status.ObservedGeneration = mission.Generation
					return ctrl.Result{}, r.Status().Update(ctx, mission)
				}
			}
		}
	}

	// Create Chain CRs for any referenced chains that don't exist yet
	if len(mission.Spec.Chains) > 0 {
		allChainsComplete, anyChainFailed, err := r.reconcileMissionChains(ctx, mission)
		if err != nil {
			log.Error(err, "Failed to reconcile mission chains")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		if anyChainFailed {
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "One or more mission chains failed"
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "ChainFailed",
				Message:            "One or more mission chains failed",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}

		if allChainsComplete {
			mission.Status.Phase = aiv1alpha1.MissionPhaseSucceeded
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "All mission chains completed successfully"
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Succeeded",
				Message:            "All mission chains completed successfully",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}
	} else {
		// No chains — mission succeeds immediately (it was just a briefing mission)
		mission.Status.Phase = aiv1alpha1.MissionPhaseSucceeded
		now := metav1.Now()
		mission.Status.CompletedAt = &now
		mission.Status.Result = "Mission completed (briefing-only)"
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "Complete",
			Status:             metav1.ConditionTrue,
			Reason:             "Succeeded",
			Message:            "Briefing-only mission completed",
			ObservedGeneration: mission.Generation,
		})
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Update knight statuses
	r.updateKnightStatuses(ctx, mission)
	mission.Status.ObservedGeneration = mission.Generation
	_ = r.Status().Update(ctx, mission)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileMissionChains creates and monitors Chain CRs for the mission.
// Returns (allComplete, anyFailed, error).
func (r *MissionReconciler) reconcileMissionChains(ctx context.Context, mission *aiv1alpha1.Mission) (bool, bool, error) {
	log := logf.FromContext(ctx)

	// Determine which phase chains to run based on mission state
	// Setup chains run first, then Active, Teardown runs during cleanup
	activePhases := []string{"Setup", "Active"}

	allComplete := true
	anyFailed := false

	for _, chainRef := range mission.Spec.Chains {
		// Only process chains for current active phases
		phaseMatch := false
		for _, p := range activePhases {
			if chainRef.Phase == p || (chainRef.Phase == "" && p == "Active") { // default is Active
				phaseMatch = true
				break
			}
		}
		if !phaseMatch {
			continue
		}

		// Create mission-scoped chain copy if it doesn't exist
		if err := r.ensureMissionChain(ctx, mission, chainRef); err != nil {
			log.Error(err, "Failed to create mission chain", "chain", chainRef.Name)
			anyFailed = true
			continue
		}

		// Monitor the mission-scoped chain copy
		missionChainName := fmt.Sprintf("mission-%s-%s", mission.Name, chainRef.Name)
		chain := &aiv1alpha1.Chain{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      missionChainName,
			Namespace: mission.Namespace,
		}, chain)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				log.Info("Mission chain not yet created", "chain", missionChainName)
				allComplete = false
				continue
			}
			return false, false, err
		}

		// Update mission.status.chainStatuses
		r.updateChainStatus(mission, chainRef.Name, missionChainName, chain.Status.Phase)

		// Check chain status
		switch chain.Status.Phase {
		case aiv1alpha1.ChainPhaseSucceeded:
			// OK
		case aiv1alpha1.ChainPhaseFailed:
			anyFailed = true
		default:
			allComplete = false
		}
	}

	return allComplete, anyFailed, nil
}

// reconcileCleaningUp handles resource cleanup and optional self-deletion.
func (r *MissionReconciler) reconcileCleaningUp(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Run teardown chains if any
	for _, chainRef := range mission.Spec.Chains {
		if chainRef.Phase != "Teardown" {
			continue
		}
		chain := &aiv1alpha1.Chain{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      chainRef.Name,
			Namespace: mission.Namespace,
		}, chain); err != nil {
			log.Info("Teardown chain not found, skipping", "chain", chainRef.Name)
			continue
		}
		// If teardown chain hasn't run yet, trigger it
		if chain.Status.Phase == aiv1alpha1.ChainPhaseIdle {
			now := metav1.Now()
			chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
			chain.Status.StartedAt = &now
			if err := r.Status().Update(ctx, chain); err != nil {
				log.Error(err, "Failed to trigger teardown chain", "chain", chainRef.Name)
			}
			// Requeue to wait for teardown
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		// If teardown chain is still running, wait
		if chain.Status.Phase == aiv1alpha1.ChainPhaseRunning {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Self-delete if cleanupPolicy=Delete and TTL expired
	if mission.Spec.CleanupPolicy == "Delete" &&
		mission.Status.ExpiresAt != nil &&
		time.Now().After(mission.Status.ExpiresAt.Time) {
		log.Info("Deleting expired mission", "mission", mission.Name)
		if err := r.Delete(ctx, mission); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Mark cleanup as done
	meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
		Type:               "CleanupComplete",
		Status:             metav1.ConditionTrue,
		Reason:             "CleanedUp",
		Message:            "Mission cleanup completed",
		ObservedGeneration: mission.Generation,
	})
	mission.Status.ObservedGeneration = mission.Generation
	_ = r.Status().Update(ctx, mission)

	// If cleanupPolicy=Delete but TTL hasn't expired yet, requeue
	if mission.Spec.CleanupPolicy == "Delete" && mission.Status.ExpiresAt != nil {
		remaining := time.Until(mission.Status.ExpiresAt.Time)
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	return ctrl.Result{}, nil
}

// updateKnightStatuses refreshes knight readiness from current Knight CRs.
func (r *MissionReconciler) updateKnightStatuses(ctx context.Context, mission *aiv1alpha1.Mission) {
	for i, mk := range mission.Spec.Knights {
		if mk.Ephemeral {
			continue
		}
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      mk.Name,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			mission.Status.KnightStatuses[i].Ready = false
			continue
		}
		mission.Status.KnightStatuses[i].Ready = knight.Status.Ready
	}
}

// ensureNATS connects to NATS if not already connected.
func (r *MissionReconciler) ensureNATS(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.natsClient != nil && r.natsClient.IsConnected() {
		return nil
	}

	log := logf.FromContext(ctx)
	config := natspkg.DefaultConfig()
	r.natsClient = natspkg.NewClient(config, log)

	return r.natsClient.Connect()
}

// publishBriefing publishes the mission briefing to NATS.
func (r *MissionReconciler) publishBriefing(ctx context.Context, mission *aiv1alpha1.Mission) error {
	if err := r.ensureNATS(ctx); err != nil {
		return err
	}

	knightNames := make([]string, 0, len(mission.Spec.Knights))
	for _, mk := range mission.Spec.Knights {
		knightNames = append(knightNames, mk.Name)
	}

	payload := BriefingPayload{
		MissionName: mission.Name,
		Objective:   mission.Spec.Objective,
		Briefing:    mission.Spec.Briefing,
		Knights:     knightNames,
	}

	// Publish to mission briefing subject
	prefix := natsPrefix(mission)
	subject := fmt.Sprintf("%s.briefing", prefix)
	if err := r.natsClient.PublishJSON(subject, payload); err != nil {
		return err
	}

	// Also publish briefing as a task to each knight's normal task subject
	for _, mk := range mission.Spec.Knights {
		if mk.Ephemeral {
			continue
		}
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      mk.Name,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			continue
		}

		taskPayload := natspkg.TaskPayload{
			TaskID:    fmt.Sprintf("mission-%s-briefing-%d", mission.Name, time.Now().UnixMilli()),
			ChainName: fmt.Sprintf("mission-%s", mission.Name),
			StepName:  "briefing",
			Task:      fmt.Sprintf("[Mission: %s]\nObjective: %s\n\n%s", mission.Name, mission.Spec.Objective, mission.Spec.Briefing),
		}

		taskSubject := natspkg.TaskSubject("fleet-a", knight.Spec.Domain, mk.Name)
		if err := r.natsClient.PublishJSON(taskSubject, taskPayload); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to publish briefing to knight", "knight", mk.Name)
		}
	}

	return nil
}

// aggregateMissionCost calculates the total cost across all mission knights.
func (r *MissionReconciler) aggregateMissionCost(ctx context.Context, mission *aiv1alpha1.Mission) (float64, error) {
	var totalCost float64

	for _, mk := range mission.Spec.Knights {
		knightName := mk.Name
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      knightName,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue // Knight not found, skip
			}
			return 0, err
		}

		// Parse knight's total cost
		if knight.Status.TotalCost != "" {
			var cost float64
			if _, err := fmt.Sscanf(knight.Status.TotalCost, "%f", &cost); err == nil {
				totalCost += cost
			}
		}
	}

	return totalCost, nil
}

// ensureMissionChain creates a mission-scoped chain copy if it doesn't already exist.
func (r *MissionReconciler) ensureMissionChain(ctx context.Context, mission *aiv1alpha1.Mission, chainRef aiv1alpha1.MissionChainRef) error {
	log := logf.FromContext(ctx)

	// Fetch the source chain template
	sourceChain := &aiv1alpha1.Chain{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      chainRef.Name,
		Namespace: mission.Namespace,
	}, sourceChain); err != nil {
		return fmt.Errorf("source chain %q not found: %w", chainRef.Name, err)
	}

	// Build the mission-scoped chain name
	missionChainName := fmt.Sprintf("mission-%s-%s", mission.Name, chainRef.Name)

	// Check if it already exists
	existingChain := &aiv1alpha1.Chain{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      missionChainName,
		Namespace: mission.Namespace,
	}, existingChain)
	if err == nil {
		// Chain already exists
		return nil
	}
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// Get RoundTable reference for this mission
	rtRef := mission.Spec.RoundTableRef
	if rtRef == "" {
		rtRef = "default" // fallback to default if not specified
	}

	// Create the mission-scoped chain
	missionChain := &aiv1alpha1.Chain{
		ObjectMeta: metav1.ObjectMeta{
			Name:      missionChainName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				"ai.roundtable.io/mission":     mission.Name,
				"ai.roundtable.io/chain-phase": chainRef.Phase,
			},
		},
		Spec: aiv1alpha1.ChainSpec{
			Description:   fmt.Sprintf("Mission %s: %s", mission.Name, sourceChain.Spec.Description),
			Steps:         sourceChain.Spec.Steps,
			Timeout:       sourceChain.Spec.Timeout,
			RoundTableRef: rtRef,
			OutputKnight:  sourceChain.Spec.OutputKnight,
			RetryPolicy:   sourceChain.Spec.RetryPolicy,
		},
	}

	// Override input if specified
	if chainRef.InputOverride != "" {
		missionChain.Spec.Input = chainRef.InputOverride
	} else {
		missionChain.Spec.Input = sourceChain.Spec.Input
	}

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(mission, missionChain, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create the chain CR
	if err := r.Create(ctx, missionChain); err != nil {
		return fmt.Errorf("failed to create mission chain: %w", err)
	}

	log.Info("Created mission-scoped chain", "chain", missionChainName, "sourceChain", chainRef.Name)
	return nil
}

// updateChainStatus updates the mission's chainStatuses array with the latest chain status.
func (r *MissionReconciler) updateChainStatus(mission *aiv1alpha1.Mission, chainRefName, chainCRName string, phase aiv1alpha1.ChainPhase) {
	// Find existing status entry
	for i := range mission.Status.ChainStatuses {
		if mission.Status.ChainStatuses[i].Name == chainRefName {
			mission.Status.ChainStatuses[i].ChainCRName = chainCRName
			mission.Status.ChainStatuses[i].Phase = phase
			return
		}
	}

	// Add new status entry
	mission.Status.ChainStatuses = append(mission.Status.ChainStatuses, aiv1alpha1.MissionChainStatus{
		Name:        chainRefName,
		ChainCRName: chainCRName,
		Phase:       phase,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *MissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Mission{}).
		Owns(&aiv1alpha1.Chain{}).
		Named("mission").
		Complete(r)
}
