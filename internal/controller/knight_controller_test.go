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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

var _ = Describe("Knight Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-knight"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Knight")
			knight := &aiv1alpha1.Knight{}
			err := k8sClient.Get(ctx, typeNamespacedName, knight)
			if err != nil && errors.IsNotFound(err) {
				resource := &aiv1alpha1.Knight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: aiv1alpha1.KnightSpec{
						Domain: "security",
						Model:  "claude-sonnet-4-20250514",
						Skills: []string{"security", "shared"},
						NATS: aiv1alpha1.KnightNATS{
							URL:           "nats://nats.test:4222",
							Subjects:      []string{"test.tasks.security.>"},
							Stream:        "test_tasks",
							ResultsStream: "test_results",
							MaxDeliver:    1,
						},
						Concurrency: 2,
						TaskTimeout: 120,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &aiv1alpha1.Knight{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance Knight")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &KnightReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the Knight has a finalizer")
			knight := &aiv1alpha1.Knight{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, knight)).To(Succeed())
			Expect(knight.Finalizers).To(ContainElement("ai.roundtable.io/finalizer"))
		})

		It("should create a ConfigMap with knight configuration", func() {
			controllerReconciler := &KnightReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Run reconciliation twice — first adds finalizer, second creates resources
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "knight-test-knight-config",
				Namespace: "default",
			}, cm)).To(Succeed())

			Expect(cm.Data["KNIGHT_SKILLS"]).To(Equal("security,shared"))
			Expect(cm.Labels["roundtable.io/domain"]).To(Equal("security"))
		})

		It("should create a PVC for the knight workspace", func() {
			controllerReconciler := &KnightReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-knight",
				Namespace: "default",
			}, pvc)).To(Succeed())
		})

		It("should create a Deployment with correct containers", func() {
			controllerReconciler := &KnightReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deploy)).To(Succeed())

			// Should have 2 containers: knight + skill-filter
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(2))
			Expect(deploy.Spec.Template.Spec.Containers[0].Name).To(Equal("app"))
			Expect(deploy.Spec.Template.Spec.Containers[1].Name).To(Equal("skill-filter"))

			// Check labels
			Expect(deploy.Labels["roundtable.io/domain"]).To(Equal("security"))

			// Check automount is enabled (knights may need in-cluster access)
			Expect(*deploy.Spec.Template.Spec.AutomountServiceAccountToken).To(BeTrue())
		})
	})

	Describe("deriveResultsPrefix", func() {
		It("returns empty string for empty subjects", func() {
			Expect(deriveResultsPrefix(nil)).To(Equal(""))
			Expect(deriveResultsPrefix([]string{})).To(Equal(""))
		})

		It("extracts table-prefix from tasks subject", func() {
			subjects := []string{"table-prefix.tasks.>"}
			Expect(deriveResultsPrefix(subjects)).To(Equal("table-prefix.results"))
		})

		It("extracts rt-dev prefix", func() {
			subjects := []string{"rt-dev.tasks.planning.>"}
			Expect(deriveResultsPrefix(subjects)).To(Equal("rt-dev.results"))
		})

		It("extracts chelonian prefix", func() {
			subjects := []string{"chelonian.tasks.mission.abc123"}
			Expect(deriveResultsPrefix(subjects)).To(Equal("chelonian.results"))
		})

		It("uses first valid subject with .tasks.", func() {
			subjects := []string{"bogus", "myfleet.tasks.something"}
			Expect(deriveResultsPrefix(subjects)).To(Equal("myfleet.results"))
		})

		It("falls back to dot-split for malformed subject without .tasks.", func() {
			subjects := []string{"something.else.entirely"}
			Expect(deriveResultsPrefix(subjects)).To(Equal("something.results"))
		})

		It("returns empty string for single-segment subject", func() {
			subjects := []string{"nope"}
			Expect(deriveResultsPrefix(subjects)).To(Equal(""))
		})
	})

	Describe("cleanupStaleRuntime", func() {
		var (
			ctx                context.Context
			reconciler         *KnightReconciler
			knightName         string
			knightNamespace    string
			typeNamespacedName types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &KnightReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			knightName = "test-runtime-transition"
			knightNamespace = "default"
			typeNamespacedName = types.NamespacedName{
				Name:      knightName,
				Namespace: knightNamespace,
			}
		})

		AfterEach(func() {
			// Clean up knight if it exists
			knight := &aiv1alpha1.Knight{}
			if err := k8sClient.Get(ctx, typeNamespacedName, knight); err == nil {
				_ = k8sClient.Delete(ctx, knight)
			}
			// Clean up deployment if it exists
			deploy := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, typeNamespacedName, deploy); err == nil {
				_ = k8sClient.Delete(ctx, deploy)
			}
		})

		It("deletes stale Deployment when runtime is sandbox", func() {
			// Create a Knight with default runtime (deployment)
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: knightNamespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "test",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"shared"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			// Create a Deployment manually (simulating previous reconcile)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: knightNamespace,
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "test"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  "test",
								Image: "test:latest",
							}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deploy)).To(Succeed())

			// Verify deployment exists
			deployCheck := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, deployCheck)).To(Succeed())

			// Switch knight to sandbox runtime
			knight.Spec.Runtime = "sandbox"

			// Call cleanupStaleRuntime
			err := reconciler.cleanupStaleRuntime(ctx, knight)
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was deleted
			deployAfter := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, typeNamespacedName, deployAfter)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("is idempotent when no stale resources exist", func() {
			// Create a Knight with default runtime
			knight := &aiv1alpha1.Knight{
				ObjectMeta: metav1.ObjectMeta{
					Name:      knightName,
					Namespace: knightNamespace,
				},
				Spec: aiv1alpha1.KnightSpec{
					Domain: "test",
					Model:  "claude-sonnet-4-20250514",
					Skills: []string{"shared"},
					NATS: aiv1alpha1.KnightNATS{
						URL:           "nats://nats.test:4222",
						Subjects:      []string{"test.tasks.>"},
						Stream:        "test_tasks",
						ResultsStream: "test_results",
					},
				},
			}
			Expect(k8sClient.Create(ctx, knight)).To(Succeed())

			// Call cleanupStaleRuntime without any stale resources
			err := reconciler.cleanupStaleRuntime(ctx, knight)
			Expect(err).NotTo(HaveOccurred())

			// Call again to verify idempotence
			err = reconciler.cleanupStaleRuntime(ctx, knight)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
