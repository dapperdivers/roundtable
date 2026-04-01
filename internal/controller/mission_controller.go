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
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/mission"
	"github.com/dapperdivers/roundtable/internal/status"
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
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	NATS      *natspkg.Provider
	Planner   *mission.Planner
	Assembler *mission.KnightAssembler
	mu        sync.Mutex
}

// natsClient returns the shared NATS client, or an error if the provider is not configured.
func (r *MissionReconciler) natsClient() (natspkg.Client, error) {
	if r.NATS == nil {
		return nil, fmt.Errorf("NATS provider not configured")
	}
	return r.NATS.Client()
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

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
		now := metav1.Now()
		mission.Status.StartedAt = &now
		expiresAt := metav1.NewTime(now.Add(time.Duration(mission.Spec.TTL) * time.Second))
		mission.Status.ExpiresAt = &expiresAt
		r.initKnightStatuses(mission)
		err := status.ForMission(mission).
			Phase(aiv1alpha1.MissionPhasePending).
			Apply(ctx, r.Client)
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		r.Recorder.Eventf(mission, corev1.EventTypeNormal, "PhaseTransition", "Mission transitioned to %s", aiv1alpha1.MissionPhasePending)
		return ctrl.Result{}, err
	}

	// Check TTL expiration in any non-terminal phase
	if mission.Status.ExpiresAt != nil && time.Now().After(mission.Status.ExpiresAt.Time) {
		if mission.Status.Phase != aiv1alpha1.MissionPhaseCleaningUp &&
			mission.Status.Phase != aiv1alpha1.MissionPhaseExpired {
			log.Info("Mission TTL expired", "mission", mission.Name)
			// Go straight to CleaningUp in a single status update to avoid
			// double-update conflicts (the old code set Expired then immediately
			// overwrote to CleaningUp — the second update stomped the first).
			err := status.ForMission(mission).
				Complete("Mission expired (TTL exceeded)", aiv1alpha1.MissionPhaseCleaningUp).
				Condition(aiv1alpha1.ConditionMissionComplete, aiv1alpha1.ReasonMissionExpired, "Mission TTL expired", metav1.ConditionTrue).
				Apply(ctx, r.Client)
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			r.Recorder.Event(mission, corev1.EventTypeWarning, "Timeout", "Mission exceeded TTL")
			r.Recorder.Eventf(mission, corev1.EventTypeNormal, "PhaseTransition", "Mission transitioned to %s", aiv1alpha1.MissionPhaseCleaningUp)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
	}

	switch mission.Status.Phase {
	case aiv1alpha1.MissionPhasePending:
		return r.reconcilePending(ctx, mission)
	case aiv1alpha1.MissionPhaseProvisioning:
		return r.reconcileProvisioning(ctx, mission)
	case aiv1alpha1.MissionPhasePlanning:
		return r.Planner.ReconcilePlanning(ctx, mission)
	case aiv1alpha1.MissionPhaseAssembling:
		return r.reconcileAssembling(ctx, mission)
	case aiv1alpha1.MissionPhaseBriefing:
		return r.reconcileBriefing(ctx, mission)
	case aiv1alpha1.MissionPhaseActive:
		return r.reconcileActive(ctx, mission)
	case aiv1alpha1.MissionPhaseSucceeded, aiv1alpha1.MissionPhaseFailed:
		// Only transition to cleanup if not already cleaned up (prevents infinite loop)
		if meta.IsStatusConditionTrue(mission.Status.Conditions, aiv1alpha1.ConditionCleanupComplete) {
			return ctrl.Result{}, nil
		}
		mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
		mission.Status.ObservedGeneration = mission.Generation
		err := r.Status().Update(ctx, mission)
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	case aiv1alpha1.MissionPhaseCleaningUp:
		return r.reconcileCleaningUp(ctx, mission)
	case aiv1alpha1.MissionPhaseExpired:
		// Already handled above, but if we get here directly just clean up
		mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
		err := r.Status().Update(ctx, mission)
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
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

// reconcilePending validates the mission spec before provisioning.
func (r *MissionReconciler) reconcilePending(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Validate knight templates have unique names
	templateNames := make(map[string]bool)
	for _, template := range mission.Spec.KnightTemplates {
		if templateNames[template.Name] {
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			mission.Status.Result = fmt.Sprintf("Duplicate knight template name: %s", template.Name)
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}
		templateNames[template.Name] = true
	}

	// Validate knights have unique names and valid template refs
	knightNames := make(map[string]bool)
	for _, knight := range mission.Spec.Knights {
		if knightNames[knight.Name] {
			return ctrl.Result{}, status.ForMission(mission).
				Failed(fmt.Sprintf("Duplicate knight name: %s", knight.Name)).
				Apply(ctx, r.Client)
		}
		knightNames[knight.Name] = true

		// If using templateRef, validate it exists in mission-level templates.
		// Note: RoundTable-level templates are validated later during assembling
		// (they require a Get call we defer to avoid premature fetches).
		if knight.TemplateRef != "" && !templateNames[knight.TemplateRef] && mission.Spec.RoundTableRef == "" {
			return ctrl.Result{}, status.ForMission(mission).
				Failed(fmt.Sprintf("Knight %s references unknown template: %s", knight.Name, knight.TemplateRef)).
				Apply(ctx, r.Client)
		}

		// Validate ephemeral knights have spec OR templateRef (not both, not neither)
		if knight.Ephemeral {
			hasSpec := knight.EphemeralSpec != nil
			hasTemplate := knight.TemplateRef != ""
			if hasSpec == hasTemplate { // XOR check
				return ctrl.Result{}, status.ForMission(mission).
					Failed(fmt.Sprintf("Ephemeral knight %s must have exactly one of ephemeralSpec or templateRef", knight.Name)).
					Apply(ctx, r.Client)
			}
		}
	}

	// Validate referenced chains exist
	for _, chainRef := range mission.Spec.Chains {
		chain := &aiv1alpha1.Chain{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      chainRef.Name,
			Namespace: mission.Namespace,
		}, chain); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return ctrl.Result{}, status.ForMission(mission).
					Failed(fmt.Sprintf("Referenced chain not found: %s", chainRef.Name)).
					Apply(ctx, r.Client)
			}
			return ctrl.Result{}, err
		}
	}

	log.Info("Mission spec validation passed", "mission", mission.Name)
	err := status.ForMission(mission).
		Phase(aiv1alpha1.MissionPhaseProvisioning).
		Apply(ctx, r.Client)
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	r.Recorder.Eventf(mission, corev1.EventTypeNormal, "PhaseTransition", "Mission transitioned to %s", aiv1alpha1.MissionPhaseProvisioning)
	return ctrl.Result{RequeueAfter: 1 * time.Second}, err
}

// reconcileProvisioning creates the ephemeral RoundTable and NATS streams if needed.

// nextPhaseAfterProvisioning returns Planning if metaMission, otherwise Assembling.
func nextPhaseAfterProvisioning(mission *aiv1alpha1.Mission) aiv1alpha1.MissionPhase {
	if mission.Spec.MetaMission {
		return aiv1alpha1.MissionPhasePlanning
	}
	return aiv1alpha1.MissionPhaseAssembling
}

func (r *MissionReconciler) reconcileProvisioning(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If roundTableRef is already set, skip provisioning (using existing RT)
	if mission.Spec.RoundTableRef != "" {
		log.Info("Using existing RoundTable", "roundTable", mission.Spec.RoundTableRef)
		mission.Status.Phase = nextPhaseAfterProvisioning(mission)
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
	}

	// If no ephemeral knights, skip ephemeral RT creation (v1 compatibility)
	hasEphemeral := false
	for _, mk := range mission.Spec.Knights {
		if mk.Ephemeral {
			hasEphemeral = true
			break
		}
	}
	if !hasEphemeral {
		log.Info("No ephemeral knights, skipping ephemeral RoundTable creation")
		mission.Status.Phase = nextPhaseAfterProvisioning(mission)
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
	}

	// Generate resource names
	uid8 := string(mission.UID)[:8]
	roundTableName := fmt.Sprintf("mission-%s-%s", mission.Name, uid8)
	natsPrefix := fmt.Sprintf("msn-%s-%s", mission.Name, uid8)
	serviceAccountName := fmt.Sprintf("mission-%s", mission.Name)
	// Use underscores in stream names (NATS requirement)
	tasksStream := fmt.Sprintf("msn_%s_%s_tasks", strings.ReplaceAll(mission.Name, "-", "_"), uid8)
	resultsStream := fmt.Sprintf("msn_%s_%s_results", strings.ReplaceAll(mission.Name, "-", "_"), uid8)

	// 1. Create ServiceAccount (if not exists)
	if err := r.Assembler.EnsureMissionServiceAccount(ctx, mission, serviceAccountName); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure ServiceAccount: %w", err)
	}

	// 2. Create NetworkPolicy (if not exists)
	if err := r.Assembler.EnsureMissionNetworkPolicy(ctx, mission); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure NetworkPolicy: %w", err)
	}

	// 3. Check if ephemeral RoundTable already exists
	rt := &aiv1alpha1.RoundTable{}
	rtKey := types.NamespacedName{Name: roundTableName, Namespace: mission.Namespace}
	err := r.Get(ctx, rtKey, rt)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get ephemeral RoundTable: %w", err)
		}

		// RoundTable doesn't exist, create it
		rt = r.Assembler.BuildEphemeralRoundTable(ctx, mission, roundTableName, natsPrefix, tasksStream, resultsStream)

		log.Info("Creating ephemeral RoundTable", "name", roundTableName)
		if err := r.Create(ctx, rt); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to create ephemeral RoundTable: %w", err)
		}

		// Update Mission status with RoundTable name and stream names
		mission.Status.RoundTableName = roundTableName
		mission.Status.NATSTasksStream = tasksStream
		mission.Status.NATSResultsStream = resultsStream
		mission.Status.ObservedGeneration = mission.Generation

		if err := r.Status().Update(ctx, mission); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update mission status: %w", err)
		}

		// Requeue to wait for RoundTable to become Ready
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// RoundTable exists, check if it's Ready
	if rt.Status.Phase != aiv1alpha1.RoundTablePhaseReady {
		log.Info("Waiting for ephemeral RoundTable to become Ready",
			"roundTable", roundTableName,
			"currentPhase", rt.Status.Phase)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// RoundTable is Ready, streams are created
	log.Info("Ephemeral RoundTable ready, transitioning to Assembling",
		"roundTable", roundTableName,
		"tasksStream", tasksStream,
		"resultsStream", resultsStream)

	// Transition to Assembling phase
	mission.Status.Phase = nextPhaseAfterProvisioning(mission)
	mission.Status.ObservedGeneration = mission.Generation
	if err := r.Status().Update(ctx, mission); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to transition to Assembling: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// reconcileAssembling creates ephemeral knights and validates all knight references for readiness.
func (r *MissionReconciler) reconcileAssembling(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	oldPhase := mission.Status.Phase

	// Delegate to KnightAssembler
	result, err := r.Assembler.ReconcileAssembling(ctx, mission)
	if err != nil {
		return result, err
	}

	// Update status after assembly
	if err := r.Status().Update(ctx, mission); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update mission status: %w", err)
	}

	// Emit events for phase transitions and assembly completion
	if mission.Status.Phase != oldPhase {
		r.Recorder.Eventf(mission, corev1.EventTypeNormal, "PhaseTransition", "Mission transitioned to %s", mission.Status.Phase)
	}
	if mission.Status.Phase == aiv1alpha1.MissionPhaseBriefing {
		knightCount := len(mission.Status.KnightStatuses)
		r.Recorder.Eventf(mission, corev1.EventTypeNormal, "KnightsAssembled", "%d knights assembled", knightCount)
	}

	return result, nil
}

// reconcileBriefing publishes the mission briefing to NATS and transitions to Active.
func (r *MissionReconciler) reconcileBriefing(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Publish briefing to NATS
	if mission.Spec.Briefing != "" {
		if err := r.publishBriefing(ctx, mission); err != nil {
			log.Error(err, "Failed to publish briefing, will retry")
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionBriefingPublished,
				Status:             metav1.ConditionFalse,
				Reason:             aiv1alpha1.ReasonBriefingPublishFailed,
				Message:            fmt.Sprintf("Failed to publish briefing: %v", err),
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			_ = r.Status().Update(ctx, mission)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		log.Info("Briefing published", "mission", mission.Name)
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionBriefingPublished,
			Status:             metav1.ConditionTrue,
			Reason:             aiv1alpha1.ReasonBriefingPublished,
			Message:            "Mission briefing published to all knights",
			ObservedGeneration: mission.Generation,
		})
	} else {
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionBriefingPublished,
			Status:             metav1.ConditionTrue,
			Reason:             aiv1alpha1.ReasonNoBriefing,
			Message:            "No briefing text configured",
			ObservedGeneration: mission.Generation,
		})
	}

	// Bug #3 Fix: Trigger mission-generated chains to Running phase.
	// Generated chains remain in Idle after the planner creates them.
	// The chain controller only triggers chains via cron schedule, so mission-generated
	// chains need to be manually started by setting their status.phase to Running.
	if err := r.triggerGeneratedChains(ctx, mission); err != nil {
		log.Error(err, "Failed to trigger generated chains, will retry")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	mission.Status.Phase = aiv1alpha1.MissionPhaseActive
	mission.Status.ObservedGeneration = mission.Generation
	err := r.Status().Update(ctx, mission)
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	r.Recorder.Eventf(mission, corev1.EventTypeNormal, "PhaseTransition", "Mission transitioned to %s", aiv1alpha1.MissionPhaseActive)
	return ctrl.Result{RequeueAfter: 1 * time.Second}, err
}

// reconcileActive monitors chain execution, timeout, and knight status.
func (r *MissionReconciler) reconcileActive(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check timeout
	if mission.Status.StartedAt != nil {
		elapsed := time.Since(mission.Status.StartedAt.Time)
		if elapsed > time.Duration(mission.Spec.Timeout)*time.Second {
			log.Info("Mission timed out", "mission", mission.Name, "elapsed", elapsed)
			err := status.ForMission(mission).
				Failed(fmt.Sprintf("Mission timed out after %ds", mission.Spec.Timeout)).
				Condition(aiv1alpha1.ConditionMissionComplete, aiv1alpha1.ReasonMissionTimeout,
					fmt.Sprintf("Mission timed out after %ds", mission.Spec.Timeout),
					metav1.ConditionTrue).
				Apply(ctx, r.Client)
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
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

					// Suspend all mission-owned chains to prevent further cost
					if err := r.suspendMissionChains(ctx, mission); err != nil {
						log.Error(err, "Failed to suspend mission chains")
					}

					err := status.ForMission(mission).
						Failed(fmt.Sprintf("Cost budget exceeded: $%.2f > $%.2f", totalCost, budget)).
						Condition(aiv1alpha1.ConditionMissionComplete, aiv1alpha1.ReasonOverBudget,
							fmt.Sprintf("Cost $%.2f exceeded budget $%.2f", totalCost, budget),
							metav1.ConditionTrue).
						Apply(ctx, r.Client)
					if apierrors.IsConflict(err) {
						return ctrl.Result{Requeue: true}, nil
					}
					return ctrl.Result{}, err
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

		// FIX #2: Guard transition to terminal state - ensure all chains in status.chainStatuses
		// have reached terminal phase before allowing mission to transition to Succeeded/Failed.
		// This prevents premature cleanup while chains are still running.
		if anyChainFailed || allChainsComplete {
			// Double-check all chains in status have reached terminal state
			hasNonTerminalChains := false
			for _, cs := range mission.Status.ChainStatuses {
				if cs.Phase != aiv1alpha1.ChainPhaseSucceeded && cs.Phase != aiv1alpha1.ChainPhaseFailed {
					hasNonTerminalChains = true
					log.V(1).Info("Waiting for chain to reach terminal state",
						"chain", cs.Name,
						"currentPhase", cs.Phase)
					break
				}
			}

			if hasNonTerminalChains {
				// Requeue and wait for all chains to finish
				log.Info("Chains still running, waiting for completion before transitioning mission")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}

		if anyChainFailed {
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "One or more mission chains failed"
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionMissionComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonMissionChainFailed,
				Message:            "One or more mission chains failed",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			err := r.Status().Update(ctx, mission)
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}

		if allChainsComplete {
			mission.Status.Phase = aiv1alpha1.MissionPhaseSucceeded
			now := metav1.Now()
			mission.Status.CompletedAt = &now
			mission.Status.Result = "All mission chains completed successfully"
			meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionMissionComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonMissionSucceeded,
				Message:            "All mission chains completed successfully",
				ObservedGeneration: mission.Generation,
			})
			mission.Status.ObservedGeneration = mission.Generation
			err := r.Status().Update(ctx, mission)
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	} else {
		// No chains — stay Active until TTL expires or external completion.
		// Knights may still receive ad-hoc tasks via NATS during the mission window.
		log.V(1).Info("No chains defined, mission remains Active awaiting TTL or external completion",
			"mission", mission.Name)
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

		// Trigger idle chains — mission-scoped chains have no schedule,
		// so the mission controller must kick them to Running.
		if chain.Status.Phase == aiv1alpha1.ChainPhaseIdle || chain.Status.Phase == "" {
			// Re-fetch to avoid conflict errors (chain controller may have reconciled)
			if err := r.Get(ctx, types.NamespacedName{
				Name:      missionChainName,
				Namespace: mission.Namespace,
			}, chain); err != nil {
				log.Error(err, "Failed to re-fetch chain for trigger", "chain", missionChainName)
				allComplete = false
				continue
			}
			now := metav1.Now()
			chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
			chain.Status.StartedAt = &now
			if err := r.Status().Update(ctx, chain); err != nil {
				if apierrors.IsConflict(err) {
					log.Info("Conflict triggering chain, will retry", "chain", missionChainName)
					allComplete = false
					continue
				}
				return false, false, fmt.Errorf("failed to trigger chain %s: %w", missionChainName, err)
			}
			log.Info("Triggered mission chain", "chain", missionChainName)
			r.updateChainStatus(mission, chainRef.Name, missionChainName, aiv1alpha1.ChainPhaseRunning)
			allComplete = false
			continue
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

	// If cleanup already completed, skip directly to terminal phase transition.
	// This prevents repeated delete attempts on every reconcile loop.
	if meta.IsStatusConditionTrue(mission.Status.Conditions, aiv1alpha1.ConditionCleanupComplete) {
		return r.transitionToTerminalPhase(ctx, mission)
	}

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

	// Store results to NATS KV if retainResults is true and not already stored
	if mission.Spec.RetainResults && mission.Status.ResultsConfigMap == "" {
		if err := r.storeResultsToKV(ctx, mission); err != nil {
			log.Error(err, "Failed to store results to NATS KV")
			// Continue with cleanup even if this fails — set the key anyway
			// to prevent infinite retry loop
			mission.Status.ResultsConfigMap = fmt.Sprintf("mission-results.%s", mission.Name)
		}
	}

	// Determine if we should actually delete resources based on cleanupPolicy
	shouldDelete := r.shouldDeleteResources(mission)

	if shouldDelete {
		// Step 1: Delete ephemeral Knight CRs
		log.Info("Deleting ephemeral Knight CRs")
		if err := r.deleteEphemeralKnights(ctx, mission); err != nil {
			log.Error(err, "Failed to delete ephemeral knights, retrying")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// Step 2: Delete NATS consumers (best effort)
		log.Info("Deleting NATS consumers")
		if err := r.deleteNATSConsumers(ctx, mission); err != nil {
			log.Error(err, "Failed to delete NATS consumers (best effort, continuing)")
			// Continue even if this fails - consumers will be deleted when stream is deleted
		}

		// Step 3: Delete NATS streams
		if mission.Status.NATSTasksStream != "" || mission.Status.NATSResultsStream != "" {
			log.Info("Deleting NATS streams",
				"tasksStream", mission.Status.NATSTasksStream,
				"resultsStream", mission.Status.NATSResultsStream)
			if err := r.deleteNATSStreams(ctx, mission); err != nil {
				log.Error(err, "Failed to delete NATS streams, retrying with backoff")
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}

		// Step 4: Delete ephemeral RoundTable (owner ref handles cascade)
		if mission.Status.RoundTableName != "" {
			log.Info("Deleting ephemeral RoundTable", "name", mission.Status.RoundTableName)
			if err := r.deleteEphemeralRoundTable(ctx, mission); err != nil {
				log.Error(err, "Failed to delete RoundTable, retrying")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}

	// Mark cleanup as done
	meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
		Type:               aiv1alpha1.ConditionCleanupComplete,
		Status:             metav1.ConditionTrue,
		Reason:             aiv1alpha1.ReasonCleanupComplete,
		Message:            "Mission cleanup completed",
		ObservedGeneration: mission.Generation,
	})

	r.Recorder.Event(mission, corev1.EventTypeNormal, "CleanupComplete", "Mission resources cleaned up")

	return r.transitionToTerminalPhase(ctx, mission)
}

// transitionToTerminalPhase determines the final mission state and handles self-deletion.
func (r *MissionReconciler) transitionToTerminalPhase(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Transition to terminal phase based on original outcome
	if mission.Status.Phase != aiv1alpha1.MissionPhaseSucceeded &&
		mission.Status.Phase != aiv1alpha1.MissionPhaseFailed &&
		mission.Status.Phase != aiv1alpha1.MissionPhaseExpired {
		// Determine terminal phase from chain results and planning outcome
		allSucceeded := true

		// Check if planning failed
		if mission.Status.Result != "" && strings.Contains(mission.Status.Result, "failed") {
			allSucceeded = false
		}

		for _, cs := range mission.Status.ChainStatuses {
			if cs.Phase == aiv1alpha1.ChainPhaseFailed {
				allSucceeded = false
				break
			}
		}
		if allSucceeded {
			mission.Status.Phase = aiv1alpha1.MissionPhaseSucceeded
		} else {
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
		}
	}

	mission.Status.ObservedGeneration = mission.Generation
	_ = r.Status().Update(ctx, mission)

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

// publishBriefing publishes the mission briefing to NATS.
func (r *MissionReconciler) publishBriefing(ctx context.Context, mission *aiv1alpha1.Mission) error {
	client, err := r.natsClient()
	if err != nil {
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
	if err := client.PublishJSON(subject, payload); err != nil {
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

		// Derive subject prefix from the knight's NATS config
		briefingPrefix := natsPrefix(mission)
		if len(knight.Spec.NATS.Subjects) > 0 {
			parts := strings.SplitN(knight.Spec.NATS.Subjects[0], ".tasks.", 2)
			if len(parts) == 2 {
				briefingPrefix = parts[0]
			}
		}
		taskSubject := natspkg.TaskSubject(briefingPrefix, knight.Spec.Domain, mk.Name)
		if err := client.PublishJSON(taskSubject, taskPayload); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to publish briefing to knight", "knight", mk.Name)
		}
	}

	return nil
}

// storeResultsToKV stores mission results in a NATS KV bucket for retention.
// Bucket: "mission-results", Key: mission name, Value: JSON with all results.
// KV Put is idempotent — no "already exists" problem like ConfigMaps.
func (r *MissionReconciler) storeResultsToKV(ctx context.Context, mission *aiv1alpha1.Mission) error {
	log := logf.FromContext(ctx)

	client, err := r.natsClient()
	if err != nil || !client.IsConnected() {
		log.Info("NATS not available, skipping KV results storage")
		return fmt.Errorf("NATS client not available: %w", err)
	}

	kvKey := mission.Name
	const kvBucket = "mission-results"

	// Build duration string
	duration := ""
	if mission.Status.StartedAt != nil && mission.Status.CompletedAt != nil {
		dur := mission.Status.CompletedAt.Sub(mission.Status.StartedAt.Time)
		duration = fmt.Sprintf("%.0fm%.0fs", dur.Minutes(), dur.Seconds()-dur.Minutes()*60)
	}

	// Build complete results document
	results := map[string]interface{}{
		"summary": map[string]interface{}{
			"mission":   mission.Name,
			"objective": mission.Spec.Objective,
			"phase":     string(mission.Status.Phase),
			"result":    mission.Status.Result,
			"duration":  duration,
			"totalCost": mission.Status.TotalCost,
		},
		"timeline": map[string]interface{}{
			"started":   "",
			"completed": "",
			"phases":    []map[string]string{},
		},
		"chains":  map[string]interface{}{},
		"knights": []map[string]interface{}{},
	}

	// Timeline
	timeline := results["timeline"].(map[string]interface{})
	if mission.Status.StartedAt != nil {
		timeline["started"] = mission.Status.StartedAt.Format(time.RFC3339)
	}
	if mission.Status.CompletedAt != nil {
		timeline["completed"] = mission.Status.CompletedAt.Format(time.RFC3339)
	}
	for _, cond := range mission.Status.Conditions {
		timeline["phases"] = append(timeline["phases"].([]map[string]string), map[string]string{
			"type":      cond.Type,
			"status":    string(cond.Status),
			"reason":    cond.Reason,
			"timestamp": cond.LastTransitionTime.Format(time.RFC3339),
		})
	}

	// Chain results
	chains := results["chains"].(map[string]interface{})
	for _, cs := range mission.Status.ChainStatuses {
		chain := &aiv1alpha1.Chain{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      cs.ChainCRName,
			Namespace: mission.Namespace,
		}, chain); err != nil {
			log.Info("Failed to fetch chain for results", "chain", cs.ChainCRName, "error", err.Error())
			continue
		}

		chainResult := map[string]interface{}{
			"name":   cs.Name,
			"phase":  string(cs.Phase),
			"steps":  []map[string]interface{}{},
			"output": "",
		}
		for _, stepStatus := range chain.Status.StepStatuses {
			chainResult["steps"] = append(chainResult["steps"].([]map[string]interface{}), map[string]interface{}{
				"name":   stepStatus.Name,
				"output": stepStatus.Output,
				"error":  stepStatus.Error,
			})
			if stepStatus.Output != "" {
				chainResult["output"] = stepStatus.Output
			}
		}
		chains[cs.Name] = chainResult
	}

	// Knight statuses
	for _, ks := range mission.Status.KnightStatuses {
		results["knights"] = append(results["knights"].([]map[string]interface{}), map[string]interface{}{
			"name":      ks.Name,
			"ready":     ks.Ready,
			"ephemeral": ks.Ephemeral,
		})
	}

	// Cost breakdown
	if len(mission.Status.CostBreakdown) > 0 {
		results["costs"] = map[string]interface{}{
			"breakdown": mission.Status.CostBreakdown,
			"total":     mission.Status.TotalCost,
		}
	}

	// Marshal and store — KVPut is idempotent (upsert)
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("failed to marshal results: %w", err)
	}

	if err := client.KVPut(kvBucket, kvKey, resultsJSON); err != nil {
		return fmt.Errorf("failed to store results in NATS KV: %w", err)
	}

	mission.Status.ResultsConfigMap = fmt.Sprintf("%s.%s", kvBucket, kvKey)
	log.Info("Stored mission results to NATS KV", "bucket", kvBucket, "key", kvKey,
		"chains", len(mission.Status.ChainStatuses), "bytes", len(resultsJSON))
	return nil
}

// suspendMissionChains suspends all mission-owned chains to prevent further cost accumulation.
func (r *MissionReconciler) suspendMissionChains(ctx context.Context, mission *aiv1alpha1.Mission) error {
	log := logf.FromContext(ctx)

	for _, cs := range mission.Status.ChainStatuses {
		if cs.ChainCRName == "" {
			continue
		}

		chain := &aiv1alpha1.Chain{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      cs.ChainCRName,
			Namespace: mission.Namespace,
		}, chain); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue // Chain not found, skip
			}
			log.Error(err, "Failed to fetch chain for suspension", "chain", cs.ChainCRName)
			continue
		}

		// Set suspended flag
		if !chain.Spec.Suspended {
			chain.Spec.Suspended = true
			if err := r.Update(ctx, chain); err != nil {
				log.Error(err, "Failed to suspend chain", "chain", cs.ChainCRName)
				continue
			}
			log.Info("Suspended chain due to budget exceeded", "chain", cs.ChainCRName)
		}
	}

	return nil
}

// aggregateMissionCost calculates the total cost across all mission knights and updates costBreakdown.
func (r *MissionReconciler) aggregateMissionCost(ctx context.Context, mission *aiv1alpha1.Mission) (float64, error) {
	var totalCost float64
	costBreakdown := make([]aiv1alpha1.MissionKnightCost, 0, len(mission.Spec.Knights))

	for _, mk := range mission.Spec.Knights {
		knightName := mk.Name
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      knightName,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			if client.IgnoreNotFound(err) == nil {
				// Knight not found, add to breakdown with zero cost
				costBreakdown = append(costBreakdown, aiv1alpha1.MissionKnightCost{
					Name:      knightName,
					CostUSD:   "0.0000",
					Ephemeral: mk.Ephemeral,
				})
				continue
			}
			return 0, err
		}

		// Parse knight's total cost
		knightCost := "0.0000"
		if knight.Status.TotalCost != "" {
			var cost float64
			if _, err := fmt.Sscanf(knight.Status.TotalCost, "%f", &cost); err == nil {
				totalCost += cost
				knightCost = knight.Status.TotalCost
			}
		}

		// Add to breakdown
		costBreakdown = append(costBreakdown, aiv1alpha1.MissionKnightCost{
			Name:      knightName,
			CostUSD:   knightCost,
			Ephemeral: mk.Ephemeral,
		})
	}

	// Update mission status with breakdown
	mission.Status.CostBreakdown = costBreakdown

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

// triggerGeneratedChains transitions all mission-generated chains from Idle to Running.
// This is necessary because the chain controller only triggers chains via cron schedule,
// and mission-generated chains have no schedule.
func (r *MissionReconciler) triggerGeneratedChains(ctx context.Context, mission *aiv1alpha1.Mission) error {
	log := logf.FromContext(ctx)

	// Iterate all chains owned by this mission
	chainList := &aiv1alpha1.ChainList{}
	if err := r.List(ctx, chainList,
		client.InNamespace(mission.Namespace),
		client.MatchingLabels{aiv1alpha1.LabelMission: mission.Name},
	); err != nil {
		return fmt.Errorf("failed to list mission chains: %w", err)
	}

	for _, chain := range chainList.Items {
		// Only trigger chains that are in Idle phase
		if chain.Status.Phase != aiv1alpha1.ChainPhaseIdle && chain.Status.Phase != "" {
			continue
		}

		// Transition to Running
		log.Info("Triggering generated chain", "chain", chain.Name)
		now := metav1.Now()
		chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
		chain.Status.StartedAt = &now
		if err := r.Status().Update(ctx, &chain); err != nil {
			// Log but don't fail the mission - the chain controller will eventually reconcile
			log.Error(err, "Failed to trigger chain (will retry)", "chain", chain.Name)
			return fmt.Errorf("failed to trigger chain %s: %w", chain.Name, err)
		}
	}

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

// shouldDeleteResources determines if resources should be deleted based on cleanup policy and mission outcome.
func (r *MissionReconciler) shouldDeleteResources(mission *aiv1alpha1.Mission) bool {
	switch mission.Spec.CleanupPolicy {
	case "Delete":
		return true
	case "Retain":
		return false
	case "OnSuccess":
		return mission.Status.Phase == aiv1alpha1.MissionPhaseSucceeded
	case "OnFailure":
		return mission.Status.Phase == aiv1alpha1.MissionPhaseFailed
	default:
		return true // Default to Delete
	}
}

// deleteEphemeralKnights deletes all ephemeral Knight CRs owned by this mission.
func (r *MissionReconciler) deleteEphemeralKnights(ctx context.Context, mission *aiv1alpha1.Mission) error {
	for _, ks := range mission.Status.KnightStatuses {
		if !ks.Ephemeral {
			continue
		}

		knightName := fmt.Sprintf("%s-%s", mission.Name, ks.Name)
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      knightName,
			Namespace: mission.Namespace,
		}, knight); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue // Already deleted
			}
			return err
		}

		if err := r.Delete(ctx, knight); err != nil {
			return fmt.Errorf("failed to delete knight %s: %w", knightName, err)
		}
	}
	return nil
}

// deleteNATSConsumers deletes all NATS consumers for this mission's streams (best effort).
func (r *MissionReconciler) deleteNATSConsumers(ctx context.Context, mission *aiv1alpha1.Mission) error {
	client, err := r.natsClient()
	if err != nil {
		return nil // Gracefully skip if no NATS client
	}

	tasksStream := mission.Status.NATSTasksStream
	resultsStream := mission.Status.NATSResultsStream

	for _, ks := range mission.Status.KnightStatuses {
		if !ks.Ephemeral {
			continue
		}

		consumerName := fmt.Sprintf("msn-%s-%s", mission.Name, ks.Name)

		// Delete from tasks stream (best effort)
		if tasksStream != "" {
			_ = client.DeleteConsumer(tasksStream, consumerName)
		}

		// Delete from results stream (best effort)
		if resultsStream != "" {
			_ = client.DeleteConsumer(resultsStream, consumerName)
		}
	}

	return nil
}

// deleteNATSStreams deletes the mission's task and result streams.
func (r *MissionReconciler) deleteNATSStreams(ctx context.Context, mission *aiv1alpha1.Mission) error {
	client, err := r.natsClient()
	if err != nil {
		return nil // Gracefully skip if no NATS client
	}

	// Delete tasks stream
	if mission.Status.NATSTasksStream != "" {
		if err := client.DeleteStream(mission.Status.NATSTasksStream); err != nil {
			return fmt.Errorf("failed to delete tasks stream: %w", err)
		}
	}

	// Delete results stream
	if mission.Status.NATSResultsStream != "" {
		if err := client.DeleteStream(mission.Status.NATSResultsStream); err != nil {
			return fmt.Errorf("failed to delete results stream: %w", err)
		}
	}

	return nil
}

// deleteEphemeralRoundTable deletes the mission's ephemeral RoundTable CR.
func (r *MissionReconciler) deleteEphemeralRoundTable(ctx context.Context, mission *aiv1alpha1.Mission) error {
	if mission.Status.RoundTableName == "" {
		return nil // No RoundTable to delete
	}

	rt := &aiv1alpha1.RoundTable{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      mission.Status.RoundTableName,
		Namespace: mission.Namespace,
	}, rt); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil // Already deleted
		}
		return err
	}

	return r.Delete(ctx, rt)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Mission{}).
		Owns(&aiv1alpha1.Chain{}).
		Owns(&aiv1alpha1.Knight{}).
		Owns(&aiv1alpha1.RoundTable{}).
		Named("mission").
		Complete(r)
}
