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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("Helper Functions", func() {

	// ── computePhase ──────────────────────────────────────────────────────
	Describe("RoundTableReconciler.computePhase", func() {
		var (
			reconciler *RoundTableReconciler
			rt         *aiv1alpha1.RoundTable
		)

		BeforeEach(func() {
			reconciler = &RoundTableReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
			rt = &aiv1alpha1.RoundTable{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rt", Namespace: "default"},
			}
		})

		It("returns Provisioning when total is 0", func() {
			phase := reconciler.computePhase(rt, 0, 0, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseProvisioning))
		})

		It("returns Ready when readyCount equals total", func() {
			phase := reconciler.computePhase(rt, 3, 3, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
		})

		It("returns Degraded when readyCount is less than total", func() {
			phase := reconciler.computePhase(rt, 1, 3, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseDegraded))
		})

		It("returns Degraded when readyCount is 0 but total > 0", func() {
			phase := reconciler.computePhase(rt, 0, 3, 0)
			Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseDegraded))
		})

		Context("with cost budget", func() {
			BeforeEach(func() {
				rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{
					CostBudgetUSD: "100.0",
				}
			})

			It("returns OverBudget when cost exceeds budget", func() {
				phase := reconciler.computePhase(rt, 3, 3, 150.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseOverBudget))
			})

			It("returns Ready when cost is within budget", func() {
				phase := reconciler.computePhase(rt, 3, 3, 50.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			})

			It("returns OverBudget even when all knights ready", func() {
				phase := reconciler.computePhase(rt, 3, 3, 100.01)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseOverBudget))
			})
		})

		Context("with zero or empty budget", func() {
			It("ignores budget when set to '0'", func() {
				rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{
					CostBudgetUSD: "0",
				}
				phase := reconciler.computePhase(rt, 3, 3, 999.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			})

			It("ignores budget when empty string", func() {
				rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{
					CostBudgetUSD: "",
				}
				phase := reconciler.computePhase(rt, 3, 3, 999.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			})

			It("ignores budget when policies is nil", func() {
				rt.Spec.Policies = nil
				phase := reconciler.computePhase(rt, 3, 3, 999.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			})
		})

		Context("with invalid budget string", func() {
			It("ignores unparseable budget and falls through to normal logic", func() {
				rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{
					CostBudgetUSD: "not-a-number",
				}
				phase := reconciler.computePhase(rt, 3, 3, 999.0)
				Expect(phase).To(Equal(aiv1alpha1.RoundTablePhaseReady))
			})
		})
	})

	// ── initKnightStatuses ────────────────────────────────────────────────
	Describe("MissionReconciler.initKnightStatuses", func() {
		var reconciler *MissionReconciler

		BeforeEach(func() {
			reconciler = &MissionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
		})

		It("initializes empty statuses for empty knights list", func() {
			mission := &aiv1alpha1.Mission{
				Spec: aiv1alpha1.MissionSpec{Knights: []aiv1alpha1.MissionKnight{}},
			}
			reconciler.initKnightStatuses(mission)
			Expect(mission.Status.KnightStatuses).To(HaveLen(0))
		})

		It("creates one status entry per knight", func() {
			mission := &aiv1alpha1.Mission{
				Spec: aiv1alpha1.MissionSpec{
					Knights: []aiv1alpha1.MissionKnight{
						{Name: "lancelot"},
						{Name: "gawain", Ephemeral: true},
						{Name: "percival"},
					},
				},
			}
			reconciler.initKnightStatuses(mission)
			Expect(mission.Status.KnightStatuses).To(HaveLen(3))
			Expect(mission.Status.KnightStatuses[0].Name).To(Equal("lancelot"))
			Expect(mission.Status.KnightStatuses[0].Ephemeral).To(BeFalse())
			Expect(mission.Status.KnightStatuses[1].Name).To(Equal("gawain"))
			Expect(mission.Status.KnightStatuses[1].Ephemeral).To(BeTrue())
			Expect(mission.Status.KnightStatuses[2].Name).To(Equal("percival"))
		})

		It("overwrites existing statuses", func() {
			mission := &aiv1alpha1.Mission{
				Spec: aiv1alpha1.MissionSpec{
					Knights: []aiv1alpha1.MissionKnight{{Name: "new-knight"}},
				},
			}
			mission.Status.KnightStatuses = []aiv1alpha1.MissionKnightStatus{
				{Name: "old-knight"},
			}
			reconciler.initKnightStatuses(mission)
			Expect(mission.Status.KnightStatuses).To(HaveLen(1))
			Expect(mission.Status.KnightStatuses[0].Name).To(Equal("new-knight"))
		})
	})

	// ── natsPrefix ────────────────────────────────────────────────────────
	Describe("natsPrefix", func() {
		It("returns custom prefix when NATSPrefix is set", func() {
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "my-mission"},
				Spec:       aiv1alpha1.MissionSpec{NATSPrefix: "custom-prefix"},
			}
			Expect(natsPrefix(mission)).To(Equal("custom-prefix"))
		})

		It("returns mission-<name> when NATSPrefix is empty", func() {
			mission := &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "quest-alpha"},
				Spec:       aiv1alpha1.MissionSpec{},
			}
			Expect(natsPrefix(mission)).To(Equal("mission-quest-alpha"))
		})
	})

	// ── nextPhaseAfterProvisioning ────────────────────────────────────────
	Describe("nextPhaseAfterProvisioning", func() {
		It("returns Planning for meta missions", func() {
			mission := &aiv1alpha1.Mission{
				Spec: aiv1alpha1.MissionSpec{MetaMission: true},
			}
			Expect(nextPhaseAfterProvisioning(mission)).To(Equal(aiv1alpha1.MissionPhasePlanning))
		})

		It("returns Assembling for non-meta missions", func() {
			mission := &aiv1alpha1.Mission{
				Spec: aiv1alpha1.MissionSpec{MetaMission: false},
			}
			Expect(nextPhaseAfterProvisioning(mission)).To(Equal(aiv1alpha1.MissionPhaseAssembling))
		})

		It("returns Assembling when MetaMission is default (false)", func() {
			mission := &aiv1alpha1.Mission{}
			Expect(nextPhaseAfterProvisioning(mission)).To(Equal(aiv1alpha1.MissionPhaseAssembling))
		})
	})

	// ── shouldDeleteResources ─────────────────────────────────────────────
	Describe("MissionReconciler.shouldDeleteResources", func() {
		var reconciler *MissionReconciler

		BeforeEach(func() {
			reconciler = &MissionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
		})

		DescribeTable("cleanup policy behavior",
			func(policy string, phase aiv1alpha1.MissionPhase, expected bool) {
				mission := &aiv1alpha1.Mission{
					Spec:   aiv1alpha1.MissionSpec{CleanupPolicy: policy},
					Status: aiv1alpha1.MissionStatus{Phase: phase},
				}
				Expect(reconciler.shouldDeleteResources(mission)).To(Equal(expected))
			},
			Entry("Delete policy always deletes", "Delete", aiv1alpha1.MissionPhaseSucceeded, true),
			Entry("Retain policy never deletes", "Retain", aiv1alpha1.MissionPhaseSucceeded, false),
			Entry("Retain policy never deletes on failure", "Retain", aiv1alpha1.MissionPhaseFailed, false),
			Entry("OnSuccess deletes on success", "OnSuccess", aiv1alpha1.MissionPhaseSucceeded, true),
			Entry("OnSuccess retains on failure", "OnSuccess", aiv1alpha1.MissionPhaseFailed, false),
			Entry("OnFailure deletes on failure", "OnFailure", aiv1alpha1.MissionPhaseFailed, true),
			Entry("OnFailure retains on success", "OnFailure", aiv1alpha1.MissionPhaseSucceeded, false),
			Entry("empty policy defaults to delete", "", aiv1alpha1.MissionPhaseSucceeded, true),
			Entry("unknown policy defaults to delete", "SomethingElse", aiv1alpha1.MissionPhaseFailed, true),
		)
	})

	// ── updateChainStatus ─────────────────────────────────────────────────
	Describe("MissionReconciler.updateChainStatus", func() {
		var reconciler *MissionReconciler

		BeforeEach(func() {
			reconciler = &MissionReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}
		})

		It("adds a new chain status when none exists", func() {
			mission := &aiv1alpha1.Mission{}
			reconciler.updateChainStatus(mission, "my-chain", "my-chain-cr", aiv1alpha1.ChainPhaseRunning)
			Expect(mission.Status.ChainStatuses).To(HaveLen(1))
			Expect(mission.Status.ChainStatuses[0].Name).To(Equal("my-chain"))
			Expect(mission.Status.ChainStatuses[0].ChainCRName).To(Equal("my-chain-cr"))
			Expect(mission.Status.ChainStatuses[0].Phase).To(Equal(aiv1alpha1.ChainPhaseRunning))
		})

		It("updates existing chain status by name", func() {
			mission := &aiv1alpha1.Mission{
				Status: aiv1alpha1.MissionStatus{
					ChainStatuses: []aiv1alpha1.MissionChainStatus{
						{Name: "chain-a", ChainCRName: "old-cr", Phase: aiv1alpha1.ChainPhaseIdle},
					},
				},
			}
			reconciler.updateChainStatus(mission, "chain-a", "new-cr", aiv1alpha1.ChainPhaseSucceeded)
			Expect(mission.Status.ChainStatuses).To(HaveLen(1))
			Expect(mission.Status.ChainStatuses[0].ChainCRName).To(Equal("new-cr"))
			Expect(mission.Status.ChainStatuses[0].Phase).To(Equal(aiv1alpha1.ChainPhaseSucceeded))
		})

		It("appends new entry without disturbing existing ones", func() {
			mission := &aiv1alpha1.Mission{
				Status: aiv1alpha1.MissionStatus{
					ChainStatuses: []aiv1alpha1.MissionChainStatus{
						{Name: "chain-a", ChainCRName: "cr-a", Phase: aiv1alpha1.ChainPhaseRunning},
					},
				},
			}
			reconciler.updateChainStatus(mission, "chain-b", "cr-b", aiv1alpha1.ChainPhaseFailed)
			Expect(mission.Status.ChainStatuses).To(HaveLen(2))
			Expect(mission.Status.ChainStatuses[0].Name).To(Equal("chain-a"))
			Expect(mission.Status.ChainStatuses[0].Phase).To(Equal(aiv1alpha1.ChainPhaseRunning))
			Expect(mission.Status.ChainStatuses[1].Name).To(Equal("chain-b"))
			Expect(mission.Status.ChainStatuses[1].Phase).To(Equal(aiv1alpha1.ChainPhaseFailed))
		})
	})
})
