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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
)

const (
	specHashAnnotation     = "roundtable.io/spec-hash"
	nixToolsHashAnnotation = "roundtable.io/nix-tools-hash"
)

// PodSpecBuilder is a function that constructs a PodSpec for a Knight.
// This allows the controller to inject its own pod-building logic
// (using PodBuilder) without the runtime package depending on internal packages.
type PodSpecBuilder func(ctx context.Context, knight *aiv1alpha1.Knight) appsv1.DeploymentSpec

// DeploymentBackend implements RuntimeBackend using Kubernetes Deployments.
// This is the default "always-on" backend where each Knight runs as a Deployment.
type DeploymentBackend struct {
	Client       client.Client
	Scheme       *runtime.Scheme
	DefaultImage string

	// BuildDeploymentSpec is a pluggable function that constructs the full
	// DeploymentSpec for a Knight. The controller injects this to keep
	// PodBuilder usage in the controller layer.
	BuildDeploymentSpec PodSpecBuilder
}

// NewDeploymentBackend creates a new DeploymentBackend.
func NewDeploymentBackend(c client.Client, scheme *runtime.Scheme, defaultImage string, builder PodSpecBuilder) *DeploymentBackend {
	return &DeploymentBackend{
		Client:              c,
		Scheme:              scheme,
		DefaultImage:        defaultImage,
		BuildDeploymentSpec: builder,
	}
}

// Reconcile creates or updates the Deployment for the Knight.
func (b *DeploymentBackend) Reconcile(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	// Build the desired deployment spec using the injected builder
	desiredSpec := b.BuildDeploymentSpec(ctx, knight)

	labels := map[string]string{
		"app.kubernetes.io/name":       "knight",
		"app.kubernetes.io/instance":   knight.Name,
		"app.kubernetes.io/managed-by": "roundtable-operator",
		"roundtable.io/domain":         knight.Spec.Domain,
	}

	// Build a temporary deployment to compute the spec hash
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knight.Name,
			Namespace: knight.Namespace,
		},
	}
	desired.Spec = desiredSpec

	podAnnotations := map[string]string{
		"roundtable.io/model":  knight.Spec.Model,
		"roundtable.io/skills": strings.Join(knight.Spec.Skills, ","),
		"roundtable.io/domain": knight.Spec.Domain,
	}
	hasNixTools := (knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0) || len(knight.Spec.NixPackages) > 0
	if hasNixTools {
		podAnnotations[nixToolsHashAnnotation] = knightpkg.NixToolsHash(knight)
	}
	desired.Spec.Template.ObjectMeta.Annotations = podAnnotations

	desiredHash := knightpkg.DeploymentSpecHash(desired)

	// Fetch or create the deployment
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knight.Name,
			Namespace: knight.Namespace,
		},
	}

	replicas := int32(1)

	op, err := controllerutil.CreateOrUpdate(ctx, b.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(knight, deploy, b.Scheme); err != nil {
			return err
		}

		// Check if the spec hash matches — if so, skip mutation
		existingHash := ""
		if deploy.Spec.Template.Annotations != nil {
			existingHash = deploy.Spec.Template.Annotations[specHashAnnotation]
		}
		if existingHash == desiredHash {
			return nil
		}

		// Apply desired state
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
		}
		deploy.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: labels,
		}
		deploy.Spec.Template.ObjectMeta.Labels = labels

		podAnnotations[specHashAnnotation] = desiredHash
		deploy.Spec.Template.ObjectMeta.Annotations = podAnnotations

		deploy.Spec.Template.Spec = desiredSpec.Template.Spec

		return nil
	})

	if err != nil {
		return fmt.Errorf("deployment reconcile failed: %w", err)
	}

	log.Info("Deployment reconciled", "operation", op,
		"specImage", knight.Spec.Image,
		"defaultImage", b.DefaultImage,
		"resolvedImage", deploy.Spec.Template.Spec.Containers[0].Image)
	return nil
}

// Cleanup deletes the Deployment owned by the Knight.
// ConfigMaps and PVCs are handled by owner references (garbage collection).
func (b *DeploymentBackend) Cleanup(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get deployment for cleanup: %w", err)
	}

	if err := b.Client.Delete(ctx, deploy); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete deployment: %w", err)
	}

	log.Info("Deployment cleaned up", "knight", knight.Name)
	return nil
}

// IsReady checks if the Knight's Deployment has at least one ready replica.
func (b *DeploymentBackend) IsReady(ctx context.Context, knight *aiv1alpha1.Knight) (bool, error) {
	deploy := &appsv1.Deployment{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get deployment: %w", err)
	}
	return deploy.Status.ReadyReplicas >= 1, nil
}

// Suspend scales the Knight's Deployment to 0 replicas.
func (b *DeploymentBackend) Suspend(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get deployment for suspend: %w", err)
	}

	zero := int32(0)
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != zero {
		deploy.Spec.Replicas = &zero
		if err := b.Client.Update(ctx, deploy); err != nil {
			return fmt.Errorf("failed to scale deployment to 0: %w", err)
		}
		log.Info("Suspended knight — scaled to 0", "knight", knight.Name)
	}

	return nil
}

// Resume scales the Knight's Deployment to 1 replica.
func (b *DeploymentBackend) Resume(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	err := b.Client.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("deployment not found for resume: %s", knight.Name)
	}
	if err != nil {
		return fmt.Errorf("failed to get deployment for resume: %w", err)
	}

	one := int32(1)
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != one {
		deploy.Spec.Replicas = &one
		if err := b.Client.Update(ctx, deploy); err != nil {
			return fmt.Errorf("failed to scale deployment to 1: %w", err)
		}
		log.Info("Resumed knight — scaled to 1", "knight", knight.Name)
	}

	return nil
}
