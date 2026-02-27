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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("RoundTable Controller", func() {
	const (
		rtName    = "test-roundtable"
		namespace = "default"
	)

	ctx := context.Background()
	rtNamespacedName := types.NamespacedName{Name: rtName, Namespace: namespace}

	newReconciler := func() *RoundTableReconciler {
		return &RoundTableReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	Context("Knight Discovery", func() {
		BeforeEach(func() {
			// Create RoundTable with label selector
			rt := &aiv1alpha1.RoundTable{}
			err := k8sClient.Get(ctx, rtNamespacedName, rt)
			if err != nil && errors.IsNotFound(err) {
				rt = &aiv1alpha1.RoundTable{
					ObjectMeta: metav1.ObjectMeta{
						Name:      rtName,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.RoundTableSpec{
						NATS: aiv1alpha1.RoundTableNATS{
							URL:           "nats://nats.test:4222",
							SubjectPrefix: "test-fleet",
							TasksStream:   "test_fleet_tasks",
							ResultsStream: "test_fleet_results",
						},
						KnightSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"roundtable.io/fleet": "test",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, rt)).To(Succeed())
			}

			// Create matching knights
			for _, name := range []string{"knight-alpha", "knight-beta"} {
				k := &aiv1alpha1.Knight{}
				knn := types.NamespacedName{Name: name, Namespace: namespace}
				err := k8sClient.Get(ctx, knn, k)
				if err != nil && errors.IsNotFound(err) {
					k = &aiv1alpha1.Knight{
						ObjectMeta: metav1.ObjectMeta{
							Name:      name,
							Namespace: namespace,
							Labels: map[string]string{
								"roundtable.io/fleet": "test",
							},
						},
						Spec: aiv1alpha1.KnightSpec{
							Domain: "security",
							Skills: []string{"security"},
							NATS: aiv1alpha1.KnightNATS{
								URL:      "nats://nats.test:4222",
								Subjects: []string{"test-fleet.tasks.security.>"},
								Stream:   "test_fleet_tasks",
							},
						},
					}
					Expect(k8sClient.Create(ctx, k)).To(Succeed())
				}
			}
		})

		AfterEach(func() {
			// Cleanup
			for _, name := range []string{"knight-alpha", "knight-beta"} {
				k := &aiv1alpha1.Knight{}
				knn := types.NamespacedName{Name: name, Namespace: namespace}
				if err := k8sClient.Get(ctx, knn, k); err == nil {
					_ = k8sClient.Delete(ctx, k)
				}
			}
			rt := &aiv1alpha1.RoundTable{}
			if err := k8sClient.Get(ctx, rtNamespacedName, rt); err == nil {
				_ = k8sClient.Delete(ctx, rt)
			}
		})

		It("should discover knights matching the label selector", func() {
			rec := newReconciler()
			result, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: rtNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNamespacedName, rt)).To(Succeed())
			Expect(rt.Status.KnightsTotal).To(Equal(int32(2)))
			Expect(rt.Status.Knights).To(HaveLen(2))
		})

		It("should set Provisioning phase when no knights are discovered", func() {
			// Delete the knights first
			for _, name := range []string{"knight-alpha", "knight-beta"} {
				k := &aiv1alpha1.Knight{}
				knn := types.NamespacedName{Name: name, Namespace: namespace}
				if err := k8sClient.Get(ctx, knn, k); err == nil {
					Expect(k8sClient.Delete(ctx, k)).To(Succeed())
				}
			}

			rec := newReconciler()
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: rtNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNamespacedName, rt)).To(Succeed())
			Expect(rt.Status.Phase).To(Equal(aiv1alpha1.RoundTablePhaseProvisioning))
			Expect(rt.Status.KnightsTotal).To(Equal(int32(0)))
		})
	})

	Context("Health Aggregation", func() {
		BeforeEach(func() {
			rt := &aiv1alpha1.RoundTable{}
			err := k8sClient.Get(ctx, rtNamespacedName, rt)
			if err != nil && errors.IsNotFound(err) {
				rt = &aiv1alpha1.RoundTable{
					ObjectMeta: metav1.ObjectMeta{
						Name:      rtName,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.RoundTableSpec{
						NATS: aiv1alpha1.RoundTableNATS{
							URL:           "nats://nats.test:4222",
							SubjectPrefix: "test-fleet",
							TasksStream:   "test_fleet_tasks",
							ResultsStream: "test_fleet_results",
						},
						KnightSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"roundtable.io/fleet": "health-test",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, rt)).To(Succeed())
			}
		})

		AfterEach(func() {
			for _, name := range []string{"healthy-knight", "unhealthy-knight"} {
				k := &aiv1alpha1.Knight{}
				knn := types.NamespacedName{Name: name, Namespace: namespace}
				if err := k8sClient.Get(ctx, knn, k); err == nil {
					_ = k8sClient.Delete(ctx, k)
				}
			}
			rt := &aiv1alpha1.RoundTable{}
			if err := k8sClient.Get(ctx, rtNamespacedName, rt); err == nil {
				_ = k8sClient.Delete(ctx, rt)
			}
		})

		It("should report Degraded when some knights are not ready", func() {
			// Create two knights, set one as ready and one not
			for _, tc := range []struct {
				name  string
				ready bool
				phase aiv1alpha1.KnightPhase
			}{
				{"healthy-knight", true, aiv1alpha1.KnightPhaseReady},
				{"unhealthy-knight", false, aiv1alpha1.KnightPhaseDegraded},
			} {
				k := &aiv1alpha1.Knight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.name,
						Namespace: namespace,
						Labels: map[string]string{
							"roundtable.io/fleet": "health-test",
						},
					},
					Spec: aiv1alpha1.KnightSpec{
						Domain: "security",
						Skills: []string{"security"},
						NATS: aiv1alpha1.KnightNATS{
							URL:      "nats://nats.test:4222",
							Subjects: []string{"test-fleet.tasks.security.>"},
							Stream:   "test_fleet_tasks",
						},
					},
				}
				Expect(k8sClient.Create(ctx, k)).To(Succeed())

				// Update status
				k.Status.Phase = tc.phase
				k.Status.Ready = tc.ready
				Expect(k8sClient.Status().Update(ctx, k)).To(Succeed())
			}

			rec := newReconciler()
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: rtNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNamespacedName, rt)).To(Succeed())
			Expect(rt.Status.Phase).To(Equal(aiv1alpha1.RoundTablePhaseDegraded))
			Expect(rt.Status.KnightsReady).To(Equal(int32(1)))
			Expect(rt.Status.KnightsTotal).To(Equal(int32(2)))
		})

		It("should report Ready when all knights are ready", func() {
			for _, name := range []string{"healthy-knight", "unhealthy-knight"} {
				k := &aiv1alpha1.Knight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: namespace,
						Labels: map[string]string{
							"roundtable.io/fleet": "health-test",
						},
					},
					Spec: aiv1alpha1.KnightSpec{
						Domain: "security",
						Skills: []string{"security"},
						NATS: aiv1alpha1.KnightNATS{
							URL:      "nats://nats.test:4222",
							Subjects: []string{"test-fleet.tasks.security.>"},
							Stream:   "test_fleet_tasks",
						},
					},
				}
				Expect(k8sClient.Create(ctx, k)).To(Succeed())

				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				Expect(k8sClient.Status().Update(ctx, k)).To(Succeed())
			}

			rec := newReconciler()
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: rtNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNamespacedName, rt)).To(Succeed())
			Expect(rt.Status.Phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			Expect(rt.Status.KnightsReady).To(Equal(int32(2)))
		})
	})

	Context("Suspended State", func() {
		BeforeEach(func() {
			rt := &aiv1alpha1.RoundTable{}
			err := k8sClient.Get(ctx, rtNamespacedName, rt)
			if err != nil && errors.IsNotFound(err) {
				rt = &aiv1alpha1.RoundTable{
					ObjectMeta: metav1.ObjectMeta{
						Name:      rtName,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.RoundTableSpec{
						NATS: aiv1alpha1.RoundTableNATS{
							URL:           "nats://nats.test:4222",
							SubjectPrefix: "test-fleet",
							TasksStream:   "test_fleet_tasks",
							ResultsStream: "test_fleet_results",
						},
						Suspended: true,
					},
				}
				Expect(k8sClient.Create(ctx, rt)).To(Succeed())
			}
		})

		AfterEach(func() {
			rt := &aiv1alpha1.RoundTable{}
			if err := k8sClient.Get(ctx, rtNamespacedName, rt); err == nil {
				_ = k8sClient.Delete(ctx, rt)
			}
		})

		It("should set Suspended phase when spec.suspended is true", func() {
			rec := newReconciler()
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: rtNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNamespacedName, rt)).To(Succeed())
			Expect(rt.Status.Phase).To(Equal(aiv1alpha1.RoundTablePhaseSuspended))
		})
	})

	Context("Phase Computation", func() {
		It("should return OverBudget when cost exceeds budget", func() {
			rec := newReconciler()
			rt := &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{
						CostBudgetUSD: "10.00",
					},
				},
			}
			phase := rec.computePhase(rt, 2, 2, 15.0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseOverBudget))
		})

		It("should return Provisioning when no knights exist", func() {
			rec := newReconciler()
			rt := &aiv1alpha1.RoundTable{}
			phase := rec.computePhase(rt, 0, 0, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseProvisioning))
		})

		It("should return Ready when all knights are ready", func() {
			rec := newReconciler()
			rt := &aiv1alpha1.RoundTable{}
			phase := rec.computePhase(rt, 3, 3, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
		})

		It("should return Degraded when some knights are not ready", func() {
			rec := newReconciler()
			rt := &aiv1alpha1.RoundTable{}
			phase := rec.computePhase(rt, 1, 3, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseDegraded))
		})
	})
})
