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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("RoundTable Controller - Warm Pool", func() {
	Context("When reconciling a RoundTable with warm pool configuration", func() {
		const (
			rtName       = "test-rt-warmpool"
			namespace    = "default"
			poolSize     = int32(3)
			templateName = "default-knight"
		)

		ctx := context.Background()
		rtNN := types.NamespacedName{Name: rtName, Namespace: namespace}

		BeforeEach(func() {
			By("Creating a RoundTable with warm pool configuration")
			rt := &aiv1alpha1.RoundTable{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.RoundTableSpec{
					NATS: aiv1alpha1.RoundTableNATS{
						URL:           "nats://nats.test:4222",
						SubjectPrefix: "test",
						TasksStream:   "test_tasks",
						ResultsStream: "test_results",
					},
					KnightTemplates: map[string]aiv1alpha1.KnightSpec{
						templateName: {
							Domain: "general",
							Model:  "claude-sonnet-4-20250514",
							Skills: []string{"general"},
							NATS: aiv1alpha1.KnightNATS{
								URL:           "nats://nats.test:4222",
								Subjects:      []string{"test.tasks.general.>"},
								Stream:        "test_tasks",
								ResultsStream: "test_results",
							},
							Concurrency: 2,
							TaskTimeout: 120,
						},
					},
					WarmPool: &aiv1alpha1.WarmPoolConfig{
						Size: poolSize,
						Template: aiv1alpha1.KnightSpec{
							Domain: "general",
							Model:  "claude-sonnet-4-20250514",
							Skills: []string{"general"},
							NATS: aiv1alpha1.KnightNATS{
								URL:           "nats://nats.test:4222",
								Subjects:      []string{"test.tasks.general.>"},
								Stream:        "test_tasks",
								ResultsStream: "test_results",
							},
							Concurrency: 2,
							TaskTimeout: 120,
						},
						MaxIdleTime: "1h",
					},
				},
			}
			Expect(k8sClient.Create(ctx, rt)).To(Succeed())
		})

		AfterEach(func() {
			By("Cleaning up RoundTable and warm pool knights")
			rt := &aiv1alpha1.RoundTable{}
			if err := k8sClient.Get(ctx, rtNN, rt); err == nil {
				// Delete warm pool knights first
				knights := &aiv1alpha1.KnightList{}
				_ = k8sClient.List(ctx, knights)
				for _, knight := range knights.Items {
					if knight.Labels[aiv1alpha1.LabelRoundTable] == rtName &&
						knight.Labels[aiv1alpha1.LabelWarmPool] == "true" {
						_ = k8sClient.Delete(ctx, &knight)
					}
				}
				_ = k8sClient.Delete(ctx, rt)
			}
		})

		It("should create knights to match the pool size", func() {
			By("Reconciling the RoundTable")
			reconciler := &RoundTableReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			// Reconcile multiple times to ensure knights are created
			for i := 0; i < 5; i++ {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rtNN})
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(100 * time.Millisecond)
			}

			By("Verifying warm pool knights were created")
			knights := &aiv1alpha1.KnightList{}
			Expect(k8sClient.List(ctx, knights)).To(Succeed())

			warmKnights := []aiv1alpha1.Knight{}
			for _, knight := range knights.Items {
				if knight.Labels[aiv1alpha1.LabelRoundTable] == rtName &&
					knight.Labels[aiv1alpha1.LabelWarmPool] == "true" {
					warmKnights = append(warmKnights, knight)
				}
			}

			Expect(warmKnights).To(HaveLen(int(poolSize)))

			By("Verifying warm pool status is updated")
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNN, rt)).To(Succeed())
			Expect(rt.Status.WarmPool).NotTo(BeNil())
			// Initially all knights are provisioning (not ready yet)
			Expect(rt.Status.WarmPool.Provisioning).To(Equal(poolSize))
			Expect(rt.Status.WarmPool.Available).To(Equal(int32(0)))
			Expect(rt.Status.WarmPool.Claimed).To(Equal(int32(0)))
		})

		It("should recycle idle knights that exceed MaxIdleTime", func() {
			By("Creating a warm pool knight with old creation timestamp")
			oldKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName + "-warm-old",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelRoundTable:      rtName,
						aiv1alpha1.LabelWarmPool:        "true",
						aiv1alpha1.LabelWarmPoolClaimed: "false",
					},
					Annotations: map[string]string{
						// Set creation time to 2 hours ago
						aiv1alpha1.AnnotationWarmPoolCreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
					Concurrency: 2,
					TaskTimeout: 120,
				},
			}
			Expect(k8sClient.Create(ctx, oldKnight)).To(Succeed())

			// Mark it as ready
			oldKnight.Status.Ready = true
			oldKnight.Status.Phase = aiv1alpha1.KnightPhaseReady
			Expect(k8sClient.Status().Update(ctx, oldKnight)).To(Succeed())

			By("Reconciling the RoundTable")
			reconciler := &RoundTableReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rtNN})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the old knight was recycled")
			// Give it a moment for the delete to propagate
			Eventually(func() bool {
				knight := &aiv1alpha1.Knight{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      rtName + "-warm-old",
					Namespace: namespace,
				}, knight)
				return err != nil
			}, 5*time.Second, 500*time.Millisecond).Should(BeTrue())
		})

		It("should update status counts correctly", func() {
			By("Creating warm pool knights with different states")
			// Ready unclaimed knight
			readyKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName + "-warm-ready",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelRoundTable:      rtName,
						aiv1alpha1.LabelWarmPool:        "true",
						aiv1alpha1.LabelWarmPoolClaimed: "false",
					},
					Annotations: map[string]string{
						aiv1alpha1.AnnotationWarmPoolCreatedAt: time.Now().Format(time.RFC3339),
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, readyKnight)).To(Succeed())
			readyKnight.Status.Ready = true
			readyKnight.Status.Phase = aiv1alpha1.KnightPhaseReady
			Expect(k8sClient.Status().Update(ctx, readyKnight)).To(Succeed())

			// Claimed knight
			claimedKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName + "-warm-claimed",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelRoundTable:      rtName,
						aiv1alpha1.LabelWarmPool:        "true",
						aiv1alpha1.LabelWarmPoolClaimed: "true",
					},
					Annotations: map[string]string{
						aiv1alpha1.AnnotationWarmPoolCreatedAt: time.Now().Format(time.RFC3339),
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, claimedKnight)).To(Succeed())

			// Provisioning knight
			provisioningKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName + "-warm-provisioning",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelRoundTable:      rtName,
						aiv1alpha1.LabelWarmPool:        "true",
						aiv1alpha1.LabelWarmPoolClaimed: "false",
					},
					Annotations: map[string]string{
						aiv1alpha1.AnnotationWarmPoolCreatedAt: time.Now().Format(time.RFC3339),
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, provisioningKnight)).To(Succeed())
			provisioningKnight.Status.Ready = false
			provisioningKnight.Status.Phase = aiv1alpha1.KnightPhaseProvisioning
			Expect(k8sClient.Status().Update(ctx, provisioningKnight)).To(Succeed())

			By("Reconciling the RoundTable")
			reconciler := &RoundTableReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rtNN})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status counts are accurate")
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, rtNN, rt)).To(Succeed())
			Expect(rt.Status.WarmPool).NotTo(BeNil())
			Expect(rt.Status.WarmPool.Available).To(Equal(int32(1))) // readyKnight
			Expect(rt.Status.WarmPool.Claimed).To(Equal(int32(1)))   // claimedKnight
			// Provisioning count includes the ones from the pool size as well
			Expect(rt.Status.WarmPool.Provisioning).To(BeNumerically(">=", int32(1)))
		})

		It("should not create warm pool knights when pool is not configured", func() {
			By("Creating a RoundTable without warm pool")
			rtNoPool := &aiv1alpha1.RoundTable{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rtName + "-nopool",
					Namespace: namespace,
				},
				Spec: aiv1alpha1.RoundTableSpec{
					NATS: aiv1alpha1.RoundTableNATS{
						URL:           "nats://nats.test:4222",
						SubjectPrefix: "test",
						TasksStream:   "test_tasks",
						ResultsStream: "test_results",
					},
					KnightTemplates: map[string]aiv1alpha1.KnightSpec{
						templateName: {
							Domain: "general",
							Model:  "claude-sonnet-4-20250514",
							Skills: []string{"general"},
							NATS: aiv1alpha1.KnightNATS{
								URL:           "nats://nats.test:4222",
								Subjects:      []string{"test.tasks.general.>"},
								Stream:        "test_tasks",
								ResultsStream: "test_results",
							},
						},
					},
					// No WarmPool configured
				},
			}
			Expect(k8sClient.Create(ctx, rtNoPool)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, rtNoPool)
			}()

			By("Reconciling the RoundTable")
			reconciler := &RoundTableReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rtName + "-nopool",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no warm pool knights were created")
			knights := &aiv1alpha1.KnightList{}
			Expect(k8sClient.List(ctx, knights)).To(Succeed())

			warmKnights := []aiv1alpha1.Knight{}
			for _, knight := range knights.Items {
				if knight.Labels[aiv1alpha1.LabelRoundTable] == rtName+"-nopool" &&
					knight.Labels[aiv1alpha1.LabelWarmPool] == "true" {
					warmKnights = append(warmKnights, knight)
				}
			}

			Expect(warmKnights).To(BeEmpty())

			By("Verifying warm pool status is nil")
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      rtName + "-nopool",
				Namespace: namespace,
			}, rt)).To(Succeed())
			// Status may be nil or have zero values
			if rt.Status.WarmPool != nil {
				Expect(rt.Status.WarmPool.Available).To(Equal(int32(0)))
				Expect(rt.Status.WarmPool.Claimed).To(Equal(int32(0)))
				Expect(rt.Status.WarmPool.Provisioning).To(Equal(int32(0)))
			}
		})
	})
})
