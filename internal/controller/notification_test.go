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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/notify"
)

// notifySink is an httptest receiver that counts deliveries and captures the
// last request for assertions.
type notifySink struct {
	server   *httptest.Server
	hits     atomic.Int64
	status   atomic.Int64
	lastAuth atomic.Value
	lastIdem atomic.Value
	lastBody atomic.Value
}

func newNotifySink() *notifySink {
	s := &notifySink{}
	s.status.Store(http.StatusOK)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.lastAuth.Store(r.Header.Get("Authorization"))
		s.lastIdem.Store(r.Header.Get(notify.IdempotencyHeader))
		var body notify.Payload
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			s.lastBody.Store(body)
		}
		w.WriteHeader(int(s.status.Load()))
	}))
	return s
}

var _ = Describe("Completion notifications", func() {
	const (
		namespace      = "default"
		knightName     = "notify-knight"
		roundTableName = "notify-table"
	)

	ctx := context.Background()
	var sink *notifySink

	BeforeEach(func() {
		sink = newNotifySink()
		DeferCleanup(sink.server.Close)
	})

	ensureFixtures := func() {
		rt := &aiv1alpha1.RoundTable{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: roundTableName, Namespace: namespace}, rt); errors.IsNotFound(err) {
			rt = &aiv1alpha1.RoundTable{
				ObjectMeta: metav1.ObjectMeta{Name: roundTableName, Namespace: namespace},
				Spec: aiv1alpha1.RoundTableSpec{
					NATS: aiv1alpha1.RoundTableNATS{
						URL:           "nats://localhost:4222",
						SubjectPrefix: "notify-table",
						TasksStream:   "notify_table_tasks",
						ResultsStream: "notify_table_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rt)).To(Succeed())
		}
		knight := &aiv1alpha1.Knight{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: knightName, Namespace: namespace}, knight); errors.IsNotFound(err) {
			knight = &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{Name: knightName, Namespace: namespace},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "notify",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"notify"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"notify-table.tasks.notify.>"},
						Stream:   "notify_table_tasks",
					},
					Concurrency: 1,
					TaskTimeout: 120,
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())
		}
	}

	newChainReconciler := func(allowed ...string) *ChainReconciler {
		return &ChainReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
			Notify:   notify.NewNotifier(allowed),
		}
	}

	// createTerminalChain creates a chain with a notify sink and drives its
	// status straight to the given terminal phase, the way the controller
	// leaves it after a completed run.
	createTerminalChain := func(name string, phase aiv1alpha1.ChainPhase, sinkSpec *aiv1alpha1.NotifySpec) types.NamespacedName {
		ensureFixtures()
		nn := types.NamespacedName{Name: name, Namespace: namespace}

		chain := &aiv1alpha1.Chain{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aiv1alpha1.ChainSpec{
				RoundTableRef: roundTableName,
				Notify:        sinkSpec,
				Steps: []aiv1alpha1.ChainStep{
					{Name: "do-it", KnightRef: knightName, Task: "do the thing"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, chain)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, chain)
		})

		Expect(k8sClient.Get(ctx, nn, chain)).To(Succeed())
		now := metav1.Now()
		started := metav1.NewTime(now.Add(-time.Minute))
		chain.Status.Phase = phase
		chain.Status.StartedAt = &started
		chain.Status.CompletedAt = &now
		chain.Status.RunID = "run-1"
		chain.Status.ObservedGeneration = chain.Generation
		chain.Status.StepStatuses = []aiv1alpha1.ChainStepStatus{
			{Name: "do-it", Phase: aiv1alpha1.ChainStepPhaseSucceeded, Output: "it is done"},
		}
		Expect(k8sClient.Status().Update(ctx, chain)).To(Succeed())
		return nn
	}

	webhookTo := func(url string) *aiv1alpha1.NotifySpec {
		return &aiv1alpha1.NotifySpec{Webhook: &aiv1alpha1.WebhookSink{
			URL:     url,
			Context: map[string]string{"sessionKey": "discord:channel:42"},
		}}
	}

	getCondition := func(nn types.NamespacedName) *metav1.Condition {
		chain := &aiv1alpha1.Chain{}
		Expect(k8sClient.Get(ctx, nn, chain)).To(Succeed())
		return apimeta.FindStatusCondition(chain.Status.Conditions, aiv1alpha1.ConditionNotificationSent)
	}

	Context("Chain webhooks", func() {
		It("fires exactly once and dedupes replayed reconciles", func() {
			r := newChainReconciler(sink.server.URL)
			nn := createTerminalChain("notify-once", aiv1alpha1.ChainPhaseSucceeded, webhookTo(sink.server.URL))

			for i := 0; i < 3; i++ {
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(sink.hits.Load()).To(Equal(int64(1)))
			cond := getCondition(nn)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(aiv1alpha1.ReasonNotifyDelivered))

			body := sink.lastBody.Load().(notify.Payload)
			Expect(body.Schema).To(Equal(notify.SchemaV1))
			Expect(body.Kind).To(Equal("Chain"))
			Expect(body.Phase).To(Equal("Succeeded"))
			Expect(body.Output).To(Equal("it is done"))
			Expect(body.Context).To(HaveKeyWithValue("sessionKey", "discord:channel:42"))
			Expect(body.IdempotencyKey).To(ContainSubstring("/run-1/Succeeded"))
			Expect(sink.lastIdem.Load()).To(Equal(body.IdempotencyKey))
			Expect(body.Steps).To(HaveLen(1))
			Expect(body.Steps[0].Knight).To(Equal(knightName))
		})

		It("sends the bearer token from tokenSecretRef", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "molt-hook-token", Namespace: namespace},
				Data:       map[string][]byte{"token": []byte("sekrit")},
			}
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, secret))).To(Succeed())

			spec := webhookTo(sink.server.URL)
			spec.Webhook.TokenSecretRef = &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "molt-hook-token"},
				Key:                  "token",
			}
			r := newChainReconciler(sink.server.URL)
			nn := createTerminalChain("notify-token", aiv1alpha1.ChainPhaseSucceeded, spec)

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.hits.Load()).To(Equal(int64(1)))
			Expect(sink.lastAuth.Load()).To(Equal("Bearer sekrit"))
		})

		It("rejects URLs outside the allowlist without POSTing", func() {
			r := newChainReconciler("http://molt.ai.svc:18789/")
			nn := createTerminalChain("notify-ssrf", aiv1alpha1.ChainPhaseFailed, webhookTo(sink.server.URL))

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(sink.hits.Load()).To(BeZero())
			cond := getCondition(nn)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(aiv1alpha1.ReasonNotifyURLNotAllowed))

			// Rejection is final: replays never retry it.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.hits.Load()).To(BeZero())
		})

		It("retries with backoff while the receiver fails, then succeeds", func() {
			sink.status.Store(http.StatusInternalServerError)
			r := newChainReconciler(sink.server.URL)
			nn := createTerminalChain("notify-retry", aiv1alpha1.ChainPhaseSucceeded, webhookTo(sink.server.URL))

			res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
			Expect(sink.hits.Load()).To(Equal(int64(1)))

			cond := getCondition(nn)
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
			Expect(cond.Reason).To(Equal(aiv1alpha1.ReasonNotifyRetrying))

			// Receiver recovers — the requeued reconcile delivers.
			sink.status.Store(http.StatusOK)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.hits.Load()).To(Equal(int64(2)))
			Expect(getCondition(nn).Status).To(Equal(metav1.ConditionTrue))
		})

		It("gives up after the retry window and records the failure", func() {
			sink.status.Store(http.StatusInternalServerError)
			r := newChainReconciler(sink.server.URL)
			nn := createTerminalChain("notify-giveup", aiv1alpha1.ChainPhaseSucceeded, webhookTo(sink.server.URL))

			// Age the completion past the retry window.
			chain := &aiv1alpha1.Chain{}
			Expect(k8sClient.Get(ctx, nn, chain)).To(Succeed())
			old := metav1.NewTime(time.Now().Add(-20 * time.Minute))
			chain.Status.CompletedAt = &old
			Expect(k8sClient.Status().Update(ctx, chain)).To(Succeed())

			res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeZero())

			cond := getCondition(nn)
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(aiv1alpha1.ReasonNotifyDeliveryFailed))

			// Permanent failure never fires again.
			hits := sink.hits.Load()
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.hits.Load()).To(Equal(hits))
		})

		It("does nothing for chains without spec.notify", func() {
			r := newChainReconciler(sink.server.URL)
			nn := createTerminalChain("notify-absent", aiv1alpha1.ChainPhaseSucceeded, nil)

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.hits.Load()).To(BeZero())
			Expect(getCondition(nn)).To(BeNil())
		})
	})

	Context("Mission webhooks", func() {
		newMissionReconciler := func(allowed ...string) *MissionReconciler {
			return &MissionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(100),
				Notify:   notify.NewNotifier(allowed),
			}
		}

		It("fires once after the terminal outcome is recorded", func() {
			nn := types.NamespacedName{Name: "notify-mission", Namespace: namespace}
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: namespace},
				Spec: aiv1alpha1.MissionSpec{
					Objective: "prove notifications work",
					Notify:    webhookTo(sink.server.URL),
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, mission)
			})

			Expect(k8sClient.Get(ctx, nn, mission)).To(Succeed())
			now := metav1.Now()
			mission.Status.Phase = aiv1alpha1.MissionPhaseSucceeded
			mission.Status.StartedAt = &now
			mission.Status.CompletedAt = &now
			mission.Status.Result = "All mission chains completed successfully"
			apimeta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
				Type:               aiv1alpha1.ConditionMissionComplete,
				Status:             metav1.ConditionTrue,
				Reason:             aiv1alpha1.ReasonMissionSucceeded,
				Message:            "All mission chains completed successfully",
				ObservedGeneration: mission.Generation,
			})
			Expect(k8sClient.Status().Update(ctx, mission)).To(Succeed())

			r := newMissionReconciler(sink.server.URL)
			for i := 0; i < 3; i++ {
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(sink.hits.Load()).To(Equal(int64(1)))
			body := sink.lastBody.Load().(notify.Payload)
			Expect(body.Kind).To(Equal("Mission"))
			Expect(body.Phase).To(Equal("Succeeded"))
			Expect(body.Output).To(Equal("All mission chains completed successfully"))

			updated := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
			cond := apimeta.FindStatusCondition(updated.Status.Conditions, aiv1alpha1.ConditionNotificationSent)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
