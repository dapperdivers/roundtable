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
				Name:      "knight-test-knight-workspace",
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

			// Check automount is disabled
			Expect(*deploy.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())
		})
	})
})
