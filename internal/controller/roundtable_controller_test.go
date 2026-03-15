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

	Context("Ephemeral Knight Filtering (Issue #18)", func() {
		const (
			fleetRT     = "fleet-roundtable"
			ephemeralRT = "mission-test-abc123"
		)

		BeforeEach(func() {
			// Create fleet (non-ephemeral) RoundTable
			fleetTable := &aiv1alpha1.RoundTable{}
			fleetKey := types.NamespacedName{Name: fleetRT, Namespace: namespace}
			err := k8sClient.Get(ctx, fleetKey, fleetTable)
			if err != nil && errors.IsNotFound(err) {
				fleetTable = &aiv1alpha1.RoundTable{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fleetRT,
						Namespace: namespace,
					},
					Spec: aiv1alpha1.RoundTableSpec{
						NATS: aiv1alpha1.RoundTableNATS{
							URL:           "nats://nats.test:4222",
							SubjectPrefix: "fleet-a",
							TasksStream:   "fleet_a_tasks",
							ResultsStream: "fleet_a_results",
						},
						Ephemeral: false,
					},
				}
				Expect(k8sClient.Create(ctx, fleetTable)).To(Succeed())
			}

			// Create ephemeral RoundTable
			ephRT := &aiv1alpha1.RoundTable{}
			ephKey := types.NamespacedName{Name: ephemeralRT, Namespace: namespace}
			err = k8sClient.Get(ctx, ephKey, ephRT)
			if err != nil && errors.IsNotFound(err) {
				ephRT = &aiv1alpha1.RoundTable{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ephemeralRT,
						Namespace: namespace,
						Labels: map[string]string{
							aiv1alpha1.LabelEphemeral: "true",
							aiv1alpha1.LabelMission:   "test-mission",
						},
					},
					Spec: aiv1alpha1.RoundTableSpec{
						NATS: aiv1alpha1.RoundTableNATS{
							URL:           "nats://nats.test:4222",
							SubjectPrefix: "msn-test-abc123",
							TasksStream:   "msn_test_abc123_tasks",
							ResultsStream: "msn_test_abc123_results",
						},
						Ephemeral:  true,
						MissionRef: "test-mission",
					},
				}
				Expect(k8sClient.Create(ctx, ephRT)).To(Succeed())
			}
		})

		AfterEach(func() {
			// Cleanup knights (from all tests in this context)
			for _, name := range []string{"galahad", "mission-test-scanner", "mission-other-scanner", "regular-knight", "eph-knight"} {
				k := &aiv1alpha1.Knight{}
				knn := types.NamespacedName{Name: name, Namespace: namespace}
				if err := k8sClient.Get(ctx, knn, k); err == nil {
					_ = k8sClient.Delete(ctx, k)
				}
			}
			// Cleanup RoundTables
			for _, name := range []string{fleetRT, ephemeralRT, "fleet-no-selector"} {
				rt := &aiv1alpha1.RoundTable{}
				rtKey := types.NamespacedName{Name: name, Namespace: namespace}
				if err := k8sClient.Get(ctx, rtKey, rt); err == nil {
					_ = k8sClient.Delete(ctx, rt)
				}
			}
		})

		It("should exclude ephemeral knights from fleet RoundTable aggregation", func() {
			// Ensure galahad doesn't exist from previous test
			existingGalahad := &aiv1alpha1.Knight{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "galahad", Namespace: namespace}, existingGalahad); err == nil {
				_ = k8sClient.Delete(ctx, existingGalahad)
			}

			// Create regular fleet knight
			galahad := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "galahad",
					Namespace: namespace,
					// No ephemeral label
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "security",
					Skills: []string{"security"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"fleet-a.tasks.security.>"},
						Stream:   "fleet_a_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, galahad)).To(Succeed())
			galahad.Status.Ready = true
			galahad.Status.Phase = aiv1alpha1.KnightPhaseReady
			Expect(k8sClient.Status().Update(ctx, galahad)).To(Succeed())

			// Create ephemeral knight (for mission)
			ephemeralKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mission-test-scanner",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelEphemeral:  "true",
						aiv1alpha1.LabelMission:    "test-mission",
						aiv1alpha1.LabelRoundTable: ephemeralRT,
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "security",
					Skills: []string{"security"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"msn-test-abc123.tasks.security.>"},
						Stream:   "msn_test_abc123_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ephemeralKnight)).To(Succeed())
			ephemeralKnight.Status.Ready = true
			ephemeralKnight.Status.Phase = aiv1alpha1.KnightPhaseReady
			Expect(k8sClient.Status().Update(ctx, ephemeralKnight)).To(Succeed())

			// Reconcile fleet RoundTable
			rec := newReconciler()
			fleetKey := types.NamespacedName{Name: fleetRT, Namespace: namespace}
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: fleetKey})
			Expect(err).NotTo(HaveOccurred())

			// Verify fleet RoundTable only sees galahad (not ephemeral knight)
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, fleetKey, rt)).To(Succeed())
			Expect(rt.Status.KnightsTotal).To(Equal(int32(1)), "Fleet RT should only count non-ephemeral knights")
			Expect(rt.Status.KnightsReady).To(Equal(int32(1)))
			Expect(rt.Status.Knights).To(HaveLen(1))
			Expect(rt.Status.Knights[0].Name).To(Equal("galahad"))
		})

		It("should only discover knights belonging to ephemeral RoundTable", func() {
			// Create knight for THIS ephemeral RoundTable
			missionKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mission-test-scanner",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelEphemeral:  "true",
						aiv1alpha1.LabelMission:    "test-mission",
						aiv1alpha1.LabelRoundTable: ephemeralRT,
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "security",
					Skills: []string{"security"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"msn-test-abc123.tasks.security.>"},
						Stream:   "msn_test_abc123_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, missionKnight)).To(Succeed())
			missionKnight.Status.Ready = true
			missionKnight.Status.Phase = aiv1alpha1.KnightPhaseReady
			Expect(k8sClient.Status().Update(ctx, missionKnight)).To(Succeed())

			// Create knight for DIFFERENT ephemeral RoundTable
			otherKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mission-other-scanner",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelEphemeral:  "true",
						aiv1alpha1.LabelMission:    "other-mission",
						aiv1alpha1.LabelRoundTable: "mission-other-xyz789",
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "security",
					Skills: []string{"security"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"msn-other-xyz789.tasks.security.>"},
						Stream:   "msn_other_xyz789_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, otherKnight)).To(Succeed())

			// Reconcile ephemeral RoundTable
			rec := newReconciler()
			ephKey := types.NamespacedName{Name: ephemeralRT, Namespace: namespace}
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: ephKey})
			Expect(err).NotTo(HaveOccurred())

			// Verify ephemeral RoundTable only sees its own knight
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, ephKey, rt)).To(Succeed())
			Expect(rt.Status.KnightsTotal).To(Equal(int32(1)), "Ephemeral RT should only see its own knights")
			Expect(rt.Status.KnightsReady).To(Equal(int32(1)))
			Expect(rt.Status.Knights).To(HaveLen(1))
			Expect(rt.Status.Knights[0].Name).To(Equal("mission-test-scanner"))
		})

		It("should exclude ephemeral knights even without label selector", func() {
			// Create a fleet RoundTable without explicit knight selector
			rtNoSelector := &aiv1alpha1.RoundTable{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "fleet-no-selector",
					Namespace: namespace,
				},
				Spec: aiv1alpha1.RoundTableSpec{
					NATS: aiv1alpha1.RoundTableNATS{
						URL:           "nats://nats.test:4222",
						SubjectPrefix: "fleet-b",
						TasksStream:   "fleet_b_tasks",
						ResultsStream: "fleet_b_results",
					},
					Ephemeral: false,
					// No knightSelector specified
				},
			}
			Expect(k8sClient.Create(ctx, rtNoSelector)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, rtNoSelector)
			}()

			// Create regular knight
			regularKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-knight",
					Namespace: namespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "infra",
					Skills: []string{"infra"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"fleet-b.tasks.infra.>"},
						Stream:   "fleet_b_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, regularKnight)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, regularKnight)
			}()

			// Create ephemeral knight
			ephKnight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "eph-knight",
					Namespace: namespace,
					Labels: map[string]string{
						aiv1alpha1.LabelEphemeral: "true",
					},
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "security",
					Skills: []string{"security"},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"msn-x.tasks.security.>"},
						Stream:   "msn_x_tasks",
					},
				},
			}
			Expect(k8sClient.Create(ctx, ephKnight)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, ephKnight)
			}()

			// Reconcile
			rec := newReconciler()
			key := types.NamespacedName{Name: "fleet-no-selector", Namespace: namespace}
			_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			// Verify only regular knight is counted
			rt := &aiv1alpha1.RoundTable{}
			Expect(k8sClient.Get(ctx, key, rt)).To(Succeed())
			Expect(rt.Status.KnightsTotal).To(Equal(int32(1)), "Should filter out ephemeral knight")
		})
	})
})
