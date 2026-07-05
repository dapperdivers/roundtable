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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/dapperdivers/roundtable/internal/util"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/notify"
	"github.com/dapperdivers/roundtable/pkg/metrics"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

const (
	chainFinalizer = "ai.roundtable.io/chain-finalizer"
)

// natsConfig holds resolved NATS configuration for a chain's target RoundTable.
type natsConfig struct {
	SubjectPrefix string // e.g. "table-prefix" or "chelonian"
	TasksStream   string // e.g. "fleet_a_tasks" or "chelonian_tasks"
	ResultsStream string // e.g. "fleet_a_results" or "chelonian_results"
}

// ChainReconciler reconciles a Chain object.
type ChainReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	NATS   *natspkg.Provider
	Notify *notify.Notifier
	cron   *cron.Cron
	mu     sync.Mutex
	// cronEntries maps chain namespace/name to cron entry ID
	cronEntries map[string]cron.EntryID
}

// natsClient returns the shared NATS client, or an error if the provider is not configured.
func (r *ChainReconciler) natsClient() (natspkg.Client, error) {
	if r.NATS == nil {
		return nil, fmt.Errorf("NATS provider not configured")
	}
	return r.NATS.Client()
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=missions,verbs=get;list;watch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *ChainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	chain := &aiv1alpha1.Chain{}
	if err := r.Get(ctx, req.NamespacedName, chain); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Chain is gone — make sure no cron entry outlives it.
			r.removeCronEntry(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if chain.DeletionTimestamp != nil {
		r.removeCronEntry(req.NamespacedName)
		chain.Finalizers = util.RemoveString(chain.Finalizers, chainFinalizer)
		if err := r.Update(ctx, chain); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !util.ContainsString(chain.Finalizers, chainFinalizer) {
		chain.Finalizers = append(chain.Finalizers, chainFinalizer)
		if err := r.Update(ctx, chain); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Validate roundTableRef is present
	if chain.Spec.RoundTableRef == "" && chain.Spec.MissionRef == "" {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionChainValid,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonMissingRoundTableRef,
			Message:            "Chain must have either roundTableRef or missionRef configured",
			ObservedGeneration: chain.Generation,
		})
		chain.Status.Phase = aiv1alpha1.ChainPhaseFailed
		chain.Status.ObservedGeneration = chain.Generation
		if statusErr := r.Status().Update(ctx, chain); statusErr != nil {
			log.Error(statusErr, "Failed to update status during validation error")
		}
		return ctrl.Result{}, fmt.Errorf("chain %s/%s missing roundTableRef or missionRef", chain.Namespace, chain.Name)
	}

	// Validate knight refs
	if err := r.validateKnightRefs(ctx, chain); err != nil {
		// A knight that disappears after the owning mission started cleanup
		// (or is gone / terminal) was deleted BY that cleanup — mission-scoped
		// ephemeral knights are removed before the chain itself is
		// garbage-collected. Reporting InvalidKnightRef then is post-cleanup
		// noise that looks like the dispatch failure cause, so stop
		// reconciling quietly instead of setting conditions and requeuing.
		if apierrors.IsNotFound(err) && r.owningMissionInactive(ctx, chain) {
			log.V(1).Info("Knight missing after owning mission cleanup, stopping reconcile",
				"mission", chain.Labels[aiv1alpha1.LabelMission])
			return ctrl.Result{}, nil
		}
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionChainValid,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonInvalidKnightRef,
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		if statusErr := r.Status().Update(ctx, chain); statusErr != nil {
			log.Error(statusErr, "Failed to update status during validation error")
		}
		return ctrl.Result{}, err
	}

	// Validate DAG
	if err := r.validateDAG(chain); err != nil {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionChainValid,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonCyclicDependency,
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		if statusErr := r.Status().Update(ctx, chain); statusErr != nil {
			log.Error(statusErr, "Failed to update status during validation error")
		}
		return ctrl.Result{}, err
	}

	// Validate templates parse correctly
	if err := r.validateTemplates(chain); err != nil {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionChainValid,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonInvalidTemplate,
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		if statusErr := r.Status().Update(ctx, chain); statusErr != nil {
			log.Error(statusErr, "Failed to update status during validation error")
		}
		return ctrl.Result{}, err
	}

	meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
		Type:               aiv1alpha1.ConditionChainValid,
		Status:             metav1.ConditionTrue,
		Reason:             aiv1alpha1.ReasonChainValid,
		Message:            "Chain spec is valid",
		ObservedGeneration: chain.Generation,
	})

	// Handle schedule, catching up a missed fire (e.g. operator downtime)
	if r.reconcileSchedule(ctx, chain) {
		log.Info("Missed scheduled run detected, triggering catch-up")
		r.triggerChain(ctx, req.NamespacedName)
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle suspended
	if chain.Spec.Suspended {
		chain.Status.Phase = aiv1alpha1.ChainPhaseSuspended
		chain.Status.ObservedGeneration = chain.Generation
		return r.updateStatus(ctx, chain, 0)
	}

	// Initialize status if empty
	if chain.Status.Phase == "" {
		chain.Status.Phase = aiv1alpha1.ChainPhaseIdle
		r.initStepStatuses(chain)
		chain.Status.ObservedGeneration = chain.Generation
		return r.updateStatus(ctx, chain, 0)
	}

	// Reset to Idle when spec changes (generation drift) and chain is not running
	if chain.Status.ObservedGeneration != chain.Generation &&
		chain.Status.Phase != aiv1alpha1.ChainPhaseRunning {
		log.Info("Spec changed, resetting chain to Idle",
			"oldGen", chain.Status.ObservedGeneration,
			"newGen", chain.Generation)
		chain.Status.Phase = aiv1alpha1.ChainPhaseIdle
		r.initStepStatuses(chain)
		// A new run gets its own completion notification.
		meta.RemoveStatusCondition(&chain.Status.Conditions, aiv1alpha1.ConditionNotificationSent)

		// Attempt to restore completed steps from NATS KV (resume capability)
		restored := r.restoreStepOutputsFromKV(ctx, chain)
		if restored > 0 {
			log.Info("Restored step outputs from NATS KV after spec change", "restored", restored)
		}

		chain.Status.ObservedGeneration = chain.Generation
		return r.updateStatus(ctx, chain, 0)
	}

	switch chain.Status.Phase {
	case aiv1alpha1.ChainPhaseIdle:
		// Nothing to do unless triggered (manual trigger sets phase to Running externally)
		return ctrl.Result{}, nil

	case aiv1alpha1.ChainPhaseRunning:
		return r.reconcileRunning(ctx, chain)

	case aiv1alpha1.ChainPhaseSucceeded, aiv1alpha1.ChainPhaseFailed, aiv1alpha1.ChainPhasePartiallySucceeded:
		// Terminal — only a pending completion notification still needs work.
		// Notification state never affects the phase itself.
		if notificationPending(chain.Spec.Notify, chain.Status.Conditions) {
			completedAt := notifyCompletedAt(chain.Status.CompletedAt, chain.Status.Conditions, aiv1alpha1.ConditionChainComplete)
			requeue := deliverNotification(ctx, r.Client, r.Recorder, r.Notify, chain,
				&chain.Status.Conditions, chain.Generation, completedAt, chainNotifyPayload(chain))
			return r.updateStatus(ctx, chain, requeue)
		}
		return ctrl.Result{}, nil

	case aiv1alpha1.ChainPhaseSuspended:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// updateStatus writes the chain status, converting optimistic-concurrency
// conflicts into a requeue instead of a reconcile error. On success the
// result carries requeueAfter (zero means no requeue).
func (r *ChainReconciler) updateStatus(ctx context.Context, chain *aiv1alpha1.Chain, requeueAfter time.Duration) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, chain); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// validateKnightRefs checks that all knightRef values resolve to Knight CRs.
func (r *ChainReconciler) validateKnightRefs(ctx context.Context, chain *aiv1alpha1.Chain) error {
	for _, step := range chain.Spec.Steps {
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      step.KnightRef,
			Namespace: chain.Namespace,
		}, knight); err != nil {
			return fmt.Errorf("step %q references non-existent knight %q: %w", step.Name, step.KnightRef, err)
		}
	}
	return nil
}

// owningMissionInactive reports whether the chain belongs to a mission that no
// longer needs it reconciled: the mission is gone, being deleted, cleaning up,
// or already in a terminal phase (Succeeded/Failed/Expired). Mission cleanup
// deletes the mission's ephemeral knights before the chain is garbage-collected,
// so a chain can transiently reference knights that were legitimately removed.
// A missing knight while the mission is still active (pre-dispatch) is a real
// error and is NOT covered here.
func (r *ChainReconciler) owningMissionInactive(ctx context.Context, chain *aiv1alpha1.Chain) bool {
	missionName := chain.Labels[aiv1alpha1.LabelMission]
	if missionName == "" {
		return false
	}
	mission := &aiv1alpha1.Mission{}
	if err := r.Get(ctx, types.NamespacedName{Name: missionName, Namespace: chain.Namespace}, mission); err != nil {
		// Mission deleted entirely — the chain is an orphan awaiting GC.
		return apierrors.IsNotFound(err)
	}
	if mission.DeletionTimestamp != nil {
		return true
	}
	switch mission.Status.Phase {
	case aiv1alpha1.MissionPhaseCleaningUp,
		aiv1alpha1.MissionPhaseSucceeded,
		aiv1alpha1.MissionPhaseFailed,
		aiv1alpha1.MissionPhaseExpired:
		return true
	}
	return false
}

// validateDAG performs topological sort to detect cycles.
// validateTemplates pre-parses all step task templates to catch syntax errors early.
// Also warns about common mistakes like using lowercase field names.
func (r *ChainReconciler) validateTemplates(chain *aiv1alpha1.Chain) error {
	for _, step := range chain.Spec.Steps {
		if !strings.Contains(step.Task, "{{") {
			continue
		}
		tmpl, err := template.New("validate").Parse(step.Task)
		if err != nil {
			return fmt.Errorf("step %q has invalid template: %w", step.Name, err)
		}
		// Dry-run execute with mock data to catch field access errors
		mockSteps := make(map[string]map[string]string)
		for _, s := range chain.Spec.Steps {
			mockSteps[s.Name] = map[string]string{
				"Output": "",
				"Error":  "",
			}
		}
		mockData := map[string]interface{}{
			"Steps": mockSteps,
			"Input": "",
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, mockData); err != nil {
			return fmt.Errorf("step %q template execution error (hint: use .Steps.stepname.Output not steps.stepname.output): %w", step.Name, err)
		}
	}
	return nil
}

func (r *ChainReconciler) validateDAG(chain *aiv1alpha1.Chain) error {
	// Convert ChainSteps to DAGNodes
	nodes := make([]util.DAGNode, len(chain.Spec.Steps))
	for i, step := range chain.Spec.Steps {
		nodes[i] = util.DAGNode{
			Name:      step.Name,
			DependsOn: step.DependsOn,
		}
	}
	return util.ValidateDAG(nodes)
}

// initStepStatuses initializes step status entries for all steps.
func (r *ChainReconciler) initStepStatuses(chain *aiv1alpha1.Chain) {
	chain.Status.StepStatuses = make([]aiv1alpha1.ChainStepStatus, len(chain.Spec.Steps))
	for i, step := range chain.Spec.Steps {
		chain.Status.StepStatuses[i] = aiv1alpha1.ChainStepStatus{
			Name:  step.Name,
			Phase: aiv1alpha1.ChainStepPhasePending,
		}
	}
}

// reconcileRunning processes the DAG execution for a running chain.
func (r *ChainReconciler) reconcileRunning(ctx context.Context, chain *aiv1alpha1.Chain) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Resolve NATS config from the chain's RoundTable reference
	nc, err := r.resolveNATSConfig(ctx, chain)
	if err != nil {
		log.Error(err, "Failed to resolve NATS config for chain")
		return ctrl.Result{}, err
	}

	// Initialize step statuses and startedAt if missing (manual trigger via status patch)
	if len(chain.Status.StepStatuses) == 0 {
		log.Info("Initializing step statuses for manually triggered chain")
		r.initStepStatuses(chain)
		// A new run gets its own completion notification.
		meta.RemoveStatusCondition(&chain.Status.Conditions, aiv1alpha1.ConditionNotificationSent)

		// A manual trigger starts a new run with its own identity, so KV
		// restore below only picks up outputs this run produced (none yet) —
		// stale outputs from earlier runs can no longer masquerade as results.
		chain.Status.RunID = string(uuid.NewUUID())

		// Attempt to restore completed steps from NATS KV (resume capability)
		restored := r.restoreStepOutputsFromKV(ctx, chain)
		if restored > 0 {
			log.Info("Restored step outputs from NATS KV", "restored", restored)
		}

		now := metav1.Now()
		chain.Status.StartedAt = &now
		chain.Status.ObservedGeneration = chain.Generation
		return r.updateStatus(ctx, chain, RequeueFast)
	}

	// Runs started before run identity existed get an ID on first reconcile.
	if chain.Status.RunID == "" {
		chain.Status.RunID = string(uuid.NewUUID())
	}

	if chain.Status.StartedAt == nil {
		now := metav1.Now()
		chain.Status.StartedAt = &now
		r.Recorder.Event(chain, corev1.EventTypeNormal, "Started", "Chain execution started")
	}

	// Check overall timeout
	if chain.Status.StartedAt != nil {
		elapsed := time.Since(chain.Status.StartedAt.Time)
		if elapsed > time.Duration(chain.Spec.Timeout)*time.Second {
			log.Info("Chain timed out", "elapsed", elapsed)
			chain.Status.Phase = aiv1alpha1.ChainPhaseFailed
			now := metav1.Now()
			chain.Status.CompletedAt = &now
			chain.Status.RunsFailed++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionChainComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonChainTimeout,
				Message:            fmt.Sprintf("Chain timed out after %ds", chain.Spec.Timeout),
				ObservedGeneration: chain.Generation,
			})
			r.Recorder.Eventf(chain, corev1.EventTypeWarning, "Failed", "Chain timed out after %ds", chain.Spec.Timeout)
			chain.Status.ObservedGeneration = chain.Generation
			return ctrl.Result{}, r.Status().Update(ctx, chain)
		}
	}

	// Build step status map
	statusMap := make(map[string]*aiv1alpha1.ChainStepStatus)
	for i := range chain.Status.StepStatuses {
		statusMap[chain.Status.StepStatuses[i].Name] = &chain.Status.StepStatuses[i]
	}

	// Build spec step map
	specMap := make(map[string]*aiv1alpha1.ChainStep)
	for i := range chain.Spec.Steps {
		specMap[chain.Spec.Steps[i].Name] = &chain.Spec.Steps[i]
	}

	// Check for completed running steps (poll NATS results)
	for i := range chain.Status.StepStatuses {
		ss := &chain.Status.StepStatuses[i]
		if ss.Phase == aiv1alpha1.ChainStepPhaseRunning {
			// Skip polling if no taskID — step was set to Running by a previous
			// operator version or before the status was persisted. Requeue will
			// handle it once the taskID is saved.
			if ss.TaskID == "" {
				log.Info("Step running but no taskID — skipping poll, will requeue", "step", ss.Name)
				continue
			}
			// Check per-step timeout
			spec := specMap[ss.Name]
			if ss.StartedAt != nil && spec != nil {
				elapsed := time.Since(ss.StartedAt.Time)
				if elapsed > time.Duration(spec.Timeout)*time.Second {
					log.Info("Step timed out", "step", ss.Name)
					ss.Phase = aiv1alpha1.ChainStepPhaseFailed
					ss.Error = fmt.Sprintf("step timed out after %ds", spec.Timeout)
					now := metav1.Now()
					ss.CompletedAt = &now
					continue
				}
			}

			// Try to get result from NATS
			result, err := r.pollResult(ctx, nc, chain.Name, ss.Name, ss.TaskID)
			if err != nil {
				log.Error(err, "Failed to poll result", "step", ss.Name)
				continue
			}
			if result != nil {
				now := metav1.Now()
				ss.CompletedAt = &now
				resultErr := result.GetError()
				resultOutput := result.GetOutput()
				if resultErr == "" && isEmptyStepOutput(resultOutput) {
					resultErr = "knight returned empty output"
					r.Recorder.Eventf(chain, corev1.EventTypeWarning, "StepEmptyOutput",
						"Step %s returned empty output, treating as failure", ss.Name)
				}
				if resultErr != "" {
					ss.Phase = aiv1alpha1.ChainStepPhaseFailed
					ss.Error = resultErr
					// Check retry (per-step policy overrides chain-level)
					retryPolicy := chain.Spec.RetryPolicy
					if spec != nil && spec.Retry != nil {
						retryPolicy = &aiv1alpha1.ChainRetryPolicy{
							MaxRetries:     spec.Retry.MaxAttempts,
							BackoffSeconds: spec.Retry.BackoffSeconds,
						}
					}
					if retryPolicy != nil && ss.Retries < retryPolicy.MaxRetries {
						ss.Retries++
						ss.Phase = aiv1alpha1.ChainStepPhasePending
						ss.CompletedAt = nil
						ss.Error = ""
						log.Info("Retrying step", "step", ss.Name, "retry", ss.Retries, "maxRetries", retryPolicy.MaxRetries)
					}
				} else {
					ss.Phase = aiv1alpha1.ChainStepPhaseSucceeded
					ss.Output = resultOutput

					r.Recorder.Eventf(chain, corev1.EventTypeNormal, "StepCompleted", "Step %s completed", ss.Name)

					// Store full output to NATS KV (best-effort)
					if spec := specMap[ss.Name]; spec != nil {
						r.storeStepOutputToKV(ctx, chain.Name, chain.Status.RunID, ss.Name, resultOutput, resultErr, spec.KnightRef, ss.StartedAt, &now)
					}

					// Truncate CRD status output to avoid etcd bloat (4000 chars allows
					// meaningful summaries for template resolution while staying well
					// under etcd's 1.5MB object limit — 10 steps × 4KB = 40KB max)
					if len(ss.Output) > 4000 {
						ss.Output = ss.Output[:4000] + "\n\n... [truncated — full output in NATS KV bucket 'chain-outputs', key '" + chain.Name + "." + ss.Name + "']"
					}

					// Best-effort artifact write if outputPath is set
					if spec != nil && spec.OutputPath != "" {
						outputPath, err := r.renderOutputPath(chain, spec)
						if err != nil {
							log.Error(err, "Failed to render outputPath", "step", ss.Name)
						} else {
							if err := r.writeArtifact(ctx, nc, chain, spec.Name, outputPath, resultOutput); err != nil {
								log.Error(err, "Failed to dispatch artifact write", "step", ss.Name, "path", outputPath)
							} else {
								log.Info("Dispatched artifact write", "step", ss.Name, "path", outputPath)
							}
						}
					}
				}
			}
		}
	}

	// Find ready steps and publish
	for i := range chain.Spec.Steps {
		step := &chain.Spec.Steps[i]
		ss := statusMap[step.Name]
		if ss.Phase != aiv1alpha1.ChainStepPhasePending {
			continue
		}

		// Check if retry backoff applies (per-step policy overrides chain-level)
		if ss.Retries > 0 && ss.CompletedAt != nil {
			retryPolicy := chain.Spec.RetryPolicy
			spec := specMap[step.Name]
			if spec != nil && spec.Retry != nil {
				retryPolicy = &aiv1alpha1.ChainRetryPolicy{
					BackoffSeconds: spec.Retry.BackoffSeconds,
				}
			}
			if retryPolicy != nil {
				backoff := time.Duration(retryPolicy.BackoffSeconds) * time.Second
				if time.Since(ss.CompletedAt.Time) < backoff {
					continue
				}
			}
		}

		// Check dependencies
		ready := true
		for _, dep := range step.DependsOn {
			depStatus := statusMap[dep]
			depSpec := specMap[dep]
			if depStatus == nil {
				ready = false
				break
			}
			switch depStatus.Phase {
			case aiv1alpha1.ChainStepPhaseSucceeded:
				// OK
			case aiv1alpha1.ChainStepPhaseFailed:
				if depSpec != nil && depSpec.ContinueOnFailure {
					// OK
				} else {
					ready = false
				}
			default:
				ready = false
			}
		}
		if !ready {
			continue
		}

		// Render task template
		taskStr, err := r.renderTemplate(chain, step.Task)
		if err != nil {
			log.Error(err, "Failed to render template", "step", step.Name)
			ss.Phase = aiv1alpha1.ChainStepPhaseFailed
			ss.Error = fmt.Sprintf("template render error: %v", err)
			now := metav1.Now()
			ss.CompletedAt = &now
			continue
		}

		// Get knight domain
		knight := &aiv1alpha1.Knight{}
		if err := r.Get(ctx, types.NamespacedName{Name: step.KnightRef, Namespace: chain.Namespace}, knight); err != nil {
			log.Error(err, "Failed to get knight", "knightRef", step.KnightRef)
			continue
		}

		// The run ID shares the final subject token with the timestamp (joined
		// by "-") so the result subject keeps the same token count and the
		// wildcard fallback in pollResult still matches.
		taskID := fmt.Sprintf("chain-%s-%s.%s-%d", chain.Name, step.Name, chain.Status.RunID, time.Now().UnixMilli())

		payload := natspkg.TaskPayload{
			TaskID:    taskID,
			ChainName: chain.Name,
			StepName:  step.Name,
			RunID:     chain.Status.RunID,
			Task:      taskStr,
		}

		if err := r.publishTask(ctx, nc, knight.Spec.Domain, step.KnightRef, payload); err != nil {
			log.Error(err, "Failed to publish task", "step", step.Name)
			continue
		}

		now := metav1.Now()
		ss.Phase = aiv1alpha1.ChainStepPhaseRunning
		ss.StartedAt = &now
		ss.TaskID = taskID
		log.Info("Published step task", "step", step.Name, "taskId", taskID, "knight", step.KnightRef)
	}

	// Check if all steps are terminal
	allTerminal := true
	anyFailed := false
	for _, ss := range chain.Status.StepStatuses {
		switch ss.Phase {
		case aiv1alpha1.ChainStepPhaseSucceeded, aiv1alpha1.ChainStepPhaseFailed, aiv1alpha1.ChainStepPhaseSkipped:
			if ss.Phase == aiv1alpha1.ChainStepPhaseFailed {
				// Check if continueOnFailure — if not, the chain fails
				spec := specMap[ss.Name]
				if spec == nil || !spec.ContinueOnFailure {
					anyFailed = true
				}
			}
		default:
			allTerminal = false
		}
	}

	// If a step failed without continueOnFailure, skip remaining pending steps and fail chain
	if anyFailed {
		for i := range chain.Status.StepStatuses {
			if chain.Status.StepStatuses[i].Phase == aiv1alpha1.ChainStepPhasePending {
				chain.Status.StepStatuses[i].Phase = aiv1alpha1.ChainStepPhaseSkipped
			}
		}
		// Check if still have running steps
		stillRunning := false
		for _, ss := range chain.Status.StepStatuses {
			if ss.Phase == aiv1alpha1.ChainStepPhaseRunning {
				stillRunning = true
				break
			}
		}
		if !stillRunning {
			allTerminal = true
		} else {
			allTerminal = false
		}
	}

	if allTerminal {
		now := metav1.Now()
		chain.Status.CompletedAt = &now

		// Count hard failures, soft failures, and successes
		hardFailures := 0
		softFailures := 0
		succeededSteps := 0
		totalSteps := len(chain.Status.StepStatuses)

		for _, ss := range chain.Status.StepStatuses {
			if ss.Phase == aiv1alpha1.ChainStepPhaseSucceeded {
				succeededSteps++
			} else if ss.Phase == aiv1alpha1.ChainStepPhaseFailed {
				spec := specMap[ss.Name]
				if spec != nil && spec.ContinueOnFailure {
					softFailures++
				} else {
					hardFailures++
				}
			}
		}

		if hardFailures > 0 {
			// At least one hard failure — chain fails
			chain.Status.Phase = aiv1alpha1.ChainPhaseFailed
			chain.Status.RunsFailed++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionChainComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonChainFailed,
				Message:            fmt.Sprintf("%d step(s) failed without continueOnFailure", hardFailures),
				ObservedGeneration: chain.Generation,
			})
			r.Recorder.Eventf(chain, corev1.EventTypeWarning, "Failed", "Chain failed: %d step(s) failed without continueOnFailure", hardFailures)
		} else if softFailures > 0 {
			// No hard failures, but some soft failures
			chain.Status.Phase = aiv1alpha1.ChainPhasePartiallySucceeded
			chain.Status.RunsCompleted++ // Count as completed (not failed)
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionChainComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonChainPartiallySucceeded,
				Message:            fmt.Sprintf("%d/%d steps succeeded (%d failed with continueOnFailure)", succeededSteps, totalSteps, softFailures),
				ObservedGeneration: chain.Generation,
			})
			r.Recorder.Eventf(chain, corev1.EventTypeNormal, "Succeeded", "Chain partially succeeded: %d/%d steps succeeded", succeededSteps, totalSteps)
		} else {
			// All steps succeeded
			chain.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
			chain.Status.RunsCompleted++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionChainComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonChainSucceeded,
				Message:            fmt.Sprintf("All %d steps completed successfully", totalSteps),
				ObservedGeneration: chain.Generation,
			})
			r.Recorder.Event(chain, corev1.EventTypeNormal, "Succeeded", "Chain completed successfully")
		}

		// A run that never published a single task (every terminal step was
		// restored from cache or skipped) did no real work. That usually means
		// stale KV entries are masking a problem — make it visible.
		executedSteps := 0
		for _, ss := range chain.Status.StepStatuses {
			if ss.TaskID != "" {
				executedSteps++
			}
		}
		if executedSteps == 0 && totalSteps > 0 {
			log.Info("Chain run completed without executing any steps")
			r.Recorder.Event(chain, corev1.EventTypeWarning, "NoStepsExecuted",
				"Chain run completed without executing any steps (all outputs restored from cache)")
			metrics.ChainNoOpRunsTotal.WithLabelValues(chain.Name).Inc()
		}

		chain.Status.ObservedGeneration = chain.Generation
		return r.updateStatus(ctx, chain, 0)
	}

	chain.Status.ObservedGeneration = chain.Generation

	// Requeue to poll for results
	return r.updateStatus(ctx, chain, RequeueDefault)
}

// renderTemplate renders Go templates in the task string with step outputs and input.
func (r *ChainReconciler) renderTemplate(chain *aiv1alpha1.Chain, taskStr string) (string, error) {
	if !strings.Contains(taskStr, "{{") {
		return taskStr, nil
	}

	// Build template data
	steps := make(map[string]map[string]string)
	for _, ss := range chain.Status.StepStatuses {
		steps[ss.Name] = map[string]string{
			"Output": ss.Output,
			"Error":  ss.Error,
		}
	}

	data := map[string]interface{}{
		"Steps": steps,
		"Input": chain.Spec.Input,
	}

	tmpl, err := template.New("task").Parse(taskStr)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("template execute error: %w", err)
	}

	return buf.String(), nil
}

// resolveNATSConfig looks up the chain's RoundTable and returns the NATS configuration.
// Falls back to defaultNATSConfig if no roundTableRef is specified.
func (r *ChainReconciler) resolveNATSConfig(ctx context.Context, chain *aiv1alpha1.Chain) (natsConfig, error) {
	if chain.Spec.RoundTableRef == "" {
		return natsConfig{}, fmt.Errorf("chain %s/%s has no roundTableRef configured", chain.Namespace, chain.Name)
	}

	rt := &aiv1alpha1.RoundTable{}
	if err := r.Get(ctx, types.NamespacedName{Name: chain.Spec.RoundTableRef, Namespace: chain.Namespace}, rt); err != nil {
		return natsConfig{}, fmt.Errorf("RoundTable %q not found: %w", chain.Spec.RoundTableRef, err)
	}

	return natsConfig{
		SubjectPrefix: rt.Spec.NATS.SubjectPrefix,
		TasksStream:   rt.Spec.NATS.TasksStream,
		ResultsStream: rt.Spec.NATS.ResultsStream,
	}, nil
}

// publishTask publishes a task to NATS JetStream.
func (r *ChainReconciler) publishTask(ctx context.Context, nc natsConfig, domain, knightName string, payload natspkg.TaskPayload) error {
	client, err := r.natsClient()
	if err != nil {
		return err
	}

	subject := natspkg.TaskSubject(nc.SubjectPrefix, domain, knightName)
	return client.PublishJSON(subject, payload)
}

// pollResult checks for a result message for a given chain step.
// If taskID is provided, polls for that exact result subject; otherwise falls back to wildcard.
func (r *ChainReconciler) pollResult(ctx context.Context, nc natsConfig, chainName, stepName, taskID string) (*natspkg.TaskResult, error) {
	log := logf.FromContext(ctx)

	client, err := r.natsClient()
	if err != nil {
		return nil, err
	}

	// Use exact taskID subject when available (prevents stale result replay)
	var subject string
	if taskID != "" {
		subject = natspkg.ResultSubject(nc.SubjectPrefix, taskID)
	} else {
		taskPrefix := fmt.Sprintf("chain-%s-%s", chainName, stepName)
		subject = natspkg.ResultSubjectWildcard(nc.SubjectPrefix, taskPrefix)
	}

	// Use ephemeral consumer with explicit ack (compatible with both Limits and WorkQueue retention)
	consumerName := natspkg.ChainConsumerName(chainName, stepName)

	msg, err := client.PollMessage(subject, 2*time.Second,
		natspkg.WithDurable(consumerName),
		natspkg.WithAckExplicit(),
		natspkg.WithBindStream(nc.ResultsStream),
		natspkg.WithDeliverAll(),
		natspkg.WithFallbackAutoDetect(),
	)

	// Clean up ephemeral consumer
	defer func() {
		_ = client.DeleteConsumer(nc.ResultsStream, consumerName)
	}()

	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, nil // Timeout, no result yet
	}

	// Ack the message (required for WorkQueue retention)
	if err := msg.Ack(); err != nil {
		log.Error(err, "Failed to ack result message")
	}

	log.Info("Received result message", "subject", msg.Subject, "dataLen", len(msg.Data))

	var result natspkg.TaskResult
	if err := json.Unmarshal(msg.Data, &result); err != nil {
		log.Error(err, "Failed to unmarshal result", "subject", msg.Subject, "rawPreview", string(msg.Data[:min(200, len(msg.Data))]))
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	log.Info("Parsed result", "taskId", result.GetTaskID(), "outputLen", len(result.GetOutput()), "error", result.GetError())

	return &result, nil
}

// reconcileSchedule manages the cron schedule for the chain. It returns true
// if a scheduled fire was missed (e.g. the operator was down) and a catch-up
// run should be triggered.
func (r *ChainReconciler) reconcileSchedule(ctx context.Context, chain *aiv1alpha1.Chain) bool {
	key := chain.Namespace + "/" + chain.Name

	if chain.Spec.Schedule == "" || chain.Spec.Suspended {
		r.removeCronEntry(types.NamespacedName{Namespace: chain.Namespace, Name: chain.Name})
		return false
	}

	r.mu.Lock()

	if r.cron == nil {
		r.cron = cron.New()
		r.cronEntries = make(map[string]cron.EntryID)
		r.cron.Start()
	}

	if _, ok := r.cronEntries[key]; !ok {
		nn := types.NamespacedName{Namespace: chain.Namespace, Name: chain.Name}
		entryID, err := r.cron.AddFunc(chain.Spec.Schedule, func() {
			r.triggerChain(context.Background(), nn)
		})
		if err != nil {
			r.mu.Unlock()
			logf.FromContext(ctx).Error(err, "Failed to add cron schedule", "schedule", chain.Spec.Schedule)
			return false
		}
		r.cronEntries[key] = entryID
	}
	r.mu.Unlock()

	return r.missedSchedule(chain)
}

// missedSchedule reports whether the chain's next fire after lastScheduledAt
// has already passed without a run starting, within the optional
// startingDeadlineSeconds window.
func (r *ChainReconciler) missedSchedule(chain *aiv1alpha1.Chain) bool {
	if chain.Status.LastScheduledAt == nil || chain.Status.Phase == aiv1alpha1.ChainPhaseRunning {
		return false
	}

	sched, err := cron.ParseStandard(chain.Spec.Schedule)
	if err != nil {
		return false
	}

	expected := sched.Next(chain.Status.LastScheduledAt.Time)
	now := time.Now()
	if !expected.Before(now) {
		return false
	}
	if dl := chain.Spec.StartingDeadlineSeconds; dl != nil && now.Sub(expected) > time.Duration(*dl)*time.Second {
		return false
	}
	return true
}

// triggerChain starts a new chain run: it resets step statuses, assigns a
// fresh run ID, and sets the phase to Running. Called from cron goroutines
// (with context.Background()) and from reconcile for missed-schedule catch-up.
func (r *ChainReconciler) triggerChain(ctx context.Context, nn types.NamespacedName) {
	log := logf.Log.WithName("chain-cron")

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		chain := &aiv1alpha1.Chain{}
		if err := r.Get(ctx, nn, chain); err != nil {
			return err
		}

		if chain.Spec.Suspended {
			return nil
		}

		// Guard against overlapping runs: resetting step statuses while a
		// previous run is still in flight orphans its in-progress steps and
		// lets stale outputs masquerade as results for the new run.
		if chain.Status.Phase == aiv1alpha1.ChainPhaseRunning {
			log.Info("Skipping cron trigger, previous run still in progress", "chain", nn.String())
			r.Recorder.Event(chain, corev1.EventTypeWarning, "CronTriggerSkipped",
				"Skipped scheduled trigger: previous run still in progress")
			return nil
		}

		r.initStepStatuses(chain)
		// A new run gets its own completion notification.
		meta.RemoveStatusCondition(&chain.Status.Conditions, aiv1alpha1.ConditionNotificationSent)
		now := metav1.Now()
		chain.Status.RunID = string(uuid.NewUUID())
		chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
		chain.Status.StartedAt = &now
		chain.Status.CompletedAt = nil
		chain.Status.LastScheduledAt = &now

		if err := r.Status().Update(ctx, chain); err != nil {
			return err
		}
		r.Recorder.Event(chain, corev1.EventTypeNormal, "CronTriggered", "Chain triggered by cron schedule")
		return nil
	})
	if err != nil {
		log.Error(err, "Failed to trigger chain", "chain", nn.String())
	}
}

// removeCronEntry removes a cron entry for a chain.
func (r *ChainReconciler) removeCronEntry(nn types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := nn.Namespace + "/" + nn.Name
	if r.cronEntries == nil {
		return
	}
	if id, ok := r.cronEntries[key]; ok {
		if r.cron != nil {
			r.cron.Remove(id)
		}
		delete(r.cronEntries, key)
	}
}

// renderOutputPath renders template variables in the outputPath.
func (r *ChainReconciler) renderOutputPath(chain *aiv1alpha1.Chain, step *aiv1alpha1.ChainStep) (string, error) {
	path := step.OutputPath
	if !strings.Contains(path, "{{") {
		return path, nil
	}

	data := map[string]string{
		"Date":  time.Now().UTC().Format("2006-01-02"),
		"Chain": chain.Name,
		"Step":  step.Name,
	}

	tmpl, err := template.New("outputPath").Parse(path)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// writeArtifact dispatches a write task to the outputKnight.
func (r *ChainReconciler) writeArtifact(ctx context.Context, nc natsConfig, chain *aiv1alpha1.Chain, stepName, outputPath, content string) error {
	client, err := r.natsClient()
	if err != nil {
		return err
	}

	knightName := chain.Spec.OutputKnight
	if knightName == "" {
		knightName = "gawain"
	}

	// Get knight to find its domain
	knight := &aiv1alpha1.Knight{}
	if err := r.Get(ctx, types.NamespacedName{Name: knightName, Namespace: chain.Namespace}, knight); err != nil {
		return fmt.Errorf("output knight %q not found: %w", knightName, err)
	}

	taskID := fmt.Sprintf("chain-%s-%s-artifact.%s-%d", chain.Name, stepName, chain.Status.RunID, time.Now().UnixMilli())

	// The task instructs the knight to write the content to the path
	task := fmt.Sprintf("Write the following content to the file at path '%s'. Create any missing directories. Write ONLY the content below, do not modify or summarize it.\n\n---\n%s", outputPath, content)

	payload := natspkg.TaskPayload{
		TaskID:    taskID,
		ChainName: chain.Name,
		StepName:  stepName + "-artifact",
		RunID:     chain.Status.RunID,
		Task:      task,
	}

	subject := natspkg.TaskSubject(nc.SubjectPrefix, knight.Spec.Domain, knightName)
	return client.PublishJSON(subject, payload)
}

// emptyOutputSentinels are placeholder strings produced by knights when an
// agent session yields no real content. They carry no usable output and must
// not be treated as a successful step result.
var emptyOutputSentinels = []string{
	"[No output from agent]",
}

// isEmptyStepOutput reports whether a step output contains no usable content.
func isEmptyStepOutput(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return true
	}
	for _, sentinel := range emptyOutputSentinels {
		if trimmed == sentinel {
			return true
		}
	}
	return false
}

// storeStepOutputToKV stores the full step output to the NATS KV "chain-outputs" bucket.
// This is best-effort — failures are logged but do not block chain execution.
func (r *ChainReconciler) storeStepOutputToKV(ctx context.Context, chainName, runID, stepName, output, errStr, knight string, startedAt, completedAt *metav1.Time) {
	log := logf.FromContext(ctx)

	// Never persist empty outputs — a poisoned KV entry would be restored as a
	// "successful" step on later runs, silently skipping real work.
	if errStr == "" && isEmptyStepOutput(output) {
		log.Info("Refusing to store empty step output to KV", "step", stepName)
		return
	}

	client, err := r.natsClient()
	if err != nil {
		log.Error(err, "Failed to connect NATS for KV store", "step", stepName)
		return
	}

	var durationStr string
	if startedAt != nil && completedAt != nil {
		durationStr = completedAt.Time.Sub(startedAt.Time).String()
	}

	kvValue := map[string]interface{}{
		"output":   output,
		"error":    errStr,
		"knight":   knight,
		"runId":    runID,
		"duration": durationStr,
		"storedAt": time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(kvValue)
	if err != nil {
		log.Error(err, "Failed to marshal KV value", "step", stepName)
		return
	}

	key := chainName + "." + stepName
	if err := client.KVPut("chain-outputs", key, data); err != nil {
		log.Error(err, "Failed to store step output to KV", "key", key)
	} else {
		log.Info("Stored step output to NATS KV", "bucket", "chain-outputs", "key", key, "size", len(data))
	}
}

// restoreStepOutputsFromKV attempts to restore step outputs from NATS KV.
// Returns the number of steps successfully restored.
func (r *ChainReconciler) restoreStepOutputsFromKV(ctx context.Context, chain *aiv1alpha1.Chain) int {
	log := logf.FromContext(ctx)

	client, err := r.natsClient()
	if err != nil {
		log.Error(err, "Failed to connect NATS for KV restore")
		return 0
	}

	restored := 0
	for i := range chain.Status.StepStatuses {
		ss := &chain.Status.StepStatuses[i]
		if ss.Phase != aiv1alpha1.ChainStepPhasePending {
			continue // Only restore pending steps
		}

		key := chain.Name + "." + ss.Name
		data, err := client.KVGet("chain-outputs", key)
		if err != nil {
			log.V(1).Info("No stored output found for step", "step", ss.Name, "key", key)
			continue
		}

		var kvValue map[string]interface{}
		if err := json.Unmarshal(data, &kvValue); err != nil {
			log.Error(err, "Failed to unmarshal KV value", "step", ss.Name)
			continue
		}

		output, _ := kvValue["output"].(string)
		errStr, _ := kvValue["error"].(string)

		// A stored entry with no usable output is poison left by a run that
		// produced nothing — leave the step Pending so it re-executes, and
		// delete the entry so it cannot mask future runs.
		if errStr == "" && isEmptyStepOutput(output) {
			log.Info("Skipping restore of empty stored output, deleting poisoned KV entry", "step", ss.Name, "key", key)
			if err := client.KVDelete("chain-outputs", key); err != nil {
				log.Error(err, "Failed to delete poisoned KV entry", "key", key)
			}
			continue
		}

		// Only outputs produced by the current run may be restored — entries
		// from earlier runs (or pre-run-identity entries with no runId) would
		// silently stand in for work this run never did.
		storedRunID, _ := kvValue["runId"].(string)
		if storedRunID == "" || storedRunID != chain.Status.RunID {
			log.V(1).Info("Skipping stored output from a different run", "step", ss.Name, "storedRunId", storedRunID)
			continue
		}

		if errStr != "" {
			ss.Phase = aiv1alpha1.ChainStepPhaseFailed
			ss.Error = errStr
			log.Info("Restored failed step from KV", "step", ss.Name)
		} else {
			ss.Phase = aiv1alpha1.ChainStepPhaseSucceeded
			ss.Output = output
			if len(ss.Output) > 4000 {
				ss.Output = ss.Output[:4000] + "\n\n... [truncated — full output in NATS KV bucket 'chain-outputs', key '" + chain.Name + "." + ss.Name + "']"
			}
			log.Info("Restored successful step from KV", "step", ss.Name, "outputLen", len(output))
		}

		// Set completion time (approximate)
		now := metav1.Now()
		ss.CompletedAt = &now
		restored++
	}

	return restored
}

// SetupWithManager sets up the controller with the Manager.
func (r *ChainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize cron scheduler
	r.cron = cron.New()
	r.cron.Start()
	r.cronEntries = make(map[string]cron.EntryID)

	// Stop the cron scheduler on manager shutdown, waiting for any in-flight
	// trigger to finish — otherwise the cron goroutine outlives the manager.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		stopCtx := r.cron.Stop()
		<-stopCtx.Done()
		return nil
	})); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Chain{}).
		Named("chain").
		Complete(r)
}
