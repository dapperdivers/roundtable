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

package runtime

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

// SandboxBackend implements RuntimeBackend using Kubernetes Agent Sandbox CRs.
// Each Knight is backed by a Sandbox (agents.x-k8s.io/v1alpha1) instead of a Deployment.
// This provides lifecycle management (pause/resume/hibernate) and strong isolation.
type SandboxBackend struct {
	Client       client.Client
	Scheme       *k8sruntime.Scheme
	DefaultImage string

	// BuildPodSpec is a pluggable function that constructs the PodSpec for a Knight.
	// The controller injects this to keep PodBuilder usage in the controller layer.
	BuildPodSpec func(ctx context.Context, knight *aiv1alpha1.Knight) corev1.PodSpec
}

// NewSandboxBackend creates a new SandboxBackend.
func NewSandboxBackend(c client.Client, scheme *k8sruntime.Scheme, defaultImage string, builder func(ctx context.Context, knight *aiv1alpha1.Knight) corev1.PodSpec) *SandboxBackend {
	return &SandboxBackend{
		Client:       c,
		Scheme:       scheme,
		DefaultImage: defaultImage,
		BuildPodSpec: builder,
	}
}

// Reconcile creates or updates the Sandbox CR for the Knight.
func (b *SandboxBackend) Reconcile(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	podSpec := b.BuildPodSpec(ctx, knight)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knight.Name,
			Namespace: knight.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, b.Client, sandbox, func() error {
		// Set owner reference so Sandbox is garbage-collected with Knight
		if err := controllerutil.SetControllerReference(knight, sandbox, b.Scheme); err != nil {
			return err
		}

		// Labels for identification
		if sandbox.Labels == nil {
			sandbox.Labels = make(map[string]string)
		}
		sandbox.Labels["app.kubernetes.io/name"] = "knight"
		sandbox.Labels["app.kubernetes.io/instance"] = knight.Name
		sandbox.Labels["app.kubernetes.io/managed-by"] = "roundtable-operator"
		sandbox.Labels["roundtable.io/domain"] = knight.Spec.Domain

		// Build Sandbox spec from Knight
		sandbox.Spec.PodTemplate = sandboxv1alpha1.PodTemplate{
			Spec: podSpec,
		}

		// Sandbox is a singleton — always 1 replica (or 0 when suspended)
		sandbox.Spec.Replicas = ptr.To(int32(1))

		// Map VolumeClaimTemplates from Knight workspace config
		sandbox.Spec.VolumeClaimTemplates = b.buildVolumeClaimTemplates(knight)

		return nil
	})

	if err != nil {
		return fmt.Errorf("sandbox reconcile failed: %w", err)
	}

	log.Info("Sandbox reconciled", "operation", op, "knight", knight.Name)
	return nil
}

// Cleanup deletes the Sandbox CR owned by the Knight.
func (b *SandboxBackend) Cleanup(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	sandbox := &sandboxv1alpha1.Sandbox{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, sandbox)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get sandbox for cleanup: %w", err)
	}

	if err := b.Client.Delete(ctx, sandbox); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete sandbox: %w", err)
	}

	log.Info("Sandbox cleaned up", "knight", knight.Name)
	return nil
}

// IsReady checks if the Knight's Sandbox has a ready pod.
func (b *SandboxBackend) IsReady(ctx context.Context, knight *aiv1alpha1.Knight) (bool, error) {
	sandbox := &sandboxv1alpha1.Sandbox{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, sandbox)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get sandbox: %w", err)
	}

	// Check the Ready condition on the Sandbox status
	readyCond := meta.FindStatusCondition(sandbox.Status.Conditions,
		string(sandboxv1alpha1.SandboxConditionReady))
	if readyCond != nil && readyCond.Status == metav1.ConditionTrue {
		return true, nil
	}

	// Fallback: check replicas
	return sandbox.Status.Replicas >= 1, nil
}

// Suspend scales the Knight's Sandbox to 0 replicas (pause).
func (b *SandboxBackend) Suspend(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	sandbox := &sandboxv1alpha1.Sandbox{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, sandbox)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get sandbox for suspend: %w", err)
	}

	if sandbox.Spec.Replicas == nil || *sandbox.Spec.Replicas != 0 {
		sandbox.Spec.Replicas = ptr.To(int32(0))
		if err := b.Client.Update(ctx, sandbox); err != nil {
			return fmt.Errorf("failed to scale sandbox to 0: %w", err)
		}
		log.Info("Suspended knight sandbox — scaled to 0", "knight", knight.Name)
	}

	return nil
}

// Resume scales the Knight's Sandbox to 1 replica.
func (b *SandboxBackend) Resume(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	sandbox := &sandboxv1alpha1.Sandbox{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, sandbox)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("sandbox not found for resume: %s", knight.Name)
	}
	if err != nil {
		return fmt.Errorf("failed to get sandbox for resume: %w", err)
	}

	if sandbox.Spec.Replicas == nil || *sandbox.Spec.Replicas != 1 {
		sandbox.Spec.Replicas = ptr.To(int32(1))
		if err := b.Client.Update(ctx, sandbox); err != nil {
			return fmt.Errorf("failed to scale sandbox to 1: %w", err)
		}
		log.Info("Resumed knight sandbox — scaled to 1", "knight", knight.Name)
	}

	return nil
}

// buildVolumeClaimTemplates constructs PVC templates for the Sandbox from Knight workspace config.
func (b *SandboxBackend) buildVolumeClaimTemplates(knight *aiv1alpha1.Knight) []sandboxv1alpha1.PersistentVolumeClaimTemplate {
	// If the knight uses an existing claim, no templates needed
	if knight.Spec.Workspace != nil && knight.Spec.Workspace.ExistingClaim != "" {
		return nil
	}

	// Build workspace PVC template if workspace is configured
	// The Sandbox controller will create and manage the PVC lifecycle
	// For now, return nil — workspace PVCs are managed by the Knight controller directly
	// This can be enhanced to use Sandbox-native storage in a future iteration
	return nil
}
