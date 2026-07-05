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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/notify"
)

// notificationPending reports whether the spec.notify webhook still needs a
// delivery attempt. True and False conditions are both final states (sent,
// or gave up / rejected); only absent or Unknown (retrying) proceed.
func notificationPending(spec *aiv1alpha1.NotifySpec, conditions []metav1.Condition) bool {
	if spec == nil || spec.Webhook == nil {
		return false
	}
	cond := meta.FindStatusCondition(conditions, aiv1alpha1.ConditionNotificationSent)
	return cond == nil || cond.Status == metav1.ConditionUnknown
}

// deliverNotification runs a single delivery attempt for obj's webhook sink
// and records the outcome in *conditions. It never returns an error — the
// resource's phase must not be affected by notification failure. The caller
// persists the status and requeues after the returned delay (zero means the
// notification reached a final state).
//
// The retry window is measured from completedAt; within it a failure sets
// the condition to Unknown/DeliveryRetrying, beyond it to
// False/DeliveryFailed. A URL outside the allowlist is rejected immediately
// with False/URLNotAllowed (SSRF guard — see notify.allowedURLPrefixes).
func deliverNotification(
	ctx context.Context,
	c client.Client,
	recorder record.EventRecorder,
	n *notify.Notifier,
	obj client.Object,
	conditions *[]metav1.Condition,
	generation int64,
	completedAt time.Time,
	payload notify.Payload,
) time.Duration {
	log := logf.FromContext(ctx)
	webhook := payloadWebhook(obj)

	setCondition := func(status metav1.ConditionStatus, reason, message string) {
		meta.SetStatusCondition(conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionNotificationSent,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: generation,
		})
	}

	if n == nil || !n.URLAllowed(webhook.URL) {
		setCondition(metav1.ConditionFalse, aiv1alpha1.ReasonNotifyURLNotAllowed,
			fmt.Sprintf("Webhook URL %q does not match the operator's allowed URL prefixes", webhook.URL))
		recorder.Eventf(obj, corev1.EventTypeWarning, "NotificationRejected",
			"Webhook URL %q rejected by allowlist", webhook.URL)
		return 0
	}

	fail := func(err error) time.Duration {
		if n.Expired(completedAt) {
			setCondition(metav1.ConditionFalse, aiv1alpha1.ReasonNotifyDeliveryFailed,
				fmt.Sprintf("Giving up after retry window: %v", err))
			recorder.Eventf(obj, corev1.EventTypeWarning, "NotificationFailed",
				"Completion webhook permanently failed: %v", err)
			return 0
		}
		setCondition(metav1.ConditionUnknown, aiv1alpha1.ReasonNotifyRetrying,
			fmt.Sprintf("Delivery failed, will retry: %v", err))
		recorder.Eventf(obj, corev1.EventTypeWarning, "NotificationRetrying",
			"Completion webhook delivery failed, retrying: %v", err)
		return notify.Backoff(completedAt)
	}

	token, err := webhookToken(ctx, c, obj.GetNamespace(), webhook)
	if err != nil {
		return fail(err)
	}

	if err := n.Deliver(ctx, webhook.URL, token, payload); err != nil {
		return fail(err)
	}

	setCondition(metav1.ConditionTrue, aiv1alpha1.ReasonNotifyDelivered,
		fmt.Sprintf("Completion notification delivered (%s)", payload.IdempotencyKey))
	recorder.Event(obj, corev1.EventTypeNormal, "NotificationSent", "Completion webhook delivered")
	log.Info("Delivered completion notification", "kind", payload.Kind, "phase", payload.Phase,
		"idempotencyKey", payload.IdempotencyKey)
	return 0
}

// payloadWebhook extracts the webhook sink from a Chain or Mission. Callers
// only reach this via notificationPending, so the sink is always present.
func payloadWebhook(obj client.Object) *aiv1alpha1.WebhookSink {
	switch o := obj.(type) {
	case *aiv1alpha1.Chain:
		return o.Spec.Notify.Webhook
	case *aiv1alpha1.Mission:
		return o.Spec.Notify.Webhook
	}
	return nil
}

// webhookToken resolves the bearer token from the sink's Secret reference in
// the resource's own namespace. No reference means unauthenticated delivery.
// The token value is never logged or embedded in Events.
func webhookToken(ctx context.Context, c client.Client, namespace string, webhook *aiv1alpha1.WebhookSink) (string, error) {
	ref := webhook.TokenSecretRef
	if ref == nil {
		return "", nil
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, secret); err != nil {
		return "", fmt.Errorf("read token secret %q: %w", ref.Name, err)
	}
	token, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("token secret %q has no key %q", ref.Name, ref.Key)
	}
	return string(token), nil
}

// chainNotifyPayload builds the roundtable.notify/v1 payload for a terminal
// chain. The inline output is the last succeeded step's (status-truncated)
// output, capped at notify.OutputCap; the full output stays in the
// chain-outputs NATS KV bucket referenced by outputRef.
func chainNotifyPayload(chain *aiv1alpha1.Chain) notify.Payload {
	steps := make([]notify.StepSummary, 0, len(chain.Status.StepStatuses))
	knightByStep := make(map[string]string, len(chain.Spec.Steps))
	for _, s := range chain.Spec.Steps {
		knightByStep[s.Name] = s.KnightRef
	}

	var output, outputStep string
	for _, ss := range chain.Status.StepStatuses {
		steps = append(steps, notify.StepSummary{
			Name:   ss.Name,
			Knight: knightByStep[ss.Name],
			Phase:  string(ss.Phase),
		})
		if ss.Phase == aiv1alpha1.ChainStepPhaseSucceeded && ss.Output != "" {
			output = ss.Output
			outputStep = ss.Name
		}
	}
	output, truncated := notify.Truncate(output)

	var outputRef *notify.OutputRef
	if outputStep != "" {
		outputRef = &notify.OutputRef{NATSKV: &notify.NATSKVRef{
			Bucket: "chain-outputs",
			Key:    chain.Name + "." + outputStep,
		}}
	}

	idempotencyKey := string(chain.UID) + "/" + string(chain.Status.Phase)
	if chain.Status.RunID != "" {
		// Chains re-run (cron / manual trigger); the run ID keeps each run's
		// notification distinct so receiver-side dedupe can't swallow it.
		idempotencyKey = string(chain.UID) + "/" + chain.Status.RunID + "/" + string(chain.Status.Phase)
	}

	return notify.Payload{
		Schema:         notify.SchemaV1,
		Kind:           "Chain",
		Name:           chain.Name,
		Namespace:      chain.Namespace,
		UID:            string(chain.UID),
		Phase:          string(chain.Status.Phase),
		RoundTableRef:  chain.Spec.RoundTableRef,
		StartedAt:      chain.Status.StartedAt,
		FinishedAt:     chain.Status.CompletedAt,
		Steps:          steps,
		Output:         output,
		Truncated:      truncated,
		OutputRef:      outputRef,
		Context:        chain.Spec.Notify.Webhook.Context,
		IdempotencyKey: idempotencyKey,
	}
}

// missionNotifyPayload builds the roundtable.notify/v1 payload for a mission
// whose terminal outcome has been recorded. The phase comes from
// terminalOutcome because the mission's status.phase passes through
// CleaningUp before the terminal phase is restored.
func missionNotifyPayload(mission *aiv1alpha1.Mission) notify.Payload {
	output, truncated := notify.Truncate(mission.Status.Result)

	var outputRef *notify.OutputRef
	if mission.Status.ResultsConfigMap != "" {
		// Legacy field name — it holds a "<bucket>.<key>" NATS KV reference.
		outputRef = &notify.OutputRef{NATSKV: &notify.NATSKVRef{
			Bucket: "mission-results",
			Key:    mission.Name,
		}}
	}

	phase := string(terminalOutcome(mission))
	return notify.Payload{
		Schema:         notify.SchemaV1,
		Kind:           "Mission",
		Name:           mission.Name,
		Namespace:      mission.Namespace,
		UID:            string(mission.UID),
		Phase:          phase,
		RoundTableRef:  mission.Spec.RoundTableRef,
		StartedAt:      mission.Status.StartedAt,
		FinishedAt:     mission.Status.CompletedAt,
		Output:         output,
		Truncated:      truncated,
		OutputRef:      outputRef,
		Context:        mission.Spec.Notify.Webhook.Context,
		IdempotencyKey: string(mission.UID) + "/" + phase,
	}
}

// notifyCompletedAt anchors the retry window: the resource's CompletedAt when
// set, otherwise the completion condition's transition time, otherwise now.
func notifyCompletedAt(completedAt *metav1.Time, conditions []metav1.Condition, completeCondType string) time.Time {
	if completedAt != nil {
		return completedAt.Time
	}
	if cond := meta.FindStatusCondition(conditions, completeCondType); cond != nil {
		return cond.LastTransitionTime.Time
	}
	return time.Now()
}
