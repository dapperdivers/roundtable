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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("Mission Controller", func() {
	const (
		missionName = "test-mission"
		knightName  = "test-mission-knight"
		chainName   = "test-chain"
		namespace   = "default"
	)

	ctx := context.Background()

	missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
	knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}
	chainNN := types.NamespacedName{Name: chainName, Namespace: namespace}

	createKnight := func() {
		knight := &aiv1alpha1.Knight{}
		err := k8sClient.Get(ctx, knightNN, knight)
		if err != nil && errors.IsNotFound(err) {
			knight = &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: namespace,
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
						MaxDeliver:    1,
					},
					Concurrency: 1,
					TaskTimeout: 120,
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())
		}
	}

	makeKnightReady := func() {
		knight := &aiv1alpha1.Knight{}
		Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
		knight.Status.Phase = aiv1alpha1.KnightPhaseReady
		knight.Status.Ready = true
		Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())
	}

	deleteKnight := func() {
		knight := &aiv1alpha1.Knight{}
		if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
			_ = k8sClient.Delete(ctx, knight)
		}
	}

	createChain := func() {
		chain := &aiv1alpha1.Chain{}
		err := k8sClient.Get(ctx, chainNN, chain)
		if err != nil && errors.IsNotFound(err) {
			chain = &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{
					Name:      chainName,
					Namespace: namespace,
				},
				Spec: aiv1alpha1.ChainSpec{
					Description: "Test chain",
					Steps: []aiv1alpha1.ChainStep{
						{
							Name:      "step1",
							KnightRef: knightName,
							Task:      "Execute test task",
						},
					},
					RoundTableRef: "default",
					Timeout:       300,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())
		}
	}

	deleteChain := func() {
		chain := &aiv1alpha1.Chain{}
		if err := k8sClient.Get(ctx, chainNN, chain); err == nil {
			_ = k8sClient.Delete(ctx, chain)
		}
	}

	createMission := func(spec aiv1alpha1.MissionSpec) {
		mission := &aiv1alpha1.Mission{}
		err := k8sClient.Get(ctx, missionNN, mission)
		if err != nil && errors.IsNotFound(err) {
			mission = &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{
					Name:      missionName,
					Namespace: namespace,
				},
				Spec: spec,
			}
			Expect(k8sClient.Create(ctx, mission)).To(Succeed())
		}
	}

	deleteMission := func() {
		mission := &aiv1alpha1.Mission{}
		if err := k8sClient.Get(ctx, missionNN, mission); err == nil {
			// Delete ephemeral RoundTable if it exists
			if mission.Status.RoundTableName != "" {
				rt := &aiv1alpha1.RoundTable{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mission.Status.RoundTableName,
					Namespace: namespace,
				}, rt); err == nil {
					_ = k8sClient.Delete(ctx, rt)
				}
			}

			// Remove finalizer for clean test teardown
			mission.Finalizers = nil
			_ = k8sClient.Update(ctx, mission)
			_ = k8sClient.Delete(ctx, mission)
		}
		// Also delete mission-scoped chains
		missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
		missionChain := &aiv1alpha1.Chain{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      missionChainName,
			Namespace: namespace,
		}, missionChain); err == nil {
			_ = k8sClient.Delete(ctx, missionChain)
		}
	}


	// driveToPhase reconciles until the mission reaches targetPhase or maxIter is exceeded.
	// Optional beforeReconcile callback runs before each reconcile (e.g., to make knights ready).
	driveToPhase := func(r *MissionReconciler, targetPhase aiv1alpha1.MissionPhase, maxIter int, beforeReconcile ...func(aiv1alpha1.MissionPhase)) {
		for i := 0; i < maxIter; i++ {
			// Run callbacks before reconciling (e.g., make knights ready)
			m := &aiv1alpha1.Mission{}
			if err := k8sClient.Get(ctx, missionNN, m); err == nil {
				for _, fn := range beforeReconcile {
					fn(m.Status.Phase)
				}
			}
			// Reconcile
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Check phase AFTER reconciling
			if err := k8sClient.Get(ctx, missionNN, m); err == nil && m.Status.Phase == targetPhase {
				return
			}
		}
		// Final check
		m := &aiv1alpha1.Mission{}
		_ = k8sClient.Get(ctx, missionNN, m)
		Expect(m.Status.Phase).To(Equal(targetPhase), fmt.Sprintf("driveToPhase: wanted %s, got %s after %d iterations", targetPhase, m.Status.Phase, maxIter))
	}

	// readyOnAssembling returns a callback that makes the knight ready when phase is Assembling.
	readyOnAssembling := func() func(aiv1alpha1.MissionPhase) {
		called := false
		return func(phase aiv1alpha1.MissionPhase) {
			if phase == aiv1alpha1.MissionPhaseAssembling && !called {
				makeKnightReady()
				called = true
			}
		}
	}

	// makeEphemeralRoundTableReady finds and marks the ephemeral RoundTable as Ready
	makeEphemeralRoundTableReady := func() {
		// Get mission to find the RoundTable name
		mission := &aiv1alpha1.Mission{}
		if err := k8sClient.Get(ctx, missionNN, mission); err != nil {
			return
		}
		if mission.Status.RoundTableName == "" {
			return
		}

		// Get the ephemeral RoundTable
		rt := &aiv1alpha1.RoundTable{}
		rtKey := types.NamespacedName{Name: mission.Status.RoundTableName, Namespace: namespace}
		if err := k8sClient.Get(ctx, rtKey, rt); err != nil {
			return
		}

		// Mark as Ready
		rt.Status.Phase = aiv1alpha1.RoundTablePhaseReady
		rt.Status.KnightsReady = 0
		rt.Status.KnightsTotal = 0
		_ = k8sClient.Status().Update(ctx, rt)
	}

	// readyOnProvisioning returns a callback that makes the RoundTable ready when phase is Provisioning.
	readyOnProvisioning := func() func(aiv1alpha1.MissionPhase) {
		return func(phase aiv1alpha1.MissionPhase) {
			if phase == aiv1alpha1.MissionPhaseProvisioning {
				// Keep trying to mark RT as ready until it exists and is marked
				makeEphemeralRoundTableReady()
			}
		}
	}

	newReconciler := func() *MissionReconciler {
		return &MissionReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	Context("When reconciling a new mission", func() {
		BeforeEach(func() {
			createKnight()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test the mission controller",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should add a finalizer on first reconcile", func() {
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Finalizers).To(ContainElement(missionFinalizer))
		})

		It("should initialize to Pending phase", func() {
			r := newReconciler()
			// Reconcile until Pending (finalizer + init may happen in one or two cycles)
			driveToPhase(r, aiv1alpha1.MissionPhasePending, 5)

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhasePending))
			Expect(mission.Status.StartedAt).NotTo(BeNil())
			Expect(mission.Status.ExpiresAt).NotTo(BeNil())
			Expect(mission.Status.KnightStatuses).To(HaveLen(1))
			Expect(mission.Status.KnightStatuses[0].Name).To(Equal(knightName))
		})
	})

	Context("Phase transitions", func() {
		BeforeEach(func() {
			createKnight()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test phase transitions",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should progress through phases: Pending → Provisioning → Assembling → Briefing → Active → Succeeded", func() {
			r := newReconciler()
			cbProvisioning := readyOnProvisioning()
			cbAssembling := readyOnAssembling()

			// Verify each phase is reachable in order
			for _, phase := range []aiv1alpha1.MissionPhase{
				aiv1alpha1.MissionPhasePending,
				aiv1alpha1.MissionPhaseProvisioning,
				aiv1alpha1.MissionPhaseAssembling,
				aiv1alpha1.MissionPhaseBriefing,
				aiv1alpha1.MissionPhaseActive,
				aiv1alpha1.MissionPhaseSucceeded,
			} {
				driveToPhase(r, phase, 10, cbProvisioning, cbAssembling)
			}
		})
	})

	Context("Chain copy creation", func() {
		BeforeEach(func() {
			createKnight()
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test chain copy",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{
						Name:  chainName,
						Phase: "Active",
					},
				},
				RoundTableRef: "test-rt",
				TTL:           3600,
				Timeout:       1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteChain()
			deleteKnight()
		})

		It("should create a mission-scoped chain copy with correct name and ownerRef", func() {
			r := newReconciler()

			// Drive to Active phase (creates chain copies)
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())
			// One more reconcile to process chains
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			// Check mission-scoped chain was created
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			missionChain := &aiv1alpha1.Chain{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      missionChainName,
					Namespace: namespace,
				}, missionChain)
			}, "5s", "500ms").Should(Succeed())

			// Verify chain properties
			Expect(missionChain.Name).To(Equal(missionChainName))
			Expect(missionChain.Labels).To(HaveKeyWithValue("ai.roundtable.io/mission", missionName))
			Expect(missionChain.Labels).To(HaveKeyWithValue("ai.roundtable.io/chain-phase", "Active"))
			Expect(missionChain.Spec.RoundTableRef).To(Equal("test-rt"))

			// Verify owner reference
			Expect(missionChain.OwnerReferences).To(HaveLen(1))
			Expect(missionChain.OwnerReferences[0].Name).To(Equal(missionName))
			Expect(missionChain.OwnerReferences[0].Kind).To(Equal("Mission"))
			Expect(*missionChain.OwnerReferences[0].Controller).To(BeTrue())

			// Verify mission status tracks the chain
			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.ChainStatuses).To(HaveLen(1))
			Expect(mission.Status.ChainStatuses[0].Name).To(Equal(chainName))
			Expect(mission.Status.ChainStatuses[0].ChainCRName).To(Equal(missionChainName))
		})
	})

	Context("Status aggregation", func() {
		BeforeEach(func() {
			createKnight()
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should succeed when all chains succeed", func() {
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test all chains succeed",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: chainName, Phase: "Active"},
				},
				RoundTableRef: "test-rt",
				TTL:           3600,
				Timeout:       1800,
			})

			r := newReconciler()

			// Drive to Active phase
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Reconcile once more to let reconcileActive create chain copies
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Mark mission chain as succeeded
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				missionChain := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      missionChainName,
					Namespace: namespace,
				}, missionChain); err != nil {
					return err
				}
				missionChain.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
				return k8sClient.Status().Update(ctx, missionChain)
			}, "5s", "500ms").Should(Succeed())

			// Reconcile should detect success
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseSucceeded))

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Result).To(ContainSubstring("successfully"))
			condition := meta.FindStatusCondition(mission.Status.Conditions, "Complete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("Succeeded"))

			deleteChain()
		})

		It("should fail when one chain fails", func() {
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test chain failure",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: chainName, Phase: "Active"},
				},
				RoundTableRef: "test-rt",
				TTL:           3600,
				Timeout:       1800,
			})

			r := newReconciler()

			// Drive to Active phase
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Reconcile once more to let reconcileActive create chain copies
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Mark mission chain as failed
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				missionChain := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      missionChainName,
					Namespace: namespace,
				}, missionChain); err != nil {
					return err
				}
				missionChain.Status.Phase = aiv1alpha1.ChainPhaseFailed
				return k8sClient.Status().Update(ctx, missionChain)
			}, "5s", "500ms").Should(Succeed())

			// Reconcile should detect failure
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseFailed))

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Result).To(ContainSubstring("failed"))
			condition := meta.FindStatusCondition(mission.Status.Conditions, "Complete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("ChainFailed"))

			deleteChain()
		})
	})

	Context("Budget enforcement", func() {
		BeforeEach(func() {
			createKnight()
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should succeed when under budget", func() {
			createMission(aiv1alpha1.MissionSpec{
				Objective:     "Test under budget",
				CostBudgetUSD: "10.00",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				TTL:     3600,
				Timeout: 1800,
			})

			r := newReconciler()

			// Drive to Active
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Set knight cost under budget
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
			knight.Status.TotalCost = "5.25"
			Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())

			// Reconcile should succeed
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseSucceeded))

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.TotalCost).To(Equal("5.2500"))
		})

		It("should fail when over budget", func() {
			createMission(aiv1alpha1.MissionSpec{
				Objective:     "Test over budget",
				CostBudgetUSD: "10.00",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				TTL:     3600,
				Timeout: 1800,
			})

			r := newReconciler()

			// Drive to Active (no chains, so it will try to succeed immediately)
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Set knight cost over budget
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
			knight.Status.TotalCost = "15.75"
			Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())

			// Reconcile should fail due to budget
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseFailed))

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Result).To(ContainSubstring("budget exceeded"))
			Expect(mission.Status.TotalCost).To(Equal("15.7500"))
			condition := meta.FindStatusCondition(mission.Status.Conditions, "Complete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal("OverBudget"))
		})
	})

	Context("Result storage (NATS KV)", func() {
		BeforeEach(func() {
			createKnight()
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective:      "Test result retention",
				RetainResults:  true,
				RoundTableRef:  "test-rt",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: chainName, Phase: "Active"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteChain()
			deleteKnight()
		})

		It("should set ResultsConfigMap reference when retainResults=true (graceful without NATS)", func() {
			r := newReconciler()

			// Drive to Active and complete
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Reconcile once more to let reconcileActive create chain copies
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Mark chain as succeeded
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				missionChain := &aiv1alpha1.Chain{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      missionChainName,
					Namespace: namespace,
				}, missionChain); err != nil {
					return err
				}
				missionChain.Status.Phase = aiv1alpha1.ChainPhaseSucceeded
				return k8sClient.Status().Update(ctx, missionChain)
			}, "5s", "500ms").Should(Succeed())

			// Wait for completion and cleanup — without NATS, KV store fails gracefully
			// but ResultsConfigMap is still set (to prevent infinite retry)
			// Check both phase transition AND ResultsConfigMap in one Eventually
			Eventually(func() string {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.ResultsConfigMap
			}, "15s", "500ms").ShouldNot(BeEmpty())
		})
	})

	Context("When mission has timeout", func() {
		BeforeEach(func() {
			createKnight()
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test timeout",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: chainName, Phase: "Active"},
				},
				TTL:     3600,
				Timeout: 60,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteChain()
			deleteKnight()
		})

		It("should fail the mission when timeout is exceeded", func() {
			r := newReconciler()

			// Drive to Active
			driveToPhase(r, aiv1alpha1.MissionPhaseActive, 10, readyOnProvisioning(), readyOnAssembling())

			// Set startedAt to the past to trigger timeout immediately
			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			pastTime := metav1.NewTime(time.Now().Add(-120 * time.Second))
			mission.Status.StartedAt = &pastTime
			Expect(k8sClient.Status().Update(ctx, mission)).To(Succeed())

			// Reconcile should detect timeout and fail
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				m := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, m)
				return m.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseFailed))

			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Result).To(ContainSubstring("timed out"))
			condition := meta.FindStatusCondition(mission.Status.Conditions, "Complete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Reason).To(Equal("Timeout"))
		})
	})

	Context("When a knight reference is invalid", func() {
		BeforeEach(func() {
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test with missing knight",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: "nonexistent-knight", Role: "ghost"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
		})

		It("should stay in Assembling and set KnightsReady=False", func() {
			r := newReconciler()

			// Drive to Assembling (won't progress further - knight doesn't exist)
			driveToPhase(r, aiv1alpha1.MissionPhaseAssembling, 10, readyOnProvisioning())
			// One more reconcile to set KnightsReady condition
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseAssembling))

			condition := meta.FindStatusCondition(mission.Status.Conditions, "KnightsReady")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("KnightNotFound"))
		})
	})

	Context("When mission has no chains (briefing-only)", func() {
		BeforeEach(func() {
			createKnight()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Briefing-only mission",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "listener"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should succeed immediately after briefing", func() {
			r := newReconciler()

			// Drive to Succeeded (no chains = briefing-only)
			driveToPhase(r, aiv1alpha1.MissionPhaseSucceeded, 10, readyOnProvisioning(), readyOnAssembling())

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseSucceeded))
			Expect(mission.Status.Result).To(ContainSubstring("briefing-only"))
		})
	})

	Context("Cleanup phase", func() {
		BeforeEach(func() {
			createKnight()
			createMission(aiv1alpha1.MissionSpec{
				Objective:     "Test cleanup",
				CleanupPolicy: "Retain",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				TTL:     3600,
				Timeout: 1800,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should set CleanupComplete condition when retained", func() {
			r := newReconciler()

			// Drive through entire lifecycle to CleaningUp
			driveToPhase(r, aiv1alpha1.MissionPhaseSucceeded, 10, readyOnProvisioning(), readyOnAssembling())
			driveToPhase(r, aiv1alpha1.MissionPhaseCleaningUp, 5)
			// One more reconcile to actually run reconcileCleaningUp (sets conditions)
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			// Should still exist (Retain policy)
			Expect(mission.Name).To(Equal(missionName))

			condition := meta.FindStatusCondition(mission.Status.Conditions, "CleanupComplete")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})
