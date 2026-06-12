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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
)

// sharedNixStorePVCName is the cluster-wide RWX PVC backing the shared Nix
// store. The build Job mounts it read-write; knight pods mount it read-only.
// Provisioned out-of-band via GitOps; the build feature is inert until it exists.
const sharedNixStorePVCName = "roundtable-nix-store"

// nixBuildJobTTL keeps finished build Jobs around briefly for log inspection
// before Kubernetes garbage-collects them.
const nixBuildJobTTL int32 = 3600

// reconcileNixBuildJob ensures the knight's flake tools are built into the
// shared Nix store. It is a no-op unless the shared store PVC exists, so the
// operator can ship this ahead of the PVC and activate automatically once
// GitOps creates it. Builds are gated on the nix-tools hash: a knight whose
// tool set is already published (status.nixToolsHash) triggers no Job.
func (r *KnightReconciler) reconcileNixBuildJob(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	hasNixTools := (knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0) || len(knight.Spec.NixPackages) > 0
	if !hasNixTools {
		return nil
	}

	// Cheap check first: nothing to do if the current tool set is already built.
	currentHash := knightpkg.NixToolsHash(knight)
	if knight.Status.NixToolsHash == currentHash {
		return nil // already published
	}

	// Feature gate: only act when the shared store exists.
	shared := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Name: sharedNixStorePVCName, Namespace: knight.Namespace}, shared); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // shared store not provisioned yet — legacy per-pod build still applies
		}
		return fmt.Errorf("shared Nix store lookup failed: %w", err)
	}

	jobName := fmt.Sprintf("%s-nixbuild-%s", knight.Name, currentHash)
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: knight.Namespace}, job)

	if apierrors.IsNotFound(err) {
		newJob := r.buildNixBuildJob(knight, currentHash, jobName)
		if err := controllerutil.SetControllerReference(knight, newJob, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, newJob); err != nil {
			return fmt.Errorf("nix build Job create failed: %w", err)
		}
		log.Info("Nix build Job created", "job", jobName, "toolsHash", currentHash)
		r.Recorder.Eventf(knight, corev1.EventTypeNormal, "NixBuildStarted", "Building Nix tools (hash %s) into shared store", currentHash)
		return nil
	} else if err != nil {
		return fmt.Errorf("nix build Job get failed: %w", err)
	}

	// Job exists — react to its terminal state.
	switch {
	case job.Status.Succeeded > 0:
		knight.Status.NixToolsHash = currentHash // persisted by updateStatus
		log.Info("Nix build Job succeeded", "job", jobName, "toolsHash", currentHash)
		r.Recorder.Eventf(knight, corev1.EventTypeNormal, "NixBuildComplete", "Nix tools (hash %s) published to shared store", currentHash)
	case job.Spec.BackoffLimit != nil && job.Status.Failed > *job.Spec.BackoffLimit:
		r.Recorder.Eventf(knight, corev1.EventTypeWarning, "NixBuildFailed", "Nix build Job %s failed; tools not published", jobName)
	}
	return nil
}

// buildNixBuildJob constructs the per-knight Nix build Job. It runs the
// pi-knight image's nix-build.sh with the shared store mounted RW and the
// knight's flake ConfigMap at /config. TMPDIR is an emptyDir so build scratch
// never touches the shared store.
func (r *KnightReconciler) buildNixBuildJob(knight *aiv1alpha1.Knight, hash, jobName string) *batchv1.Job {
	image := knight.Spec.Image
	if image == "" {
		image = r.DefaultImage
	}
	if image == "" {
		image = "ghcr.io/dapperdivers/pi-knight:latest"
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "knight-nixbuild",
		"app.kubernetes.io/instance":   knight.Name,
		"app.kubernetes.io/managed-by": "roundtable-operator",
		"roundtable.io/purpose":        "nix-build",
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: knight.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				nixToolsHashAnnotation: hash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(2)),
			TTLSecondsAfterFinished: ptr.To(nixBuildJobTTL),
			ActiveDeadlineSeconds:   ptr.To(int64(1800)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Shared with the knight pods (uid/gid/fsGroup + OnRootMismatch)
					// so the builder writes shared-store paths the read-only
					// knights can read, kubelet doesn't recursively chown the
					// large store on every mount, and non-root policy is met.
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext:              r.KnightSecurity.PodSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:    "nixbuild",
							Image:   image,
							Command: []string{"/app/scripts/nix-build.sh"},
							Env: []corev1.EnvVar{
								// Lowercase CR name — matches the profile key
								// the knight pod derives from KNIGHT_NAME.
								{Name: "KNIGHT_NAME", Value: knight.Name},
								{Name: "FLAKE_FILE", Value: "/config/flake.nix"},
								{Name: "TMPDIR", Value: "/scratch"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
								SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "nix", MountPath: "/nix"},
								{Name: "config", MountPath: "/config"},
								{Name: "scratch", MountPath: "/scratch"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "nix",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: sharedNixStorePVCName,
								},
							},
						},
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("knight-%s-config", knight.Name),
									},
								},
							},
						},
						{
							Name:         "scratch",
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
					},
				},
			},
		},
	}
}
