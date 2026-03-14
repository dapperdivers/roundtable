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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

const (
	chainFinalizer = "ai.roundtable.io/chain-finalizer"
)

// natsConfig holds resolved NATS configuration for a chain's target RoundTable.
type natsConfig struct {
	SubjectPrefix string // e.g. "fleet-a" or "chelonian"
	TasksStream   string // e.g. "fleet_a_tasks" or "chelonian_tasks"
	ResultsStream string // e.g. "fleet_a_results" or "chelonian_results"
}

// defaultNATSConfig is the fallback when no RoundTable is specified.
// This is only used for legacy chains without a roundTableRef or missionRef.
// New chains should always resolve config from their parent RoundTable CR.
var defaultNATSConfig = natsConfig{
	SubjectPrefix: "fleet-a",
	TasksStream:   "fleet_a_tasks",
	ResultsStream: "fleet_a_results",
}

// ChainReconciler reconciles a Chain object.
type ChainReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	NATS *natspkg.Provider
	cron *cron.Cron
	mu   sync.Mutex
	// cronEntries maps chain namespace/name to cron entry ID
	cronEntries map[string]cron.EntryID
}

// natsClient returns the shared NATS client, or an error if the provider is not configured.
func (r *ChainReconciler) natsClient() (natspkg.Client, error) {
	if r.NATS == nil {
		return nil, fmt.Errorf("NATS provider not configured")
	}
	return r.natsClient()
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=roundtables,verbs=get;list;watch

func (r *ChainReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	chain := &aiv1alpha1.Chain{}
	if err := r.Get(ctx, req.NamespacedName, chain); err != nil {
		if client.IgnoreNotFound(err) == nil {
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

	// Validate knight refs
	if err := r.validateKnightRefs(ctx, chain); err != nil {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               "Valid",
			Status:             metav1.ConditionFalse,
			Reason:             "InvalidKnightRef",
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		_ = r.Status().Update(ctx, chain)
		return ctrl.Result{}, err
	}

	// Validate DAG
	if err := r.validateDAG(chain); err != nil {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               "Valid",
			Status:             metav1.ConditionFalse,
			Reason:             "CyclicDependency",
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		_ = r.Status().Update(ctx, chain)
		return ctrl.Result{}, err
	}

	// Validate templates parse correctly
	if err := r.validateTemplates(chain); err != nil {
		meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
			Type:               "Valid",
			Status:             metav1.ConditionFalse,
			Reason:             "InvalidTemplate",
			Message:            err.Error(),
			ObservedGeneration: chain.Generation,
		})
		chain.Status.ObservedGeneration = chain.Generation
		_ = r.Status().Update(ctx, chain)
		return ctrl.Result{}, err
	}

	meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
		Type:               "Valid",
		Status:             metav1.ConditionTrue,
		Reason:             "Valid",
		Message:            "Chain spec is valid",
		ObservedGeneration: chain.Generation,
	})

	// Handle schedule
	r.reconcileSchedule(ctx, chain)

	// Handle suspended
	if chain.Spec.Suspended {
		chain.Status.Phase = aiv1alpha1.ChainPhaseSuspended
		chain.Status.ObservedGeneration = chain.Generation
		return ctrl.Result{}, r.Status().Update(ctx, chain)
	}

	// Initialize status if empty
	if chain.Status.Phase == "" {
		chain.Status.Phase = aiv1alpha1.ChainPhaseIdle
		r.initStepStatuses(chain)
		chain.Status.ObservedGeneration = chain.Generation
		return ctrl.Result{}, r.Status().Update(ctx, chain)
	}

	// Reset to Idle when spec changes (generation drift) and chain is not running
	if chain.Status.ObservedGeneration != chain.Generation &&
		chain.Status.Phase != aiv1alpha1.ChainPhaseRunning {
		log := logf.FromContext(ctx)
		log.Info("Spec changed, resetting chain to Idle",
			"oldGen", chain.Status.ObservedGeneration,
			"newGen", chain.Generation)
		chain.Status.Phase = aiv1alpha1.ChainPhaseIdle
		r.initStepStatuses(chain)
		
		// Attempt to restore completed steps from NATS KV (resume capability)
		restored := r.restoreStepOutputsFromKV(ctx, chain)
		if restored > 0 {
			log.Info("Restored step outputs from NATS KV after spec change", "restored", restored)
		}
		
		chain.Status.ObservedGeneration = chain.Generation
		return ctrl.Result{}, r.Status().Update(ctx, chain)
	}

	switch chain.Status.Phase {
	case aiv1alpha1.ChainPhaseIdle:
		// Nothing to do unless triggered (manual trigger sets phase to Running externally)
		return ctrl.Result{}, nil

	case aiv1alpha1.ChainPhaseRunning:
		return r.reconcileRunning(ctx, chain)

	case aiv1alpha1.ChainPhaseSucceeded, aiv1alpha1.ChainPhaseFailed, aiv1alpha1.ChainPhasePartiallySucceeded:
		// Terminal — no requeue
		return ctrl.Result{}, nil

	case aiv1alpha1.ChainPhaseSuspended:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
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
		
		// Attempt to restore completed steps from NATS KV (resume capability)
		restored := r.restoreStepOutputsFromKV(ctx, chain)
		if restored > 0 {
			log.Info("Restored step outputs from NATS KV", "restored", restored)
		}
		
		now := metav1.Now()
		chain.Status.StartedAt = &now
		chain.Status.ObservedGeneration = chain.Generation
		return ctrl.Result{RequeueAfter: 1 * time.Second}, r.Status().Update(ctx, chain)
	}
	if chain.Status.StartedAt == nil {
		now := metav1.Now()
		chain.Status.StartedAt = &now
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
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Timeout",
				Message:            fmt.Sprintf("Chain timed out after %ds", chain.Spec.Timeout),
				ObservedGeneration: chain.Generation,
			})
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

					// Store full output to NATS KV (best-effort)
					if spec := specMap[ss.Name]; spec != nil {
						r.storeStepOutputToKV(ctx, chain.Name, ss.Name, resultOutput, resultErr, spec.KnightRef, ss.StartedAt, &now)
					}

					// Truncate CRD status output to avoid etcd bloat
					if len(ss.Output) > 1000 {
						ss.Output = ss.Output[:1000] + "\n\n... [truncated — full output in NATS KV bucket 'chain-outputs', key '" + chain.Name + "." + ss.Name + "']"
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

		taskID := fmt.Sprintf("chain-%s-%s.%d", chain.Name, step.Name, time.Now().UnixMilli())

		payload := natspkg.TaskPayload{
			TaskID:    taskID,
			ChainName: chain.Name,
			StepName:  step.Name,
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
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Failed",
				Message:            fmt.Sprintf("%d step(s) failed without continueOnFailure", hardFailures),
				ObservedGeneration: chain.Generation,
			})
		} else if softFailures > 0 {
			// No hard failures, but some soft failures
			chain.Status.Phase = aiv1alpha1.ChainPhasePartiallySucceeded
			chain.Status.RunsCompleted++ // Count as completed (not failed)
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "PartiallySucceeded",
				Message:            fmt.Sprintf("%d/%d steps succeeded (%d failed with continueOnFailure)", succeededSteps, totalSteps, softFailures),
				ObservedGeneration: chain.Generation,
			})
		} else {
			// All steps succeeded
			chain.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
			chain.Status.RunsCompleted++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Succeeded",
				Message:            fmt.Sprintf("All %d steps completed successfully", totalSteps),
				ObservedGeneration: chain.Generation,
			})
		}
		chain.Status.ObservedGeneration = chain.Generation
		return ctrl.Result{}, r.Status().Update(ctx, chain)
	}

	chain.Status.ObservedGeneration = chain.Generation
	if err := r.Status().Update(ctx, chain); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to poll for results
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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
		return defaultNATSConfig, nil
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

// reconcileSchedule manages the cron schedule for the chain.
func (r *ChainReconciler) reconcileSchedule(ctx context.Context, chain *aiv1alpha1.Chain) {
	key := chain.Namespace + "/" + chain.Name

	if chain.Spec.Schedule == "" || chain.Spec.Suspended {
		r.removeCronEntry(types.NamespacedName{Namespace: chain.Namespace, Name: chain.Name})
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cron == nil {
		r.cron = cron.New()
		r.cronEntries = make(map[string]cron.EntryID)
		r.cron.Start()
	}

	// If already registered, skip
	if _, ok := r.cronEntries[key]; ok {
		return
	}

	nn := types.NamespacedName{Namespace: chain.Namespace, Name: chain.Name}
	entryID, err := r.cron.AddFunc(chain.Spec.Schedule, func() {
		r.triggerChain(nn)
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to add cron schedule", "schedule", chain.Spec.Schedule)
		return
	}
	r.cronEntries[key] = entryID
}

// triggerChain resets a chain's step statuses and sets it to Running.
func (r *ChainReconciler) triggerChain(nn types.NamespacedName) {
	ctx := context.Background()
	log := logf.Log.WithName("chain-cron")

	chain := &aiv1alpha1.Chain{}
	if err := r.Get(ctx, nn, chain); err != nil {
		log.Error(err, "Failed to get chain for cron trigger")
		return
	}

	if chain.Spec.Suspended {
		return
	}

	// Reset step statuses
	r.initStepStatuses(chain)
	now := metav1.Now()
	chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
	chain.Status.StartedAt = &now
	chain.Status.CompletedAt = nil
	chain.Status.LastScheduledAt = &now

	if err := r.Status().Update(ctx, chain); err != nil {
		log.Error(err, "Failed to update chain status for cron trigger")
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

	taskID := fmt.Sprintf("chain-%s-%s-artifact.%d", chain.Name, stepName, time.Now().UnixMilli())

	// The task instructs the knight to write the content to the path
	task := fmt.Sprintf("Write the following content to the file at path '%s'. Create any missing directories. Write ONLY the content below, do not modify or summarize it.\n\n---\n%s", outputPath, content)

	payload := natspkg.TaskPayload{
		TaskID:    taskID,
		ChainName: chain.Name,
		StepName:  stepName + "-artifact",
		Task:      task,
	}

	subject := natspkg.TaskSubject(nc.SubjectPrefix, knight.Spec.Domain, knightName)
	return client.PublishJSON(subject, payload)
}

// storeStepOutputToKV stores the full step output to the NATS KV "chain-outputs" bucket.
// This is best-effort — failures are logged but do not block chain execution.
func (r *ChainReconciler) storeStepOutputToKV(ctx context.Context, chainName, stepName, output, errStr, knight string, startedAt, completedAt *metav1.Time) {
	log := logf.FromContext(ctx)

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

		if errStr != "" {
			ss.Phase = aiv1alpha1.ChainStepPhaseFailed
			ss.Error = errStr
			log.Info("Restored failed step from KV", "step", ss.Name)
		} else {
			ss.Phase = aiv1alpha1.ChainStepPhaseSucceeded
			ss.Output = output
			if len(ss.Output) > 1000 {
				ss.Output = ss.Output[:1000] + "\n\n... [truncated — full output in NATS KV bucket 'chain-outputs', key '" + chain.Name + "." + ss.Name + "']"
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Chain{}).
		Named("chain").
		Complete(r)
}

