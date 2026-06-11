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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
)

var _ = Describe("Knight Nix build Job", func() {
	newKnight := func(name string, tools []string) *aiv1alpha1.Knight {
		return &aiv1alpha1.Knight{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: aiv1alpha1.KnightSpec{
				Domain: "security",
				Tools:  &aiv1alpha1.KnightTools{Nix: tools},
			},
		}
	}

	// ── buildNixBuildJob (pure) ───────────────────────────────────────────
	Describe("buildNixBuildJob", func() {
		It("mounts the shared store, knight ConfigMap, and a scratch emptyDir", func() {
			r := &KnightReconciler{DefaultImage: "ghcr.io/dapperdivers/pi-knight:test"}
			k := newKnight("galahad", []string{"nmap"})
			job := r.buildNixBuildJob(k, "abc123", "galahad-nixbuild-abc123")

			pod := job.Spec.Template.Spec
			Expect(pod.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
			Expect(pod.Containers).To(HaveLen(1))

			c := pod.Containers[0]
			Expect(c.Image).To(Equal("ghcr.io/dapperdivers/pi-knight:test"))
			Expect(c.Command).To(Equal([]string{"/app/scripts/nix-build.sh"}))

			// shared store mounted RW (no ReadOnly flag), config + scratch present
			mounts := map[string]corev1.VolumeMount{}
			for _, m := range c.VolumeMounts {
				mounts[m.Name] = m
			}
			Expect(mounts["nix"].MountPath).To(Equal("/nix"))
			Expect(mounts["nix"].ReadOnly).To(BeFalse())
			Expect(mounts["config"].MountPath).To(Equal("/config"))
			Expect(mounts["scratch"].MountPath).To(Equal("/scratch"))

			// KNIGHT_NAME is the lowercase CR name (profile key); TMPDIR off /nix
			env := map[string]string{}
			for _, e := range c.Env {
				env[e.Name] = e.Value
			}
			Expect(env["KNIGHT_NAME"]).To(Equal("galahad"))
			Expect(env["TMPDIR"]).To(Equal("/scratch"))

			// volumes wired to the shared PVC and the knight's config CM
			vols := map[string]corev1.Volume{}
			for _, v := range pod.Volumes {
				vols[v.Name] = v
			}
			Expect(vols["nix"].PersistentVolumeClaim.ClaimName).To(Equal(sharedNixStorePVCName))
			Expect(vols["config"].ConfigMap.Name).To(Equal("knight-galahad-config"))
			Expect(vols["scratch"].EmptyDir).NotTo(BeNil())
		})
	})

	// ── reconcileNixBuildJob (envtest) ────────────────────────────────────
	Describe("reconcileNixBuildJob", func() {
		var r *KnightReconciler

		BeforeEach(func() {
			r = &KnightReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Recorder:     record.NewFakeRecorder(10),
				DefaultImage: "ghcr.io/dapperdivers/pi-knight:test",
			}
		})

		It("is a no-op when the shared store PVC is absent", func() {
			k := newKnight("gawain", []string{"jq"})
			Expect(r.reconcileNixBuildJob(ctx, k)).To(Succeed())

			jobs := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobs, client.InNamespace("default"))).To(Succeed())
			for _, j := range jobs.Items {
				Expect(j.Labels["app.kubernetes.io/instance"]).NotTo(Equal("gawain"))
			}
		})

		It("creates a build Job once the shared store exists", func() {
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: sharedNixStorePVCName, Namespace: "default"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, pvc) }()

			// The knight need not be persisted — reconcileNixBuildJob works on
			// the in-memory object and only creates the Job. A UID makes the
			// owner reference well-formed.
			k := newKnight("percival", []string{"nmap", "jq"})
			k.UID = "percival-uid"

			Expect(r.reconcileNixBuildJob(ctx, k)).To(Succeed())

			hash := knightpkg.NixToolsHash(k)
			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "percival-nixbuild-" + hash,
				Namespace: "default",
			}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(ContainElement("/app/scripts/nix-build.sh"))
		})

		It("does nothing when the published hash already matches", func() {
			k := newKnight("kay", []string{"curl"})
			k.Status.NixToolsHash = knightpkg.NixToolsHash(k)
			// No PVC needed — the hash-match short-circuits before the PVC check
			Expect(r.reconcileNixBuildJob(ctx, k)).To(Succeed())
		})
	})
})
