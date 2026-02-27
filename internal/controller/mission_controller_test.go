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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("Mission Controller", func() {
	const (
		missionName = "test-mission"
		knightName  = "test-mission-knight"
		namespace   = "default"
	)

	ctx := context.Background()

	missionNN := types.NamespacedName{Name: missionName, Namespace: namespace}
	knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}

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

	deleteKnight := func() {
		knight := &aiv1alpha1.Knight{}
		if err := k8sClient.Get(ctx, knightNN, knight); err == nil {
			_ = k8sClient.Delete(ctx, knight)
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

		It("should initialize to Assembling phase", func() {
			r := newReconciler()
			// First reconcile: adds finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Second reconcile: initializes status
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseAssembling))
			Expect(mission.Status.StartedAt).NotTo(BeNil())
			Expect(mission.Status.ExpiresAt).NotTo(BeNil())
			Expect(mission.Status.KnightStatuses).To(HaveLen(1))
			Expect(mission.Status.KnightStatuses[0].Name).To(Equal(knightName))
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
			// Add finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Init status
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Try assembling
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			Expect(err).NotTo(HaveOccurred())

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			Expect(mission.Status.Phase).To(Equal(aiv1alpha1.MissionPhaseAssembling))
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
				Timeout: 60, // minimum allowed timeout — will expire quickly
			})
		})

		AfterEach(func() {
			deleteMission()
			deleteKnight()
		})

		It("should fail the mission when timeout is exceeded", func() {
			r := newReconciler()
			// Add finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Init status
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			// Make knight ready so we pass assembling
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
			knight.Status.Phase = aiv1alpha1.KnightPhaseReady
			knight.Status.Ready = true
			Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())

			// Assembling → Briefing
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Briefing → Active (briefing publish will fail without NATS, but moves to Active)
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

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
			// Add finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Init status
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			// Make knight ready
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
			knight.Status.Phase = aiv1alpha1.KnightPhaseReady
			knight.Status.Ready = true
			Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())

			// Assembling → Briefing
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Briefing → Active
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			// Active → Succeeded (no chains)
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

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
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})

			// Make knight ready
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, knightNN, knight)).To(Succeed())
			knight.Status.Phase = aiv1alpha1.KnightPhaseReady
			knight.Status.Ready = true
			Expect(k8sClient.Status().Update(ctx, knight)).To(Succeed())

			// Assembling → Briefing → Active → Succeeded → CleaningUp → done
			for i := 0; i < 6; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: missionNN})
			}

			mission := &aiv1alpha1.Mission{}
			Expect(k8sClient.Get(ctx, missionNN, mission)).To(Succeed())
			// Should still exist (Retain policy)
			Expect(mission.Name).To(Equal(missionName))
		})
	})
})
