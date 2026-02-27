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

	"github.com/nats-io/nats.go"
	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

const (
	chainFinalizer = "ai.roundtable.io/chain-finalizer"
	natsURL        = "nats://nats.database.svc:4222"
)

// TaskPayload is the JSON payload published to NATS for a chain step.
type TaskPayload struct {
	TaskID    string `json:"taskId"`
	ChainName string `json:"chainName"`
	StepName  string `json:"stepName"`
	Task      string `json:"task"`
}

// TaskResult is the JSON payload received from NATS for a completed step.
type TaskResult struct {
	TaskID string `json:"taskId"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ChainReconciler reconciles a Chain object.
type ChainReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	nc   *nats.Conn
	js   nats.JetStreamContext
	mu   sync.Mutex
	cron *cron.Cron
	// cronEntries maps chain namespace/name to cron entry ID
	cronEntries map[string]cron.EntryID
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=chains/finalizers,verbs=update
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch

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
		chain.Finalizers = removeString(chain.Finalizers, chainFinalizer)
		if err := r.Update(ctx, chain); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !containsString(chain.Finalizers, chainFinalizer) {
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

	switch chain.Status.Phase {
	case aiv1alpha1.ChainPhaseIdle:
		// Nothing to do unless triggered (manual trigger sets phase to Running externally)
		return ctrl.Result{}, nil

	case aiv1alpha1.ChainPhaseRunning:
		return r.reconcileRunning(ctx, chain)

	case aiv1alpha1.ChainPhaseSucceeded, aiv1alpha1.ChainPhaseFailed:
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
func (r *ChainReconciler) validateDAG(chain *aiv1alpha1.Chain) error {
	stepNames := make(map[string]bool)
	for _, s := range chain.Spec.Steps {
		stepNames[s.Name] = true
	}

	// Build adjacency: step -> depends on
	deps := make(map[string][]string)
	for _, s := range chain.Spec.Steps {
		for _, d := range s.DependsOn {
			if !stepNames[d] {
				return fmt.Errorf("step %q depends on unknown step %q", s.Name, d)
			}
			deps[s.Name] = append(deps[s.Name], d)
		}
	}

	// Kahn's algorithm
	inDegree := make(map[string]int)
	for _, s := range chain.Spec.Steps {
		inDegree[s.Name] = 0
	}
	for _, s := range chain.Spec.Steps {
		for _, d := range s.DependsOn {
			_ = d
			inDegree[s.Name]++
		}
	}

	queue := []string{}
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++

		// Find steps that depend on this node
		for _, s := range chain.Spec.Steps {
			for _, d := range s.DependsOn {
				if d == node {
					inDegree[s.Name]--
					if inDegree[s.Name] == 0 {
						queue = append(queue, s.Name)
					}
				}
			}
		}
	}

	if visited != len(chain.Spec.Steps) {
		return fmt.Errorf("chain DAG contains a cycle")
	}
	return nil
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
			result, err := r.pollResult(ctx, chain.Name, ss.Name)
			if err != nil {
				log.Error(err, "Failed to poll result", "step", ss.Name)
				continue
			}
			if result != nil {
				now := metav1.Now()
				ss.CompletedAt = &now
				if result.Error != "" {
					ss.Phase = aiv1alpha1.ChainStepPhaseFailed
					ss.Error = result.Error
					// Check retry
					if chain.Spec.RetryPolicy != nil && ss.Retries < chain.Spec.RetryPolicy.MaxRetries {
						ss.Retries++
						ss.Phase = aiv1alpha1.ChainStepPhasePending
						ss.CompletedAt = nil
						ss.Error = ""
						log.Info("Retrying step", "step", ss.Name, "retry", ss.Retries)
					}
				} else {
					ss.Phase = aiv1alpha1.ChainStepPhaseSucceeded
					ss.Output = result.Output
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

		// Check if retry backoff applies
		if ss.Retries > 0 && chain.Spec.RetryPolicy != nil && ss.CompletedAt != nil {
			backoff := time.Duration(chain.Spec.RetryPolicy.BackoffSeconds) * time.Second
			if time.Since(ss.CompletedAt.Time) < backoff {
				continue
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

		taskID := fmt.Sprintf("chain-%s-%s-%d", chain.Name, step.Name, time.Now().UnixMilli())

		payload := TaskPayload{
			TaskID:    taskID,
			ChainName: chain.Name,
			StepName:  step.Name,
			Task:      taskStr,
		}

		if err := r.publishTask(ctx, knight.Spec.Domain, step.KnightRef, payload); err != nil {
			log.Error(err, "Failed to publish task", "step", step.Name)
			continue
		}

		now := metav1.Now()
		ss.Phase = aiv1alpha1.ChainStepPhaseRunning
		ss.StartedAt = &now
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
		if anyFailed {
			chain.Status.Phase = aiv1alpha1.ChainPhaseFailed
			chain.Status.RunsFailed++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Failed",
				Message:            "One or more steps failed",
				ObservedGeneration: chain.Generation,
			})
		} else {
			chain.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
			chain.Status.RunsCompleted++
			meta.SetStatusCondition(&chain.Status.Conditions, metav1.Condition{
				Type:               "Complete",
				Status:             metav1.ConditionTrue,
				Reason:             "Succeeded",
				Message:            "All steps completed successfully",
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

// ensureNATS connects to NATS if not already connected.
func (r *ChainReconciler) ensureNATS() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.nc != nil && r.nc.IsConnected() {
		return nil
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

// publishTask publishes a task to NATS JetStream.
func (r *ChainReconciler) publishTask(ctx context.Context, domain, knightName string, payload TaskPayload) error {
	if err := r.ensureNATS(); err != nil {
		return err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	subject := fmt.Sprintf("fleet-a.tasks.%s.%s", domain, knightName)
	_, err = r.js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("NATS publish to %s failed: %w", subject, err)
	}

	return nil
}

// pollResult checks for a result message for a given chain step.
func (r *ChainReconciler) pollResult(ctx context.Context, chainName, stepName string) (*TaskResult, error) {
	if err := r.ensureNATS(); err != nil {
		return nil, err
	}

	// Subscribe to result subject with a short timeout
	subject := fmt.Sprintf("fleet-a.results.chain-%s-%s.*", chainName, stepName)
	sub, err := r.js.SubscribeSync(subject, nats.OrderedConsumer())
	if err != nil {
		return nil, fmt.Errorf("NATS subscribe %s failed: %w", subject, err)
	}
	defer sub.Unsubscribe()

	msg, err := sub.NextMsg(500 * time.Millisecond)
	if err != nil {
		if err == nats.ErrTimeout {
			return nil, nil // No result yet
		}
		return nil, fmt.Errorf("NATS next msg: %w", err)
	}

	var result TaskResult
	if err := json.Unmarshal(msg.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

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

// SetupWithManager sets up the controller with the Manager.
func (r *ChainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Chain{}).
		Named("chain").
		Complete(r)
}

// Helpers
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}
