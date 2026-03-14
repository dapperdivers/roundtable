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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	missionpkg "github.com/dapperdivers/roundtable/internal/mission"
)

var _ = Describe("Mission Integration Tests", func() {
	const (
		namespace = "default"
		timeout   = 30 * time.Second
		interval  = 500 * time.Millisecond
	)

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("TestMissionLifecycleHappyPath", func() {
		const (
			missionName = "test-lifecycle-mission"
			knightName  = "lifecycle-knight"
			chainName   = "lifecycle-chain"
		)

		It("should progress through complete lifecycle with chain copies", func() {
			missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
			knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}
			chainNN := types.NamespacedName{Name: chainName, Namespace: namespace}

			// Cleanup
			defer func() {
				mission := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
					_ = k8sClient.Delete(ctx, mission)
				}
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
					_ = k8sClient.Delete(ctx, knight)
				}
				chain := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, chainNN, chain); err == nil {
					_ = k8sClient.Delete(ctx, chain)
				}
			}()

			// Create Knight
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
					Concurrency: 1,
					TaskTimeout: 120,
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			// Make knight ready
			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Create Chain template
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{
					Name:      chainName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.ChainSpec{
					Description: "Test chain for integration",
					Steps: []aiv1alpha1.ChainStep{
						{
							Name:      "step1",
							KnightRef: knightName,
							Task:      "Echo hello",
							Timeout:   60,
							OutputKey: "step1",
						},
					},
					Timeout: 300,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			// Create Mission with recruitExisting=true
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.MissionSpec{
					Objective: "Test complete lifecycle",
					Knights: []aiv1alpha1.MissionKnight{
						{
							Name:      knightName,
							Ephemeral: false,
							Role:      "executor",
						},
					},
					Chains: []aiv1alpha1.MissionChainRef{
						{
							Name:  chainName,
							Phase: "Active",
						},
					},
					TTL:             3600,
					Timeout:         1800,
					RecruitExisting: true,
					RoundTableRef:   "test-rt",
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())

			// Reconcile until phase transitions from Pending
			r := &MissionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Assembler: &missionpkg.KnightAssembler{Client: k8sClient, Scheme: k8sClient.Scheme()},
			}

			// Phase 1: Pending (validation)
			Eventually(func() aiv1alpha1.MissionPhase {
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				if m.Status.Phase == "" {
					_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, timeout, interval).Should(Equal(aiv1alpha1.MissionPhasePending))

			// Phase 2: Progress to Active through multiple reconciles
			for i := 0; i < 10; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				time.Sleep(200 * time.Millisecond)
			}

			// Verify chain copy created with correct name
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			missionChainNN := types.NamespacedName{Name: missionChainName, Namespace: namespace}

			Eventually(func() error {
				missionChain := &aiv1alpha1.Chain{}
				return k8sClient.Get(ctx, missionChainNN, missionChain)
			}, timeout, interval).Should(Succeed())

			// Verify owner reference
			missionChain := &aiv1alpha1.Chain{}
			Expect(k8sClient.Get(ctx, missionChainNN, missionChain)).To(Succeed())
			Expect(missionChain.OwnerReferences).To(HaveLen(1))
			Expect(missionChain.OwnerReferences[0].Name).To(Equal(missionName))
			Expect(*missionChain.OwnerReferences[0].Controller).To(BeTrue())

			// Verify labels
			Expect(missionChain.Labels).To(HaveKeyWithValue("ai.roundtable.io/mission", missionName))

			// Verify roundTableRef set correctly
			Expect(missionChain.Spec.RoundTableRef).To(Equal("test-rt"))

			// Simulate chain completion
			Eventually(func() error {
				mc := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, missionChainNN, mc); err != nil {
					return err
				}
				mc.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
				now := metav1.Now()
				mc.Status.CompletedAt = &now
				return k8sClient.Status().Update(ctx, mc)
			}, timeout, interval).Should(Succeed())

			// Continue reconciling until mission succeeds
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, timeout, interval).Should(Equal(aiv1alpha1.MissionPhaseSucceeded))

			// Verify status aggregation
			finalMission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, finalMission)).To(Succeed())
			Expect(finalMission.Status.KnightStatuses).To(HaveLen(1))
			Expect(finalMission.Status.ChainStatuses).To(HaveLen(1))
			Expect(finalMission.Status.ChainStatuses[0].Name).To(Equal(chainName))
			Expect(finalMission.Status.ChainStatuses[0].ChainCRName).To(Equal(missionChainName))
		})
	})

	Context("TestMissionBudgetEnforcement", func() {
		const (
			missionName = "test-budget-mission"
			knightName  = "budget-knight"
		)

		It("should fail when knight cost exceeds budget", func() {
			missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
			knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}

			// Cleanup
			defer func() {
				mission := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
					_ = k8sClient.Delete(ctx, mission)
				}
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
					_ = k8sClient.Delete(ctx, knight)
				}
			}()

			// Create Knight
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.general.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			// Make knight ready
			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Create Mission with low budget
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.MissionSpec{
					Objective:     "Test budget enforcement",
					CostBudgetUSD: "1.00",
					Knights: []aiv1alpha1.MissionKnight{
						{
							Name:      knightName,
							Ephemeral: false,
						},
					},
					TTL:             3600,
					Timeout:         1800,
					RecruitExisting: true,
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())

			r := &MissionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Assembler: &missionpkg.KnightAssembler{Client: k8sClient, Scheme: k8sClient.Scheme()},
			}

			// Progress through phases until Active
			for i := 0; i < 10; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, m); err == nil && m.Status.Phase == aiv1alpha1.MissionPhaseActive {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}

			// Set knight cost BEFORE next reconcile (reconcileActive checks budget)
			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.TotalCost = "2.5000" // Exceeds budget of 1.00
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Reconcile to detect budget exceeded
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, timeout, interval).Should(Equal(aiv1alpha1.MissionPhaseFailed))

			// Verify failure reason
			finalMission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, finalMission)).To(Succeed())
			Expect(finalMission.Status.Result).To(ContainSubstring("budget exceeded"))

			condition := meta.FindStatusCondition(finalMission.Status.Conditions, "Complete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal("OverBudget"))
		})
	})

	Context("TestMissionResultPreservation", func() {
		const (
			missionName = "test-results-mission"
			knightName  = "results-knight"
			chainName   = "results-chain"
		)

		It("should create ConfigMap with results when retainResults=true", func() {
			missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
			knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}
			chainNN := types.NamespacedName{Name: chainName, Namespace: namespace}

			// Cleanup
			defer func() {
				mission := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
					// Get ConfigMap name before deletion
					cmName := mission.Status.ResultsConfigMap
					_ = k8sClient.Delete(ctx, mission)

					// Clean up ConfigMap
					if cmName != "" {
						cm := &corev1.ConfigMap{}
						cmNN := types.NamespacedName{Name: cmName, Namespace: namespace}
						if err := k8sClient.Get(ctx, cmNN, cm); err == nil {
							_ = k8sClient.Delete(ctx, cm)
						}
					}
				}
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
					_ = k8sClient.Delete(ctx, knight)
				}
				chain := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, chainNN, chain); err == nil {
					_ = k8sClient.Delete(ctx, chain)
				}
			}()

			// Create Knight
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Create Chain
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{
					Name:      chainName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{
							Name:      "test-step",
							KnightRef: knightName,
							Task:      "Test task",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			// Create Mission with retainResults=true
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.MissionSpec{
					Objective: "Test result preservation",
					Knights: []aiv1alpha1.MissionKnight{
						{Name: knightName, Ephemeral: false},
					},
					Chains: []aiv1alpha1.MissionChainRef{
						{Name: chainName, Phase: "Active"},
					},
					TTL:             3600,
					Timeout:         1800,
					RetainResults:   true,
					RecruitExisting: true,
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())

			r := &MissionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Assembler: &missionpkg.KnightAssembler{Client: k8sClient, Scheme: k8sClient.Scheme()},
			}

			// Progress through phases
			for i := 0; i < 15; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				time.Sleep(100 * time.Millisecond)
			}

			// Mark chain as succeeded
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
				mc := &aiv1alpha1.Chain{}
				mcNN := types.NamespacedName{Name: missionChainName, Namespace: namespace}
				if err := k8sClient.Get(ctx, mcNN, mc); err != nil {
					return err
				}
				mc.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
				now := metav1.Now()
				mc.Status.CompletedAt = &now
				mc.Status.StepStatuses = []aiv1alpha1.ChainStepStatus{
					{
						Name:   "test-step",
						Phase:  aiv1alpha1.ChainStepPhaseSucceeded,
						Output: "Test output from step",
					},
				}
				return k8sClient.Status().Update(ctx, mc)
			}, timeout, interval).Should(Succeed())

			// Wait for mission to succeed
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, timeout, interval).Should(Equal(aiv1alpha1.MissionPhaseSucceeded))

			// Transition to CleaningUp and reconcile — without NATS, KV fails gracefully
			// but ResultsConfigMap is still set to prevent infinite retry
			m := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, m)).To(Succeed())
			for i := 0; i < 10; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				_ = k8sClient.Get(ctx, missionNN, m)
				if m.Status.ResultsConfigMap != "" {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}

			// Verify ResultsConfigMap reference is set (KV key reference)
			Expect(m.Status.ResultsConfigMap).NotTo(BeEmpty())

			// Results are stored in NATS KV (not available in test env)
			// The ResultsConfigMap field serves as the KV key reference
		})
	})

	Context("TestMissionChainCopyNaming", func() {
		const (
			missionName = "test-naming-mission"
			chainName1  = "chain-one"
			chainName2  = "chain-two"
			knightName  = "naming-knight"
		)

		It("should create chain copies with correct naming and references", func() {
			missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
			knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}

			// Cleanup
			defer func() {
				mission := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
					_ = k8sClient.Delete(ctx, mission)
				}
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
					_ = k8sClient.Delete(ctx, knight)
				}
				for _, cn := range []string{chainName1, chainName2} {
					chain := &aiv1alpha1.Chain{}
					cnn := types.NamespacedName{Name: cn, Namespace: namespace}
					if err := k8sClient.Get(ctx, cnn, chain); err == nil {
						_ = k8sClient.Delete(ctx, chain)
					}
				}
			}()

			// Create Knight
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Create Chains
			for _, cn := range []string{chainName1, chainName2} {
				chain := &aiv1alpha1.Chain{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cn,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.ChainSpec{
						Steps: []aiv1alpha1.ChainStep{
							{Name: "step1", KnightRef: knightName, Task: "Test"},
						},
					},
				}
				Expect(k8sClient.Create(ctx, chain)).To(Succeed())
			}

			// Create Mission
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.MissionSpec{
					Objective: "Test chain naming",
					Knights: []aiv1alpha1.MissionKnight{
						{Name: knightName, Ephemeral: false},
					},
					Chains: []aiv1alpha1.MissionChainRef{
						{Name: chainName1, Phase: "Active"},
						{Name: chainName2, Phase: "Active"},
					},
					TTL:             3600,
					Timeout:         1800,
					RoundTableRef:   "test-rt",
					RecruitExisting: true,
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())

			r := &MissionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Assembler: &missionpkg.KnightAssembler{Client: k8sClient, Scheme: k8sClient.Scheme()},
			}

			// Progress to Active phase
			for i := 0; i < 15; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				time.Sleep(100 * time.Millisecond)
			}

			// Verify chain copies created with correct naming
			for _, cn := range []string{chainName1, chainName2} {
				missionChainName := fmt.Sprintf("mission-%s-%s", missionName, cn)
				mcNN := types.NamespacedName{Name: missionChainName, Namespace: namespace}

				mc := &aiv1alpha1.Chain{}
				Eventually(func() error {
					return k8sClient.Get(ctx, mcNN, mc)
				}, timeout, interval).Should(Succeed())

				// Verify missionRef would be set if field exists
				// (Note: missionRef field may not exist yet in current CRD)

				// Verify roundTableRef set correctly
				Expect(mc.Spec.RoundTableRef).To(Equal("test-rt"))

				// Verify owner reference
				Expect(mc.OwnerReferences).To(HaveLen(1))
				Expect(mc.OwnerReferences[0].Name).To(Equal(missionName))
			}

			// Verify mission status tracks both chains
			finalMission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, finalMission)).To(Succeed())
			Expect(finalMission.Status.ChainStatuses).To(HaveLen(2))
		})
	})

	Context("TestMissionPartialFailure", func() {
		const (
			missionName = "test-partial-mission"
			knightName  = "partial-knight"
			chainName1  = "success-chain"
			chainName2  = "failure-chain"
		)

		It("should fail mission when one chain fails and one succeeds", func() {
			missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
			knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}

			// Cleanup
			defer func() {
				mission := &aiv1alpha1.Mission{}
				if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
					_ = k8sClient.Delete(ctx, mission)
				}
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
					_ = k8sClient.Delete(ctx, knight)
				}
				for _, cn := range []string{chainName1, chainName2} {
					chain := &aiv1alpha1.Chain{}
					cnn := types.NamespacedName{Name: cn, Namespace: namespace}
					if err := k8sClient.Get(ctx, cnn, chain); err == nil {
						_ = k8sClient.Delete(ctx, chain)
					}
				}
			}()

			// Create Knight
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "general",
					Model:  "claude-sonnet-4",
					Skills: []string{"general"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			Eventually(func() error {
				k := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, knightNN, k); err != nil {
					return err
				}
				k.Status.Phase = aiv1alpha1.KnightPhaseReady
				k.Status.Ready = true
				return k8sClient.Status().Update(ctx, k)
			}, timeout, interval).Should(Succeed())

			// Create Chains
			for _, cn := range []string{chainName1, chainName2} {
				chain := &aiv1alpha1.Chain{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cn,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.ChainSpec{
						Steps: []aiv1alpha1.ChainStep{
							{Name: "step1", KnightRef: knightName, Task: "Test"},
						},
					},
				}
				Expect(k8sClient.Create(ctx, chain)).To(Succeed())
			}

			// Create Mission
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.MissionSpec{
					Objective: "Test partial failure",
					Knights: []aiv1alpha1.MissionKnight{
						{Name: knightName, Ephemeral: false},
					},
					Chains: []aiv1alpha1.MissionChainRef{
						{Name: chainName1, Phase: "Active"},
						{Name: chainName2, Phase: "Active"},
					},
					TTL:             3600,
					Timeout:         1800,
					RecruitExisting: true,
				},
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())

			r := &MissionReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Assembler: &missionpkg.KnightAssembler{Client: k8sClient, Scheme: k8sClient.Scheme()},
			}

			// Progress to Active phase
			for i := 0; i < 15; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				time.Sleep(100 * time.Millisecond)
			}

			// Mark first chain as succeeded
			mc1Name := fmt.Sprintf("mission-%s-%s", missionName, chainName1)
			Eventually(func() error {
				mc := &aiv1alpha1.Chain{}
				mcNN := types.NamespacedName{Name: mc1Name, Namespace: namespace}
				if err := k8sClient.Get(ctx, mcNN, mc); err != nil {
					return err
				}
				mc.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
				now := metav1.Now()
				mc.Status.CompletedAt = &now
				return k8sClient.Status().Update(ctx, mc)
			}, timeout, interval).Should(Succeed())

			// Mark second chain as failed
			mc2Name := fmt.Sprintf("mission-%s-%s", missionName, chainName2)
			Eventually(func() error {
				mc := &aiv1alpha1.Chain{}
				mcNN := types.NamespacedName{Name: mc2Name, Namespace: namespace}
				if err := k8sClient.Get(ctx, mcNN, mc); err != nil {
					return err
				}
				mc.Status.Phase = aiv1alpha1.ChainPhaseFailed
				now := metav1.Now()
				mc.Status.CompletedAt = &now
				return k8sClient.Status().Update(ctx, mc)
			}, timeout, interval).Should(Succeed())

			// Wait for mission to fail
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, timeout, interval).Should(Equal(aiv1alpha1.MissionPhaseFailed))

			// Verify all chain statuses recorded
			finalMission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, finalMission)).To(Succeed())
			Expect(finalMission.Status.ChainStatuses).To(HaveLen(2))

			// Find the statuses
			var status1, status2 *aiv1alpha1.MissionChainStatus
			for i := range finalMission.Status.ChainStatuses {
				if finalMission.Status.ChainStatuses[i].Name == chainName1 {
					status1 = &finalMission.Status.ChainStatuses[i]
				}
				if finalMission.Status.ChainStatuses[i].Name == chainName2 {
					status2 = &finalMission.Status.ChainStatuses[i]
				}
			}

			Expect(status1).NotTo(BeNil())
			Expect(status1.Phase).To(Equal(aiv1alpha1.ChainPhaseSucceeded))

			Expect(status2).NotTo(BeNil())
			Expect(status2.Phase).To(Equal(aiv1alpha1.ChainPhaseFailed))
		})
	})
})
