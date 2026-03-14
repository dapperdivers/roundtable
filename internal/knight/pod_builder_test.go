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

package knight

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

func TestKnight(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Knight Package Suite")
}

var _ = Describe("PodBuilder", func() {
	var knight *aiv1alpha1.Knight
	var builder *PodBuilder

	BeforeEach(func() {
		knight = &aiv1alpha1.Knight{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-knight",
				Namespace: "default",
			},
			Spec: aiv1alpha1.KnightSpec{
				Model:       "anthropic/claude-3-5-sonnet-20241022",
				Domain:      "testing",
				Concurrency: 1,
				TaskTimeout: 300,
				Skills:      []string{"testing", "validation"},
				NATS: aiv1alpha1.KnightNATS{
					URL:           "nats://nats.default.svc:4222",
					Stream:        "test_tasks",
					ResultsStream: "test_results",
					Subjects:      []string{"test.tasks.testing.>"},
				},
			},
		}
		builder = NewPodBuilder(knight, "ghcr.io/dapperdivers/pi-knight:latest")
	})

	Describe("WithWorkspace", func() {
		It("adds workspace PVC volume and mount", func() {
			builder.WithWorkspace()

			Expect(builder.volumes).To(HaveLen(1))
			Expect(builder.volumes[0].Name).To(Equal("data"))
			Expect(builder.volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("test-knight"))

			Expect(builder.mounts).To(HaveLen(1))
			Expect(builder.mounts[0].Name).To(Equal("data"))
			Expect(builder.mounts[0].MountPath).To(Equal("/data"))
		})

		It("uses existing claim if specified", func() {
			knight.Spec.Workspace = &aiv1alpha1.KnightWorkspace{
				ExistingClaim: "custom-pvc",
			}
			builder.WithWorkspace()

			Expect(builder.volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("custom-pvc"))
		})
	})

	Describe("WithConfig", func() {
		It("adds config ConfigMap volume and mount", func() {
			builder.WithConfig("knight-test-knight-config")

			Expect(builder.volumes).To(HaveLen(1))
			Expect(builder.volumes[0].Name).To(Equal("config"))
			Expect(builder.volumes[0].ConfigMap.Name).To(Equal("knight-test-knight-config"))

			Expect(builder.mounts).To(HaveLen(1))
			Expect(builder.mounts[0].Name).To(Equal("config"))
			Expect(builder.mounts[0].MountPath).To(Equal("/config"))
			Expect(builder.mounts[0].ReadOnly).To(BeTrue())
		})
	})

	Describe("WithNixStore", func() {
		It("does nothing when no Nix tools configured", func() {
			builder.WithNixStore()
			Expect(builder.volumes).To(BeEmpty())
			Expect(builder.mounts).To(BeEmpty())
		})

		It("adds Nix PVC when tools.nix is configured", func() {
			knight.Spec.Tools = &aiv1alpha1.KnightTools{
				Nix: []string{"kubectl", "helm"},
			}
			builder.WithNixStore()

			Expect(builder.volumes).To(HaveLen(1))
			Expect(builder.volumes[0].Name).To(Equal("nix"))
			Expect(builder.volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("knight-test-knight-nix"))

			Expect(builder.mounts).To(HaveLen(1))
			Expect(builder.mounts[0].Name).To(Equal("nix"))
			Expect(builder.mounts[0].MountPath).To(Equal("/nix"))
		})
	})

	Describe("WithVault", func() {
		It("does nothing when vault not configured", func() {
			builder.WithVault()
			Expect(builder.volumes).To(BeEmpty())
			Expect(builder.mounts).To(BeEmpty())
		})

		It("adds read-only vault mount", func() {
			knight.Spec.Vault = &aiv1alpha1.KnightVault{
				ClaimName: "my-vault",
				ReadOnly:  true,
			}
			builder.WithVault()

			Expect(builder.volumes).To(HaveLen(1))
			Expect(builder.volumes[0].Name).To(Equal("vault"))
			Expect(builder.volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("my-vault"))
			Expect(builder.volumes[0].PersistentVolumeClaim.ReadOnly).To(BeTrue())

			Expect(builder.mounts).To(HaveLen(1))
			Expect(builder.mounts[0].MountPath).To(Equal("/vault"))
			Expect(builder.mounts[0].ReadOnly).To(BeTrue())
		})

		It("adds writable subpaths correctly", func() {
			knight.Spec.Vault = &aiv1alpha1.KnightVault{
				ClaimName: "my-vault",
				ReadOnly:  true,
				WritablePaths: []string{"logs/", "temp"},
			}
			builder.WithVault()

			// PVC must be non-readonly when writable paths exist
			Expect(builder.volumes[0].PersistentVolumeClaim.ReadOnly).To(BeFalse())

			// Should have 3 mounts: base + 2 writable subpaths
			Expect(builder.mounts).To(HaveLen(3))

			// Base mount is still read-only
			Expect(builder.mounts[0].MountPath).To(Equal("/vault"))
			Expect(builder.mounts[0].ReadOnly).To(BeTrue())

			// Writable subpaths
			Expect(builder.mounts[1].MountPath).To(Equal("/vault/logs"))
			Expect(builder.mounts[1].SubPath).To(Equal("logs"))
			Expect(builder.mounts[1].ReadOnly).To(BeFalse())

			Expect(builder.mounts[2].MountPath).To(Equal("/vault/temp"))
			Expect(builder.mounts[2].SubPath).To(Equal("temp"))
			Expect(builder.mounts[2].ReadOnly).To(BeFalse())
		})

		It("uses default vault claim name", func() {
			knight.Spec.Vault = &aiv1alpha1.KnightVault{
				ReadOnly: true,
			}
			builder.WithVault()

			Expect(builder.volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("obsidian-vault"))
		})
	})

	Describe("WithArsenal", func() {
		It("adds arsenal and skills emptyDir volumes", func() {
			builder.WithArsenal()

			Expect(builder.volumes).To(HaveLen(2))
			Expect(builder.volumes[0].Name).To(Equal("arsenal"))
			Expect(builder.volumes[0].EmptyDir).NotTo(BeNil())
			Expect(builder.volumes[1].Name).To(Equal("skills"))
			Expect(builder.volumes[1].EmptyDir).NotTo(BeNil())

			Expect(builder.mounts).To(HaveLen(2))
			Expect(builder.mounts[0].Name).To(Equal("arsenal"))
			Expect(builder.mounts[0].MountPath).To(Equal("/arsenal"))
			Expect(builder.mounts[0].ReadOnly).To(BeTrue())
			Expect(builder.mounts[1].Name).To(Equal("skills"))
			Expect(builder.mounts[1].MountPath).To(Equal("/skills"))
			Expect(builder.mounts[1].ReadOnly).To(BeTrue())
		})
	})

	Describe("WithSkillFilter", func() {
		It("adds skill-filter sidecar container", func() {
			builder.WithSkillFilter()

			Expect(builder.sidecars).To(HaveLen(1))
			Expect(builder.sidecars[0].Name).To(Equal("skill-filter"))
			Expect(builder.sidecars[0].Image).To(Equal("alpine:3.21"))

			// Check it has the right environment variable
			Expect(builder.sidecars[0].Env).To(ContainElement(
				corev1.EnvVar{Name: "SKILL_CATEGORIES", Value: "testing validation"},
			))

			// Check it mounts arsenal and skills
			Expect(builder.sidecars[0].VolumeMounts).To(HaveLen(2))
			Expect(builder.sidecars[0].VolumeMounts[0].Name).To(Equal("arsenal"))
			Expect(builder.sidecars[0].VolumeMounts[1].Name).To(Equal("skills"))
		})
	})

	Describe("WithGitSync", func() {
		It("does nothing when arsenal not configured", func() {
			builder.WithGitSync()
			Expect(builder.sidecars).To(BeEmpty())
		})

		It("adds git-sync sidecar with defaults", func() {
			knight.Spec.Arsenal = &aiv1alpha1.KnightArsenal{}
			builder.WithGitSync()

			Expect(builder.sidecars).To(HaveLen(1))
			Expect(builder.sidecars[0].Name).To(Equal("git-sync"))
			Expect(builder.sidecars[0].Image).To(Equal("registry.k8s.io/git-sync/git-sync:v4.4.0"))

			// Check default env vars
			envMap := make(map[string]string)
			for _, e := range builder.sidecars[0].Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["GITSYNC_REPO"]).To(Equal("https://github.com/dapperdivers/roundtable-arsenal"))
			Expect(envMap["GITSYNC_REF"]).To(Equal("main"))
			Expect(envMap["GITSYNC_ROOT"]).To(Equal("/arsenal"))
			Expect(envMap["GITSYNC_PERIOD"]).To(Equal("300s"))
		})

		It("uses custom arsenal configuration", func() {
			knight.Spec.Arsenal = &aiv1alpha1.KnightArsenal{
				Repo:   "https://github.com/custom/arsenal",
				Ref:    "develop",
				Period: "60s",
				Image:  "custom/git-sync:v1.0",
			}
			builder.WithGitSync()

			envMap := make(map[string]string)
			for _, e := range builder.sidecars[0].Env {
				envMap[e.Name] = e.Value
			}
			Expect(builder.sidecars[0].Image).To(Equal("custom/git-sync:v1.0"))
			Expect(envMap["GITSYNC_REPO"]).To(Equal("https://github.com/custom/arsenal"))
			Expect(envMap["GITSYNC_REF"]).To(Equal("develop"))
			Expect(envMap["GITSYNC_PERIOD"]).To(Equal("60s"))
		})
	})

	Describe("Build", func() {
		It("creates a valid PodSpec with security context", func() {
			builder.WithWorkspace().WithConfig("test-config")
			spec := builder.Build(context.Background())

			// Check security context
			Expect(spec.SecurityContext).NotTo(BeNil())
			Expect(*spec.SecurityContext.RunAsUser).To(Equal(int64(1000)))
			Expect(*spec.SecurityContext.RunAsGroup).To(Equal(int64(1000)))
			Expect(*spec.SecurityContext.FSGroup).To(Equal(int64(1000)))
			Expect(*spec.SecurityContext.FSGroupChangePolicy).To(Equal(corev1.FSGroupChangeOnRootMismatch))

			// Check service links disabled
			Expect(*spec.EnableServiceLinks).To(BeFalse())

			// Check automount service account token
			Expect(*spec.AutomountServiceAccountToken).To(BeTrue())
		})

		It("creates main container with proper configuration", func() {
			builder.WithWorkspace()
			spec := builder.Build(context.Background())

			Expect(spec.Containers).To(HaveLen(1))
			mainContainer := spec.Containers[0]

			Expect(mainContainer.Name).To(Equal("app"))
			Expect(mainContainer.Image).To(Equal("ghcr.io/dapperdivers/pi-knight:latest"))

			// Check env vars
			envMap := make(map[string]string)
			for _, e := range mainContainer.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["KNIGHT_NAME"]).To(Equal("Test-knight"))
			Expect(envMap["KNIGHT_MODEL"]).To(Equal("anthropic/claude-3-5-sonnet-20241022"))
			Expect(envMap["NATS_URL"]).To(Equal("nats://nats.default.svc:4222"))
			Expect(envMap["MAX_CONCURRENT_TASKS"]).To(Equal("1"))
			Expect(envMap["TASK_TIMEOUT_MS"]).To(Equal("300000"))

			// Check probes
			Expect(mainContainer.StartupProbe).NotTo(BeNil())
			Expect(mainContainer.LivenessProbe).NotTo(BeNil())
			Expect(mainContainer.ReadinessProbe).NotTo(BeNil())
			Expect(mainContainer.StartupProbe.FailureThreshold).To(Equal(int32(60)))
		})

		It("uses custom image if specified", func() {
			knight.Spec.Image = "custom/knight:v1.0"
			spec := builder.Build(context.Background())

			Expect(spec.Containers[0].Image).To(Equal("custom/knight:v1.0"))
		})

		It("includes sidecars when configured", func() {
			builder.WithArsenal().WithSkillFilter()
			knight.Spec.Arsenal = &aiv1alpha1.KnightArsenal{}
			builder.WithGitSync()

			spec := builder.Build(context.Background())

			// Should have main + skill-filter + git-sync
			Expect(spec.Containers).To(HaveLen(3))
			Expect(spec.Containers[0].Name).To(Equal("app"))
			Expect(spec.Containers[1].Name).To(Equal("skill-filter"))
			Expect(spec.Containers[2].Name).To(Equal("git-sync"))
		})

		It("includes all volumes from With* methods", func() {
			builder.
				WithWorkspace().
				WithConfig("test-config").
				WithArsenal()

			knight.Spec.Tools = &aiv1alpha1.KnightTools{Nix: []string{"kubectl"}}
			builder.WithNixStore()

			spec := builder.Build(context.Background())

			// data, config, arsenal, skills, nix
			Expect(spec.Volumes).To(HaveLen(5))

			volumeNames := make([]string, len(spec.Volumes))
			for i, v := range spec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ConsistOf("data", "config", "arsenal", "skills", "nix"))
		})
	})
})
