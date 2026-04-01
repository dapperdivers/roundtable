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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

// newSandboxTestScheme builds a scheme including sandbox types.
func newSandboxTestScheme(t *testing.T) *k8sruntime.Scheme {
	t.Helper()
	s := k8sruntime.NewScheme()
	require.NoError(t, aiv1alpha1.AddToScheme(s))
	require.NoError(t, sandboxv1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

// stubSandboxPodSpecBuilder returns a minimal PodSpec for testing.
func stubSandboxPodSpecBuilder(_ context.Context, knight *aiv1alpha1.Knight) corev1.PodSpec {
	image := knight.Spec.Image
	if image == "" {
		image = "ghcr.io/dapperdivers/openclaw-knight:latest"
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "knight",
				Image: image,
			},
		},
	}
}

func TestNewSandboxBackend(t *testing.T) {
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default-image:latest", stubSandboxPodSpecBuilder)

	assert.NotNil(t, b)
	assert.Equal(t, "default-image:latest", b.DefaultImage)
	assert.NotNil(t, b.BuildPodSpec)
	assert.NotNil(t, b.Client)
	assert.NotNil(t, b.Scheme)
}

func TestSandboxBackend_Reconcile_CreatesSandbox(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")

	err := b.Reconcile(ctx, knight)
	require.NoError(t, err)

	sandbox := &sandboxv1alpha1.Sandbox{}
	err = c.Get(ctx, types.NamespacedName{Name: "gawain", Namespace: "ai"}, sandbox)
	require.NoError(t, err)

	assert.Equal(t, "gawain", sandbox.Name)
	assert.Equal(t, "ai", sandbox.Namespace)
	assert.Equal(t, int32(1), *sandbox.Spec.Replicas)
	assert.Equal(t, "knight", sandbox.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "gawain", sandbox.Labels["app.kubernetes.io/instance"])
	assert.Equal(t, "roundtable-operator", sandbox.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "security", sandbox.Labels["roundtable.io/domain"])
	assert.Equal(t, "ghcr.io/dapperdivers/openclaw-knight:v1", sandbox.Spec.PodTemplate.Spec.Containers[0].Image)
}

func TestSandboxBackend_Cleanup_DeletesSandbox(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "gawain", Namespace: "ai"},
		Spec: sandboxv1alpha1.SandboxSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sandbox).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	err := b.Cleanup(ctx, knight)
	require.NoError(t, err)

	err = c.Get(ctx, types.NamespacedName{Name: "gawain", Namespace: "ai"}, &sandboxv1alpha1.Sandbox{})
	assert.True(t, err != nil, "sandbox should be deleted")
}

func TestSandboxBackend_Cleanup_NotFoundIsOK(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("ghost", "ai")
	err := b.Cleanup(ctx, knight)
	assert.NoError(t, err)
}

func TestSandboxBackend_IsReady_TrueWithReadyCondition(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "gawain", Namespace: "ai"},
		Spec: sandboxv1alpha1.SandboxSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
		Status: sandboxv1alpha1.SandboxStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(sandboxv1alpha1.SandboxConditionReady),
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sandbox).WithStatusSubresource(sandbox).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	ready, err := b.IsReady(ctx, knight)
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestSandboxBackend_IsReady_FalseWhenNotFound(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("ghost", "ai")
	ready, err := b.IsReady(ctx, knight)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestSandboxBackend_IsReady_FallbackToReplicas(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "gawain", Namespace: "ai"},
		Spec: sandboxv1alpha1.SandboxSpec{
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
		Status: sandboxv1alpha1.SandboxStatus{
			Replicas: 1,
			// No Ready condition set
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sandbox).WithStatusSubresource(sandbox).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	ready, err := b.IsReady(ctx, knight)
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestSandboxBackend_Suspend_ScalesToZero(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "gawain", Namespace: "ai"},
		Spec: sandboxv1alpha1.SandboxSpec{
			Replicas: ptr.To(int32(1)),
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sandbox).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	err := b.Suspend(ctx, knight)
	require.NoError(t, err)

	updated := &sandboxv1alpha1.Sandbox{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gawain", Namespace: "ai"}, updated))
	assert.Equal(t, int32(0), *updated.Spec.Replicas)
}

func TestSandboxBackend_Suspend_NotFoundIsOK(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("ghost", "ai")
	err := b.Suspend(ctx, knight)
	assert.NoError(t, err)
}

func TestSandboxBackend_Resume_ScalesToOne(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)

	sandbox := &sandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "gawain", Namespace: "ai"},
		Spec: sandboxv1alpha1.SandboxSpec{
			Replicas: ptr.To(int32(0)),
			PodTemplate: sandboxv1alpha1.PodTemplate{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(sandbox).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	err := b.Resume(ctx, knight)
	require.NoError(t, err)

	updated := &sandboxv1alpha1.Sandbox{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gawain", Namespace: "ai"}, updated))
	assert.Equal(t, int32(1), *updated.Spec.Replicas)
}

func TestSandboxBackend_Resume_ErrorsWhenNotFound(t *testing.T) {
	ctx := context.Background()
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("ghost", "ai")
	err := b.Resume(ctx, knight)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSandboxBackend_BuildVolumeClaimTemplates_NilWhenExistingClaim(t *testing.T) {
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	knight.Spec.Workspace = &aiv1alpha1.KnightWorkspace{
		ExistingClaim: "my-pvc",
	}

	templates := b.buildVolumeClaimTemplates(knight)
	assert.Nil(t, templates)
}

func TestSandboxBackend_BuildVolumeClaimTemplates_NilWhenNoWorkspace(t *testing.T) {
	s := newSandboxTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	b := NewSandboxBackend(c, s, "default:latest", stubSandboxPodSpecBuilder)

	knight := newTestKnight("gawain", "ai")
	knight.Spec.Workspace = nil

	templates := b.buildVolumeClaimTemplates(knight)
	assert.Nil(t, templates)
}
