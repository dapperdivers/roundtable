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
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
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
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

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
		mission.Status.Phase = aiv1alpha1.MissionPhasePending
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
	case aiv1alpha1.MissionPhasePending:
		return r.reconcilePending(ctx, mission)
	case aiv1alpha1.MissionPhaseProvisioning:
		return r.reconcileProvisioning(ctx, mission)
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
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			mission.Status.Result = fmt.Sprintf("Duplicate knight name: %s", knight.Name)
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}
		knightNames[knight.Name] = true

		// If using templateRef, validate it exists
		if knight.TemplateRef != "" && !templateNames[knight.TemplateRef] {
			mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
			mission.Status.Result = fmt.Sprintf("Knight %s references unknown template: %s", knight.Name, knight.TemplateRef)
			mission.Status.ObservedGeneration = mission.Generation
			return ctrl.Result{}, r.Status().Update(ctx, mission)
		}

		// Validate ephemeral knights have spec OR templateRef (not both, not neither)
		if knight.Ephemeral {
			hasSpec := knight.EphemeralSpec != nil
			hasTemplate := knight.TemplateRef != ""
			if hasSpec == hasTemplate { // XOR check
				mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
				mission.Status.Result = fmt.Sprintf("Ephemeral knight %s must have exactly one of ephemeralSpec or templateRef", knight.Name)
				mission.Status.ObservedGeneration = mission.Generation
				return ctrl.Result{}, r.Status().Update(ctx, mission)
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
				mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
				mission.Status.Result = fmt.Sprintf("Referenced chain not found: %s", chainRef.Name)
				mission.Status.ObservedGeneration = mission.Generation
				return ctrl.Result{}, r.Status().Update(ctx, mission)
			}
			return ctrl.Result{}, err
		}
	}

	log.Info("Mission spec validation passed", "mission", mission.Name)
	mission.Status.Phase = aiv1alpha1.MissionPhaseProvisioning
	mission.Status.ObservedGeneration = mission.Generation
	return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
}

// reconcileProvisioning creates the ephemeral RoundTable and NATS streams if needed.
func (r *MissionReconciler) reconcileProvisioning(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If roundTableRef is already set, skip provisioning (using existing RT)
	if mission.Spec.RoundTableRef != "" {
		log.Info("Using existing RoundTable", "roundTable", mission.Spec.RoundTableRef)
		mission.Status.Phase = aiv1alpha1.MissionPhaseAssembling
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
	if err := r.ensureMissionServiceAccount(ctx, mission, serviceAccountName); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure ServiceAccount: %w", err)
	}

	// 2. Create NetworkPolicy (if not exists)
	if err := r.ensureMissionNetworkPolicy(ctx, mission); err != nil {
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
		rt = r.buildEphemeralRoundTable(mission, roundTableName, natsPrefix, tasksStream, resultsStream)

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
	mission.Status.Phase = aiv1alpha1.MissionPhaseAssembling
	mission.Status.ObservedGeneration = mission.Generation
	if err := r.Status().Update(ctx, mission); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to transition to Assembling: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// reconcileAssembling creates ephemeral knights and validates all knight references for readiness.
func (r *MissionReconciler) reconcileAssembling(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Get ephemeral RoundTable (if one was created)
	var rt *aiv1alpha1.RoundTable
	if mission.Status.RoundTableName != "" {
		rt = &aiv1alpha1.RoundTable{}
		rtKey := types.NamespacedName{Name: mission.Status.RoundTableName, Namespace: mission.Namespace}
		if err := r.Get(ctx, rtKey, rt); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get ephemeral RoundTable: %w", err)
		}
	}

	// Track assembly progress
	knightStatuses := make(map[string]aiv1alpha1.MissionKnightStatus)
	for _, existing := range mission.Status.KnightStatuses {
		knightStatuses[existing.Name] = existing
	}

	allReady := true
	var notReadyKnights []string

	// Process each knight in spec
	for _, mk := range mission.Spec.Knights {
		if !mk.Ephemeral {
			// Recruited knight - verify it exists and is Ready
			existingKnight := &aiv1alpha1.Knight{}
			knightKey := types.NamespacedName{Name: mk.Name, Namespace: mission.Namespace}
			if err := r.Get(ctx, knightKey, existingKnight); err != nil {
				if client.IgnoreNotFound(err) == nil {
					log.Error(err, "Recruited knight not found", "knight", mk.Name)
					meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
						Type:               "KnightsReady",
						Status:             metav1.ConditionFalse,
						Reason:             "KnightNotFound",
						Message:            fmt.Sprintf("Recruited knight %q not found", mk.Name),
						ObservedGeneration: mission.Generation,
					})
					mission.Status.ObservedGeneration = mission.Generation
					_ = r.Status().Update(ctx, mission)
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				}
				return ctrl.Result{}, err
			}

			if existingKnight.Status.Phase != aiv1alpha1.KnightPhaseReady || !existingKnight.Status.Ready {
				allReady = false
				notReadyKnights = append(notReadyKnights, mk.Name)
			}

			// Update status
			knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
				Name:      mk.Name,
				Ephemeral: false,
				Ready:     existingKnight.Status.Ready,
			}
			continue
		}

		// Ephemeral knight - create if doesn't exist
		knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)
		knight := &aiv1alpha1.Knight{}
		knightKey := types.NamespacedName{Name: knightName, Namespace: mission.Namespace}
		err := r.Get(ctx, knightKey, knight)

		if err != nil && client.IgnoreNotFound(err) == nil {
			// Create ephemeral knight
			if rt == nil {
				return ctrl.Result{}, fmt.Errorf("cannot create ephemeral knight without RoundTable")
			}

			knight, err := r.buildEphemeralKnight(ctx, mission, mk, rt)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to build ephemeral knight %q: %w", mk.Name, err)
			}

			log.Info("Creating ephemeral knight", "name", knightName)
			if err := r.Create(ctx, knight); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create ephemeral knight: %w", err)
			}

			// Update status (not ready yet)
			knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
				Name:      mk.Name,
				Ephemeral: true,
				Ready:     false,
			}
			allReady = false
			notReadyKnights = append(notReadyKnights, mk.Name)
			continue
		} else if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get knight %q: %w", knightName, err)
		}

		// Knight exists, check readiness
		if knight.Status.Phase != aiv1alpha1.KnightPhaseReady || !knight.Status.Ready {
			allReady = false
			notReadyKnights = append(notReadyKnights, mk.Name)
		}

		// Update status
		knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
			Name:      mk.Name,
			Ephemeral: true,
			Ready:     knight.Status.Ready,
		}
	}

	// Update mission status with knight statuses
	mission.Status.KnightStatuses = make([]aiv1alpha1.MissionKnightStatus, 0, len(knightStatuses))
	for _, ks := range knightStatuses {
		mission.Status.KnightStatuses = append(mission.Status.KnightStatuses, ks)
	}

	if err := r.Status().Update(ctx, mission); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update mission status: %w", err)
	}

	// Check assembly timeout (Timeout/3)
	assemblyTimeout := time.Duration(mission.Spec.Timeout/3) * time.Second
	if mission.Status.StartedAt != nil && time.Since(mission.Status.StartedAt.Time) > assemblyTimeout {
		log.Info("Assembly timeout exceeded",
			"timeout", assemblyTimeout,
			"notReady", notReadyKnights)

		mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
		now := metav1.Now()
		mission.Status.CompletedAt = &now
		mission.Status.Result = fmt.Sprintf("Assembly timeout: knights not ready: %v", notReadyKnights)
		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "KnightsReady",
			Status:             metav1.ConditionFalse,
			Reason:             "AssemblyTimeout",
			Message:            fmt.Sprintf("Knights not ready within %v: %v", assemblyTimeout, notReadyKnights),
			ObservedGeneration: mission.Generation,
		})
		mission.Status.ObservedGeneration = mission.Generation
		if err := r.Status().Update(ctx, mission); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// If all knights ready, transition to Briefing
	if allReady && len(mission.Spec.Knights) > 0 {
		log.Info("All knights assembled, transitioning to Briefing",
			"mission", mission.Name,
			"knightCount", len(mission.Spec.Knights))

		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "KnightsReady",
			Status:             metav1.ConditionTrue,
			Reason:             "AllKnightsReady",
			Message:            fmt.Sprintf("All %d knights are ready", len(mission.Spec.Knights)),
			ObservedGeneration: mission.Generation,
		})
		mission.Status.Phase = aiv1alpha1.MissionPhaseBriefing
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, mission)
	}

	// Not all ready yet, requeue to check again
	log.Info("Waiting for knights to become ready",
		"total", len(mission.Spec.Knights),
		"notReady", len(notReadyKnights))

	return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
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
					
					// Suspend all mission-owned chains to prevent further cost
					if err := r.suspendMissionChains(ctx, mission); err != nil {
						log.Error(err, "Failed to suspend mission chains")
					}
					
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
				// Conflict is transient — requeue, don't fail the mission
				log.Info("Conflict triggering chain, will retry", "chain", missionChainName, "error", err)
				allComplete = false
				continue
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

	// Mark cleanup as done and transition to terminal phase
	meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
		Type:               "CleanupComplete",
		Status:             metav1.ConditionTrue,
		Reason:             "CleanedUp",
		Message:            "Mission cleanup completed",
		ObservedGeneration: mission.Generation,
	})

	// Transition to terminal phase based on chain results
	allSucceeded := true
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

// storeResultsToKV stores mission results in a NATS KV bucket for retention.
// Bucket: "mission-results", Key: mission name, Value: JSON with all results.
// KV Put is idempotent — no "already exists" problem like ConfigMaps.
func (r *MissionReconciler) storeResultsToKV(ctx context.Context, mission *aiv1alpha1.Mission) error {
	log := logf.FromContext(ctx)

	if r.natsClient == nil || !r.natsClient.IsConnected() {
		log.Info("NATS not available, skipping KV results storage")
		return fmt.Errorf("NATS client not available")
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

	if err := r.natsClient.KVPut(kvBucket, kvKey, resultsJSON); err != nil {
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

// resolveKnightSpec resolves a MissionKnight's spec from either ephemeralSpec or templateRef.
func (r *MissionReconciler) resolveKnightSpec(
	mission *aiv1alpha1.Mission,
	mk aiv1alpha1.MissionKnight,
) (*aiv1alpha1.KnightSpec, error) {
	var baseSpec *aiv1alpha1.KnightSpec

	// Path A: Inline spec
	if mk.EphemeralSpec != nil {
		baseSpec = mk.EphemeralSpec.DeepCopy()
	} else if mk.TemplateRef != "" {
		// Path B: Template reference
		var template *aiv1alpha1.MissionKnightTemplate
		for i := range mission.Spec.KnightTemplates {
			if mission.Spec.KnightTemplates[i].Name == mk.TemplateRef {
				template = &mission.Spec.KnightTemplates[i]
				break
			}
		}

		if template == nil {
			return nil, fmt.Errorf("template %q not found in mission.spec.knightTemplates", mk.TemplateRef)
		}

		baseSpec = template.Spec.DeepCopy()

		// Apply overrides (if specified)
		if mk.SpecOverrides != nil {
			r.applySpecOverrides(baseSpec, mk.SpecOverrides)
		}
	} else {
		return nil, fmt.Errorf("ephemeral knight %q has neither ephemeralSpec nor templateRef", mk.Name)
	}

	return baseSpec, nil
}

// applySpecOverrides applies KnightSpecOverrides to a KnightSpec (strategic merge).
func (r *MissionReconciler) applySpecOverrides(
	spec *aiv1alpha1.KnightSpec,
	overrides *aiv1alpha1.KnightSpecOverrides,
) {
	if overrides.Model != "" {
		spec.Model = overrides.Model
	}
	if overrides.Skills != nil {
		spec.Skills = overrides.Skills
	}
	if overrides.Env != nil {
		spec.Env = append(spec.Env, overrides.Env...)
	}
	if overrides.Prompt != nil {
		spec.Prompt = overrides.Prompt
	}
	if overrides.Concurrency != nil {
		spec.Concurrency = *overrides.Concurrency
	}
}

// buildEphemeralKnight creates a Knight CR for an ephemeral mission knight.
func (r *MissionReconciler) buildEphemeralKnight(
	ctx context.Context,
	mission *aiv1alpha1.Mission,
	mk aiv1alpha1.MissionKnight,
	rt *aiv1alpha1.RoundTable,
) (*aiv1alpha1.Knight, error) {
	// Resolve spec from template or inline
	spec, err := r.resolveKnightSpec(mission, mk)
	if err != nil {
		return nil, err
	}

	// Generate knight name
	knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)

	// Override NATS config to point at mission streams
	natsPrefix := rt.Spec.NATS.SubjectPrefix
	spec.NATS = aiv1alpha1.KnightNATS{
		URL:           rt.Spec.NATS.URL,
		Stream:        rt.Spec.NATS.TasksStream,
		ResultsStream: rt.Spec.NATS.ResultsStream,
		Subjects: []string{
			fmt.Sprintf("%s.tasks.%s.>", natsPrefix, spec.Domain),
		},
		ConsumerName: fmt.Sprintf("msn-%s-%s", mission.Name, mk.Name),
		MaxDeliver:   1, // Exactly-once delivery for mission tasks
	}

	// Inject mission secrets (if any)
	if len(mission.Spec.Secrets) > 0 {
		for _, secretRef := range mission.Spec.Secrets {
			spec.EnvFrom = append(spec.EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: secretRef,
				},
			})
		}
	}

	// Ephemeral knights don't get persistent workspace
	spec.Workspace = nil

	// Use mission-scoped ServiceAccount
	spec.ServiceAccountName = fmt.Sprintf("mission-%s", mission.Name)

	// Build Knight CR
	knight := &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knightName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:     mission.Name,
				aiv1alpha1.LabelEphemeral:   "true",
				aiv1alpha1.LabelRoundTable:  rt.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: *spec,
	}

	// Add role label if specified
	if mk.Role != "" {
		knight.Labels[aiv1alpha1.LabelRole] = mk.Role
	}

	return knight, nil
}

// ensureMissionServiceAccount creates a mission-scoped ServiceAccount if it doesn't exist.
func (r *MissionReconciler) ensureMissionServiceAccount(ctx context.Context, mission *aiv1alpha1.Mission, saName string) error {
	sa := &corev1.ServiceAccount{}
	saKey := types.NamespacedName{Name: saName, Namespace: mission.Namespace}
	err := r.Get(ctx, saKey, sa)

	if err != nil && client.IgnoreNotFound(err) == nil {
		// Create ServiceAccount
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: mission.Namespace,
				Labels: map[string]string{
					aiv1alpha1.LabelMission:   mission.Name,
					aiv1alpha1.LabelEphemeral: "true",
				},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
				},
			},
		}

		if err := r.Create(ctx, sa); err != nil {
			return fmt.Errorf("failed to create ServiceAccount: %w", err)
		}
		logf.FromContext(ctx).Info("Created mission ServiceAccount", "name", saName)
	} else if err != nil {
		return fmt.Errorf("failed to get ServiceAccount: %w", err)
	}

	return nil
}

// ensureMissionNetworkPolicy creates a NetworkPolicy to isolate ephemeral knights if it doesn't exist.
func (r *MissionReconciler) ensureMissionNetworkPolicy(ctx context.Context, mission *aiv1alpha1.Mission) error {
	policyName := fmt.Sprintf("mission-%s-isolation", mission.Name)
	policy := &networkingv1.NetworkPolicy{}
	policyKey := types.NamespacedName{Name: policyName, Namespace: mission.Namespace}
	err := r.Get(ctx, policyKey, policy)

	if err != nil && client.IgnoreNotFound(err) == nil {
		// Build NetworkPolicy
		policy = r.buildMissionNetworkPolicy(mission, policyName)

		if err := r.Create(ctx, policy); err != nil {
			return fmt.Errorf("failed to create NetworkPolicy: %w", err)
		}
		logf.FromContext(ctx).Info("Created mission NetworkPolicy", "name", policyName)
	} else if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy: %w", err)
	}

	return nil
}

// buildMissionNetworkPolicy constructs a NetworkPolicy that restricts ephemeral knight egress.
func (r *MissionReconciler) buildMissionNetworkPolicy(mission *aiv1alpha1.Mission, policyName string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:   mission.Name,
				aiv1alpha1.LabelEphemeral: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Apply to all ephemeral knights in this mission
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					aiv1alpha1.LabelMission: mission.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Allow egress to NATS server
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "database",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/name": "nats",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(4222)),
						},
					},
				},
				// Allow DNS resolution (UDP and TCP)
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"k8s-app": "kube-dns",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolUDP),
							Port:     ptr.To(intstr.FromInt(53)),
						},
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(53)),
						},
					},
				},
				// Allow HTTPS egress for AI provider APIs
				// Note: This allows egress to ANY HTTPS endpoint
				// For stricter isolation, use an egress proxy with allowlisting
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(443)),
						},
					},
				},
			},
		},
	}
}

// buildEphemeralRoundTable creates the spec for an ephemeral RoundTable for a mission.
func (r *MissionReconciler) buildEphemeralRoundTable(
	mission *aiv1alpha1.Mission,
	name, natsPrefix, tasksStream, resultsStream string,
) *aiv1alpha1.RoundTable {
	// Get parent RoundTable for defaults (if specified)
	var parentDefaults *aiv1alpha1.RoundTableDefaults
	var parentPolicies *aiv1alpha1.RoundTablePolicies
	natsURL := "nats://nats.database.svc.cluster.local:4222" // Default

	if mission.Spec.RoundTableRef != "" {
		parentRT := &aiv1alpha1.RoundTable{}
		parentKey := types.NamespacedName{Name: mission.Spec.RoundTableRef, Namespace: mission.Namespace}
		// Use background context for fetching defaults (non-critical)
		if err := r.Get(context.Background(), parentKey, parentRT); err == nil {
			if parentRT.Spec.Defaults != nil {
				parentDefaults = parentRT.Spec.Defaults
			}
			if parentRT.Spec.Policies != nil {
				parentPolicies = parentRT.Spec.Policies
			}
			if parentRT.Spec.NATS.URL != "" {
				natsURL = parentRT.Spec.NATS.URL
			}
		}
	}

	// Apply mission template overrides
	if mission.Spec.RoundTableTemplate != nil {
		if mission.Spec.RoundTableTemplate.Defaults != nil {
			parentDefaults = mission.Spec.RoundTableTemplate.Defaults
		}
		if mission.Spec.RoundTableTemplate.Policies != nil {
			parentPolicies = mission.Spec.RoundTableTemplate.Policies
		}
		if mission.Spec.RoundTableTemplate.NATSURL != "" {
			natsURL = mission.Spec.RoundTableTemplate.NATSURL
		}
	}

	// Build RoundTable CR
	rt := &aiv1alpha1.RoundTable{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:   mission.Name,
				aiv1alpha1.LabelEphemeral: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: aiv1alpha1.RoundTableSpec{
			Ephemeral:  true,
			MissionRef: mission.Name,
			NATS: aiv1alpha1.RoundTableNATS{
				URL:             natsURL,
				SubjectPrefix:   natsPrefix,
				TasksStream:     tasksStream,
				ResultsStream:   resultsStream,
				CreateStreams:   true,
				StreamRetention: "WorkQueue", // ALWAYS WorkQueue for ephemeral
			},
		},
	}

	// Apply defaults (with fallback to sensible defaults)
	if parentDefaults != nil {
		rt.Spec.Defaults = parentDefaults
	} else {
		rt.Spec.Defaults = &aiv1alpha1.RoundTableDefaults{
			Model:       "claude-sonnet-4-20250514",
			Concurrency: 3,
			TaskTimeout: 300,
		}
	}

	// Apply policies (initialize if nil)
	if parentPolicies != nil {
		rt.Spec.Policies = parentPolicies
	} else {
		rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{}
	}

	// Override cost budget with mission budget
	if mission.Spec.CostBudgetUSD != "" && mission.Spec.CostBudgetUSD != "0" {
		rt.Spec.Policies.CostBudgetUSD = mission.Spec.CostBudgetUSD
	}

	// Set max knights to mission knight count
	if rt.Spec.Policies == nil {
		rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{}
	}
	knightCount := int32(len(mission.Spec.Knights))
	rt.Spec.Policies.MaxKnights = knightCount

	return rt
}

// ensureMissionServiceAccount creates a mission-scoped ServiceAccount if it doesn't exist.
