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
			// First reconcile: adds finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Second reconcile: initializes status
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

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

			// Add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			// Init: Pending
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhasePending))

			// Pending → Provisioning
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseProvisioning))

			// Provisioning → Assembling
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseAssembling))

			// Make knight ready
			makeKnightReady()

			// Assembling → Briefing
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseBriefing))

			// Briefing → Active
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseActive))

			// Active → Succeeded (no chains)
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseSucceeded))
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

			// Drive to Active phase
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			makeKnightReady()

			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
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
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

			// Mark mission chain as succeeded
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
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
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

			// Mark mission chain as failed
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
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
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: "dummy-chain", Phase: "Active"},
				},
				TTL:     3600,
				Timeout: 1800,
			})

			r := newReconciler()

			// Drive to Active
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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

	Context("Result ConfigMap creation", func() {
		BeforeEach(func() {
			createKnight()
			createChain()
			createMission(aiv1alpha1.MissionSpec{
				Objective:     "Test result retention",
				RetainResults: true,
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
			// Clean up ConfigMap
			cmName := fmt.Sprintf("mission-%s-results", missionName)
			cm := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      cmName,
				Namespace: namespace,
			}, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
			deleteMission()
			deleteChain()
			deleteKnight()
		})

		It("should create results ConfigMap when retainResults=true", func() {
			r := newReconciler()

			// Drive to Active and complete
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

			// Mark chain as succeeded
			missionChainName := fmt.Sprintf("mission-%s-%s", missionName, chainName)
			Eventually(func() error {
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

			// Wait for completion
			Eventually(func() aiv1alpha1.MissionPhase {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
				mission := &aiv1alpha1.Mission{}
				_ = k8sClient.Get(ctx, missionNN, mission)
				return mission.Status.Phase
			}, "5s", "500ms").Should(Equal(aiv1alpha1.MissionPhaseSucceeded))

			// Transition to CleaningUp
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			// Reconcile cleanup - should create ConfigMap
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			// Verify ConfigMap was created
			cmName := fmt.Sprintf("mission-%s-results", missionName)
			cm := &corev1.ConfigMap{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      cmName,
					Namespace: namespace,
				}, cm)
			}, "5s", "500ms").Should(Succeed())

			Expect(cm.Labels).To(HaveKeyWithValue("ai.roundtable.io/mission", missionName))
			Expect(cm.Labels).To(HaveKeyWithValue("ai.roundtable.io/results", "true"))
			Expect(cm.Data).To(HaveKey("mission-name"))
			Expect(cm.Data["mission-name"]).To(Equal(missionName))
			Expect(cm.Data).To(HaveKey("objective"))
			Expect(cm.Data).To(HaveKey("phase"))

			// Verify mission status tracks the ConfigMap
			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.ResultsConfigMap).To(Equal(cmName))
		})
	})

	Context("When mission has timeout", func() {
		BeforeEach(func() {
			createKnight()
			createMission(aiv1alpha1.MissionSpec{
				Objective: "Test timeout",
				Knights: []aiv1alpha1.MissionKnight{
					{Name: knightName, Role: "tester"},
				},
				Chains: []aiv1alpha1.MissionChainRef{
					{Name: "some-chain", Phase: "Active"},
				},
				TTL:     3600,
				Timeout: 60,
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should fail the mission when timeout is exceeded", func() {
			r := newReconciler()

			// Drive to Active
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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

			// Drive through Pending and Provisioning
			for i := 0; i < 4; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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

			// Drive through phases
			for i := 0; i < 6; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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

			// Drive through lifecycle
			for i := 0; i < 8; i++ {
				if i == 3 {
					makeKnightReady()
				}
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

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
