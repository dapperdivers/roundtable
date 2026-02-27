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

var _ = Describe("Chain Controller", func() {
	const (
		chainName   = "test-chain"
		knightName  = "galahad"
		knightName2 = "kay"
		namespace   = "default"
	)

	ctx := context.Background()

	chainNN := types.NamespacedName{Name: chainName, Namespace: namespace}
	knightNN := types.NamespacedName{Name: knightName, Namespace: namespace}
	knightNN2 := types.NamespacedName{Name: knightName2, Namespace: namespace}

	createKnight := func(name, domain string) {
		knight := &aiv1alpha1.Knight{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, knight)
		if err != nil && errors.IsNotFound(err) {
			knight = &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: aiv1alpha1.KnightSpec{
					Domain: domain,
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{domain},
					NATS: aiv1alpha1.KnightNATS{
						URL:      "nats://nats.test:4222",
						Subjects: []string{"fleet-a.tasks." + domain + ".>"},
						Stream:   "fleet_a_tasks",
					},
					Concurrency: 2,
					TaskTimeout: 120,
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())
		}
	}

	newReconciler := func() *ChainReconciler {
		return &ChainReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	Context("DAG Validation", func() {
		It("should detect cycles in the DAG", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "a", KnightRef: knightName, Task: "task-a", DependsOn: []string{"c"}},
						{Name: "b", KnightRef: knightName, Task: "task-b", DependsOn: []string{"a"}},
						{Name: "c", KnightRef: knightName, Task: "task-c", DependsOn: []string{"b"}},
					},
				},
			}
			err := r.validateDAG(chain)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cycle"))
		})

		It("should accept a valid DAG", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "a", KnightRef: knightName, Task: "task-a"},
						{Name: "b", KnightRef: knightName, Task: "task-b", DependsOn: []string{"a"}},
						{Name: "c", KnightRef: knightName, Task: "task-c", DependsOn: []string{"a"}},
						{Name: "d", KnightRef: knightName, Task: "task-d", DependsOn: []string{"b", "c"}},
					},
				},
			}
			Expect(r.validateDAG(chain)).To(Succeed())
		})

		It("should reject references to unknown steps", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "a", KnightRef: knightName, Task: "task-a", DependsOn: []string{"nonexistent"}},
					},
				},
			}
			err := r.validateDAG(chain)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown step"))
		})
	})

	Context("Go Template Rendering", func() {
		It("should render templates with step outputs", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Input: "initial-data",
					Steps: []aiv1alpha1.ChainStep{
						{Name: "step1", KnightRef: knightName, Task: "first"},
						{Name: "step2", KnightRef: knightName, Task: `Analyze: {{ .Input }} and {{ index .Steps "step1" "Output" }}`},
					},
				},
				Status: aiv1alpha1.ChainStatus{
					StepStatuses: []aiv1alpha1.ChainStepStatus{
						{Name: "step1", Phase: aiv1alpha1.ChainStepPhaseSucceeded, Output: "step1-result"},
						{Name: "step2", Phase: aiv1alpha1.ChainStepPhasePending},
					},
				},
			}

			result, err := r.renderTemplate(chain, chain.Spec.Steps[1].Task)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(ContainSubstring("initial-data"))
			Expect(result).To(ContainSubstring("step1-result"))
		})

		It("should pass through non-template strings unchanged", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{Steps: []aiv1alpha1.ChainStep{{Name: "a"}}},
			}
			result, err := r.renderTemplate(chain, "plain task with no templates")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("plain task with no templates"))
		})
	})

	Context("Reconciliation", func() {
		BeforeEach(func() {
			createKnight(knightName, "security")
			createKnight(knightName2, "research")
		})

		AfterEach(func() {
			chain := &aiv1alpha1.Chain{}
			if err := k8sClient.Get(ctx, chainNN, chain); err == nil {
				k8sClient.Delete(ctx, chain)
			}
			for _, nn := range []types.NamespacedName{knightNN, knightNN2} {
				knight := &aiv1alpha1.Knight{}
				if err := k8sClient.Get(ctx, nn, knight); err == nil {
					k8sClient.Delete(ctx, knight)
				}
			}
		})

		It("should add finalizer and set Idle phase on first reconcile", func() {
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{Name: chainName, Namespace: namespace},
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "scan", KnightRef: knightName, Task: "scan the network"},
					},
					Timeout: 600,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			r := newReconciler()

			// First reconcile: adds finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile: sets initial status
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			Expect(err).NotTo(HaveOccurred())

			// Third reconcile: initializes step statuses and sets Idle
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			Expect(err).NotTo(HaveOccurred())

			updated := &aiv1alpha1.Chain{}
			Expect(k8sClient.Get(ctx, chainNN, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(chainFinalizer))
			Expect(updated.Status.Phase).To(Equal(aiv1alpha1.ChainPhaseIdle))
			Expect(updated.Status.StepStatuses).To(HaveLen(1))
			Expect(updated.Status.StepStatuses[0].Phase).To(Equal(aiv1alpha1.ChainStepPhasePending))
		})

		It("should reject a chain referencing a non-existent knight", func() {
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{Name: chainName, Namespace: namespace},
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "scan", KnightRef: "nonexistent-knight", Task: "scan"},
					},
					Timeout: 600,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			r := newReconciler()
			// Add finalizer first
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})

			// Validation should fail
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-existent knight"))
		})

		It("should handle suspended chains", func() {
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{Name: chainName, Namespace: namespace},
				Spec: aiv1alpha1.ChainSpec{
					Suspended: true,
					Steps: []aiv1alpha1.ChainStep{
						{Name: "scan", KnightRef: knightName, Task: "scan"},
					},
					Timeout: 600,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			r := newReconciler()
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			Expect(err).NotTo(HaveOccurred())

			updated := &aiv1alpha1.Chain{}
			Expect(k8sClient.Get(ctx, chainNN, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(aiv1alpha1.ChainPhaseSuspended))
		})

		It("should initialize step statuses for a fan-out DAG", func() {
			chain := &aiv1alpha1.Chain{
				ObjectMeta: metav1.ObjectMeta{Name: chainName, Namespace: namespace},
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "start", KnightRef: knightName, Task: "begin"},
						{Name: "branch-a", KnightRef: knightName, Task: "a", DependsOn: []string{"start"}},
						{Name: "branch-b", KnightRef: knightName2, Task: "b", DependsOn: []string{"start"}},
						{Name: "merge", KnightRef: knightName, Task: "merge", DependsOn: []string{"branch-a", "branch-b"}},
					},
					Timeout: 600,
				},
			}
			Expect(k8sClient.Create(ctx, chain)).To(Succeed())

			r := newReconciler()
			// Reconcile through finalizer + validation + init
			for i := 0; i < 4; i++ {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: chainNN})
			}

			updated := &aiv1alpha1.Chain{}
			Expect(k8sClient.Get(ctx, chainNN, updated)).To(Succeed())
			Expect(updated.Status.StepStatuses).To(HaveLen(4))
		})

		It("should handle not-found chain gracefully", func() {
			r := newReconciler()
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Step Status Initialization", func() {
		It("should set all steps to Pending", func() {
			r := newReconciler()
			chain := &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "a", KnightRef: knightName, Task: "task-a"},
						{Name: "b", KnightRef: knightName, Task: "task-b"},
					},
				},
			}
			r.initStepStatuses(chain)
			Expect(chain.Status.StepStatuses).To(HaveLen(2))
			Expect(chain.Status.StepStatuses[0].Name).To(Equal("a"))
			Expect(chain.Status.StepStatuses[0].Phase).To(Equal(aiv1alpha1.ChainStepPhasePending))
			Expect(chain.Status.StepStatuses[1].Name).To(Equal("b"))
			Expect(chain.Status.StepStatuses[1].Phase).To(Equal(aiv1alpha1.ChainStepPhasePending))
		})
	})
})
