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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/util"
)

// PodBuilder provides a composable way to build Knight pod specs.
// Each With* method adds its own volumes, mounts, and/or containers.
type PodBuilder struct {
	knight     *aiv1alpha1.Knight
	volumes    []corev1.Volume
	mounts     []corev1.VolumeMount
	sidecars   []corev1.Container
	env        []corev1.EnvVar
	defaultImg string
	reader     client.Reader
}

// NewPodBuilder creates a new PodBuilder for the given Knight.
func NewPodBuilder(k *aiv1alpha1.Knight, defaultImage string) *PodBuilder {
	return &PodBuilder{
		knight:     k,
		volumes:    []corev1.Volume{},
		mounts:     []corev1.VolumeMount{},
		sidecars:   []corev1.Container{},
		env:        []corev1.EnvVar{},
		defaultImg: defaultImage,
	}
}

// WithReader sets the client reader for looking up resources.
func (b *PodBuilder) WithReader(r client.Reader) *PodBuilder {
	b.reader = r
	return b
}

// WithWorkspace adds the workspace PVC mount at /data.
func (b *PodBuilder) WithWorkspace() *PodBuilder {
	pvcName := b.knight.Name
	if b.knight.Spec.Workspace != nil && b.knight.Spec.Workspace.ExistingClaim != "" {
		pvcName = b.knight.Spec.Workspace.ExistingClaim
	}

	b.volumes = append(b.volumes, corev1.Volume{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	b.mounts = append(b.mounts, corev1.VolumeMount{
		Name:      "data",
		MountPath: "/data",
	})
	return b
}

// WithConfig adds the config ConfigMap mount at /config.
func (b *PodBuilder) WithConfig(configMapName string) *PodBuilder {
	b.volumes = append(b.volumes, corev1.Volume{
		Name: "config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
			},
		},
	})
	b.mounts = append(b.mounts, corev1.VolumeMount{
		Name:      "config",
		MountPath: "/config",
		ReadOnly:  true,
	})
	return b
}

// WithNixStore adds the Nix PVC mount at /nix if tools.nix is configured.
func (b *PodBuilder) WithNixStore() *PodBuilder {
	if b.knight.Spec.Tools != nil && len(b.knight.Spec.Tools.Nix) > 0 {
		nixPVCName := fmt.Sprintf("knight-%s-nix", b.knight.Name)
		b.volumes = append(b.volumes, corev1.Volume{
			Name: "nix",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: nixPVCName,
				},
			},
		})
		b.mounts = append(b.mounts, corev1.VolumeMount{
			Name:      "nix",
			MountPath: "/nix",
		})
	}
	return b
}

// WithVault adds the vault PVC with optional writable subpaths.
func (b *PodBuilder) WithVault() *PodBuilder {
	if b.knight.Spec.Vault == nil {
		return b
	}

	claimName := b.knight.Spec.Vault.ClaimName
	if claimName == "" {
		claimName = "obsidian-vault"
	}

	// PVC must be ReadOnly=false when writablePaths exist
	pvcReadOnly := b.knight.Spec.Vault.ReadOnly
	if len(b.knight.Spec.Vault.WritablePaths) > 0 {
		pvcReadOnly = false
	}

	b.volumes = append(b.volumes, corev1.Volume{
		Name: "vault",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
				ReadOnly:  pvcReadOnly,
			},
		},
	})

	// Base mount
	b.mounts = append(b.mounts, corev1.VolumeMount{
		Name:      "vault",
		MountPath: "/vault",
		ReadOnly:  b.knight.Spec.Vault.ReadOnly,
	})

	// Writable subpaths override the read-only base
	for _, wp := range b.knight.Spec.Vault.WritablePaths {
		b.mounts = append(b.mounts, corev1.VolumeMount{
			Name:      "vault",
			MountPath: fmt.Sprintf("/vault/%s", strings.TrimSuffix(wp, "/")),
			SubPath:   strings.TrimSuffix(wp, "/"),
			ReadOnly:  false,
		})
	}

	return b
}

// WithSharedWorkspace adds the RoundTable shared workspace PVC if configured.
func (b *PodBuilder) WithSharedWorkspace(ctx context.Context) *PodBuilder {
	if b.reader == nil {
		return b
	}

	tableName, ok := b.knight.Labels["ai.roundtable.io/table"]
	if !ok {
		return b
	}

	rt := &aiv1alpha1.RoundTable{}
	if err := b.reader.Get(ctx, types.NamespacedName{
		Name:      tableName,
		Namespace: b.knight.Namespace,
	}, rt); err != nil {
		return b
	}

	if rt.Spec.SharedWorkspace == nil {
		return b
	}

	mountPath := rt.Spec.SharedWorkspace.MountPath
	if mountPath == "" {
		mountPath = "/shared"
	}

	b.volumes = append(b.volumes, corev1.Volume{
		Name: "shared-workspace",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: rt.Spec.SharedWorkspace.ClaimName,
			},
		},
	})
	b.mounts = append(b.mounts, corev1.VolumeMount{
		Name:      "shared-workspace",
		MountPath: mountPath,
	})

	return b
}

// WithArsenal adds emptyDir volumes for git-sync and skills.
func (b *PodBuilder) WithArsenal() *PodBuilder {
	b.volumes = append(b.volumes,
		corev1.Volume{
			Name:         "arsenal",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		corev1.Volume{
			Name:         "skills",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	)
	b.mounts = append(b.mounts,
		corev1.VolumeMount{Name: "arsenal", MountPath: "/arsenal", ReadOnly: true},
		corev1.VolumeMount{Name: "skills", MountPath: "/skills", ReadOnly: true},
	)
	return b
}

// WithSkillFilter adds the skill-filter sidecar container.
func (b *PodBuilder) WithSkillFilter() *PodBuilder {
	skillCategories := strings.Join(b.knight.Spec.Skills, " ")

	// Arsenal path: git-sync creates /arsenal/<repo-name> symlink
	arsenalPath := "/arsenal"
	if b.knight.Spec.Arsenal != nil {
		repo := b.knight.Spec.Arsenal.Repo
		if repo == "" {
			repo = "https://github.com/dapperdivers/roundtable-arsenal"
		}
		parts := strings.Split(strings.TrimSuffix(repo, ".git"), "/")
		arsenalPath = "/arsenal/" + parts[len(parts)-1]
	}

	skillFilterScript := fmt.Sprintf(`
ARSENAL="%s"
TARGET="/skills"
SKILL_CATEGORIES="%s"`, arsenalPath, skillCategories) + `
EXPECTED=$(echo $SKILL_CATEGORIES | wc -w)
LINKED=0
while [ "$LINKED" -lt "$EXPECTED" ]; do
  LINKED=0
  if [ -d "$ARSENAL" ]; then
    for cat in $SKILL_CATEGORIES; do
      src="$ARSENAL/$cat"
      dst="$TARGET/$cat"
      if [ -d "$src" ] && [ ! -L "$dst" ]; then
        ln -sf "$src" "$dst"
        echo "Linked $cat"
      fi
      [ -L "$dst" ] && LINKED=$((LINKED + 1))
    done
  fi
  [ "$LINKED" -lt "$EXPECTED" ] && sleep 2
done
echo "All categories linked ($LINKED/$EXPECTED)"
while true; do
  if [ -d "$ARSENAL" ]; then
    for cat in $SKILL_CATEGORIES; do
      src="$ARSENAL/$cat"
      dst="$TARGET/$cat"
      if [ -d "$src" ]; then
        current=$(readlink "$dst" 2>/dev/null || echo "")
        if [ "$current" != "$src" ]; then
          ln -sf "$src" "$dst"
          echo "Re-linked $cat"
        fi
      fi
    done
  fi
  sleep 60
done`

	skillFilterContainer := corev1.Container{
		Name:    "skill-filter",
		Image:   "alpine:3.21",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{skillFilterScript},
		Env: []corev1.EnvVar{
			{Name: "SKILL_CATEGORIES", Value: skillCategories},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("8Mi"),
				corev1.ResourceCPU:    resource.MustParse("5m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("16Mi"),
				corev1.ResourceCPU:    resource.MustParse("50m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "arsenal", MountPath: "/arsenal", ReadOnly: true},
			{Name: "skills", MountPath: "/skills"},
		},
	}

	b.sidecars = append(b.sidecars, skillFilterContainer)
	return b
}

// WithGitSync adds the git-sync sidecar container for the arsenal.
func (b *PodBuilder) WithGitSync() *PodBuilder {
	if b.knight.Spec.Arsenal == nil {
		return b
	}

	arsenalRepo := b.knight.Spec.Arsenal.Repo
	if arsenalRepo == "" {
		arsenalRepo = "https://github.com/dapperdivers/roundtable-arsenal"
	}

	arsenalRef := b.knight.Spec.Arsenal.Ref
	if arsenalRef == "" {
		arsenalRef = "main"
	}

	arsenalPeriod := b.knight.Spec.Arsenal.Period
	if arsenalPeriod == "" {
		arsenalPeriod = "300s"
	}

	arsenalImage := b.knight.Spec.Arsenal.Image
	if arsenalImage == "" {
		arsenalImage = "registry.k8s.io/git-sync/git-sync:v4.4.0"
	}

	gitSyncContainer := corev1.Container{
		Name:  "git-sync",
		Image: arsenalImage,
		Env: []corev1.EnvVar{
			{Name: "GITSYNC_REPO", Value: arsenalRepo},
			{Name: "GITSYNC_REF", Value: arsenalRef},
			{Name: "GITSYNC_ROOT", Value: "/arsenal"},
			{Name: "GITSYNC_PERIOD", Value: arsenalPeriod},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("32Mi"),
				corev1.ResourceCPU:    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "arsenal", MountPath: "/arsenal"},
		},
	}

	b.sidecars = append(b.sidecars, gitSyncContainer)
	return b
}

// Build assembles the complete PodSpec with all configured components.
func (b *PodBuilder) Build(ctx context.Context) corev1.PodSpec {
	// Determine image
	image := b.knight.Spec.Image
	if image == "" {
		image = b.defaultImg
	}
	if image == "" {
		image = "ghcr.io/dapperdivers/pi-knight:latest"
	}

	// Build environment variables
	taskTimeoutMs := int64(b.knight.Spec.TaskTimeout) * 1000
	env := []corev1.EnvVar{
		{Name: "KNIGHT_NAME", Value: util.Capitalize(b.knight.Name)},
		{Name: "KNIGHT_MODEL", Value: b.knight.Spec.Model},
		{Name: "NATS_URL", Value: b.knight.Spec.NATS.URL},
		{Name: "NATS_TASKS_STREAM", Value: b.knight.Spec.NATS.Stream},
		{Name: "NATS_RESULTS_STREAM", Value: b.knight.Spec.NATS.ResultsStream},
		{Name: "NATS_RESULTS_PREFIX", Value: DeriveResultsPrefix(b.knight.Spec.NATS.Subjects)},
		{Name: "SUBSCRIBE_TOPICS", Value: strings.Join(b.knight.Spec.NATS.Subjects, ",")},
		{Name: "MAX_CONCURRENT_TASKS", Value: fmt.Sprintf("%d", b.knight.Spec.Concurrency)},
		{Name: "TASK_TIMEOUT_MS", Value: fmt.Sprintf("%d", taskTimeoutMs)},
		{Name: "METRICS_PORT", Value: "3000"},
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "TZ", Value: "America/Chicago"},
	}

	// Append user-defined env vars
	env = append(env, b.knight.Spec.Env...)
	env = append(env, b.env...)

	// Main knight container
	probePort := 3000
	knightContainer := corev1.Container{
		Name:    "app",
		Image:   image,
		Env:     env,
		EnvFrom: b.knight.Spec.EnvFrom,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
		},
		VolumeMounts: b.mounts,
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: util.IntstrPort(probePort),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			FailureThreshold:    60, // 10 minutes for Nix builds
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: util.IntstrPort(probePort),
				},
			},
			PeriodSeconds: 30,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/ready",
					Port: util.IntstrPort(probePort),
				},
			},
			PeriodSeconds: 15,
		},
	}

	// Combine main container with sidecars
	containers := []corev1.Container{knightContainer}
	containers = append(containers, b.sidecars...)

	// Pod security context
	fsGroup := int64(1000)
	runAsUser := int64(1000)
	runAsGroup := int64(1000)

	return corev1.PodSpec{
		Containers:         containers,
		Volumes:            b.volumes,
		EnableServiceLinks: util.BoolPtr(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:           &runAsUser,
			RunAsGroup:          &runAsGroup,
			FSGroup:             &fsGroup,
			FSGroupChangePolicy: util.FSGroupChangePolicyPtr(corev1.FSGroupChangeOnRootMismatch),
		},
		ServiceAccountName:           b.knight.Spec.ServiceAccountName,
		AutomountServiceAccountToken: util.BoolPtr(true),
	}
}

// DeriveResultsPrefix extracts the NATS subject prefix for results from task subjects.
// e.g., ["table-prefix.tasks.security.>"] → "table-prefix.results"
func DeriveResultsPrefix(subjects []string) string {
	for _, subj := range subjects {
		if strings.Contains(subj, ".tasks.") {
			parts := strings.SplitN(subj, ".tasks.", 2)
			if len(parts) == 2 {
				return parts[0] + ".results"
			}
		}
	}
	// Fallback: use first segment
	for _, subj := range subjects {
		parts := strings.Split(subj, ".")
		if len(parts) > 1 {
			return parts[0] + ".results"
		}
	}
	return ""
}
