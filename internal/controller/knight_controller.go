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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
	rtmetrics "github.com/dapperdivers/roundtable/pkg/metrics"
	rtruntime "github.com/dapperdivers/roundtable/pkg/runtime"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
)

const (
	knightFinalizer        = "ai.roundtable.io/knight-finalizer"
	fieldOwner             = "roundtable-operator"
	nixToolsHashAnnotation = "roundtable.io/nix-tools-hash"
	specHashAnnotation     = "roundtable.io/spec-hash"
)

// KnightReconciler reconciles a Knight object
type KnightReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     record.EventRecorder
	DefaultImage string // Default pi-knight image (set via DEFAULT_KNIGHT_IMAGE env var)

	// RuntimeBackend abstracts the lifecycle of Knight runtime resources.
	// When set, the controller delegates Deployment reconciliation and
	// suspend/resume to this backend. When nil, falls back to the
	// inline reconcileDeployment / reconcileSuspended methods.
	RuntimeBackend rtruntime.RuntimeBackend

	// RuntimeBackends maps runtime type names to their backend implementations.
	// The controller selects the backend based on knight.Spec.Runtime.
	// If nil or the key is missing, falls back to RuntimeBackend.
	RuntimeBackends map[string]rtruntime.RuntimeBackend
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *KnightReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Knight resource
	knight := &aiv1alpha1.Knight{}
	if err := r.Get(ctx, req.NamespacedName, knight); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Knight resource not found — likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion via finalizer
	if knight.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(knight, knightFinalizer) {
			log.Info("Cleaning up knight resources", "knight", knight.Name)
			// NATS consumer cleanup would go here (future: NATS admin API call)
			controllerutil.RemoveFinalizer(knight, knightFinalizer)
			if err := r.Update(ctx, knight); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(knight, knightFinalizer) {
		controllerutil.AddFinalizer(knight, knightFinalizer)
		if err := r.Update(ctx, knight); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Set initial status
	if knight.Status.Phase == "" {
		knight.Status.Phase = aiv1alpha1.KnightPhaseProvisioning
		if err := r.Status().Update(ctx, knight); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the runtime backend for this knight
	backend := r.runtimeBackendFor(knight)

	// Clean up resources from a previous runtime type (e.g., Deployment → Sandbox transition)
	if err := r.cleanupStaleRuntime(ctx, knight); err != nil {
		log.Error(err, "Failed to clean up stale runtime resources")
		// Don't block reconciliation — the cleanup will retry on next reconcile
	}

	// Handle suspended state
	if knight.Spec.Suspended {
		if backend != nil {
			if err := backend.Suspend(ctx, knight); err != nil {
				return ctrl.Result{}, err
			}
			return r.finishSuspended(ctx, knight)
		}
		return r.reconcileSuspended(ctx, knight)
	}

	// Reconcile each owned resource
	var reconcileErr error

	// 1. ConfigMap (tools + prompt config)
	if err := r.reconcileConfigMap(ctx, knight); err != nil {
		reconcileErr = err
		log.Error(err, "Failed to reconcile ConfigMap")
	}

	// 2. PVC (persistent workspace)
	if err := r.reconcilePVC(ctx, knight); err != nil {
		reconcileErr = err
		log.Error(err, "Failed to reconcile PVC")
	}

	// 3. Runtime (Deployment or Sandbox, depending on knight.Spec.Runtime)
	if backend != nil {
		if err := backend.Reconcile(ctx, knight); err != nil {
			reconcileErr = err
			log.Error(err, "Failed to reconcile runtime", "backend", knight.Spec.Runtime)
		}
	} else {
		if err := r.reconcileDeployment(ctx, knight); err != nil {
			reconcileErr = err
			log.Error(err, "Failed to reconcile Deployment")
		}
	}

	// Update status based on reconciliation results
	if err := r.updateStatus(ctx, knight, reconcileErr); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	if reconcileErr != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, reconcileErr
	}

	return ctrl.Result{}, nil
}

// cleanupStaleRuntime removes runtime resources from a previous runtime type.
// When a Knight transitions between "deployment" and "sandbox" runtimes,
// the old resource (Deployment or Sandbox) must be removed to avoid
// Multi-Attach errors on RWO PVCs.
func (r *KnightReconciler) cleanupStaleRuntime(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)
	nn := types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}

	if knight.Spec.Runtime == "sandbox" {
		// Sandbox runtime — clean up any stale Deployment
		deploy := &appsv1.Deployment{}
		if err := r.Get(ctx, nn, deploy); err == nil {
			log.Info("Runtime transition: deleting stale Deployment for sandbox knight",
				"knight", knight.Name)
			if err := r.Delete(ctx, deploy); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete stale deployment during runtime transition: %w", err)
			}
		}
	} else {
		// Deployment runtime (default) — clean up any stale Sandbox
		sandbox := &sandboxv1alpha1.Sandbox{}
		if err := r.Get(ctx, nn, sandbox); err == nil {
			log.Info("Runtime transition: deleting stale Sandbox for deployment knight",
				"knight", knight.Name)
			if err := r.Delete(ctx, sandbox); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete stale sandbox during runtime transition: %w", err)
			}
		}
	}

	return nil
}

// reconcileSuspended scales the deployment to 0 and updates status.
func (r *KnightReconciler) reconcileSuspended(ctx context.Context, knight *aiv1alpha1.Knight) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)
	if err == nil {
		zero := int32(0)
		if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != zero {
			deploy.Spec.Replicas = &zero
			if err := r.Update(ctx, deploy); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Suspended knight — scaled to 0", "knight", knight.Name)
			r.Recorder.Event(knight, corev1.EventTypeNormal, "Suspended", "Knight suspended")
		}
	}

	knight.Status.Phase = aiv1alpha1.KnightPhaseSuspended
	knight.Status.Ready = false
	meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
		Type:               aiv1alpha1.ConditionKnightAvailable,
		Status:             metav1.ConditionFalse,
		Reason:             aiv1alpha1.ReasonKnightSuspended,
		Message:            "Knight is suspended",
		ObservedGeneration: knight.Generation,
	})
	knight.Status.ObservedGeneration = knight.Generation
	if err := r.Status().Update(ctx, knight); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// finishSuspended updates the Knight status after the RuntimeBackend has suspended it.
func (r *KnightReconciler) finishSuspended(ctx context.Context, knight *aiv1alpha1.Knight) (ctrl.Result, error) {
	knight.Status.Phase = aiv1alpha1.KnightPhaseSuspended
	knight.Status.Ready = false
	meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
		Type:               aiv1alpha1.ConditionKnightAvailable,
		Status:             metav1.ConditionFalse,
		Reason:             aiv1alpha1.ReasonKnightSuspended,
		Message:            "Knight is suspended",
		ObservedGeneration: knight.Generation,
	})
	knight.Status.ObservedGeneration = knight.Generation
	if err := r.Status().Update(ctx, knight); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileConfigMap creates/updates the knight's tool and prompt configuration.
func (r *KnightReconciler) reconcileConfigMap(ctx context.Context, knight *aiv1alpha1.Knight) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("knight-%s-config", knight.Name),
			Namespace: knight.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(knight, cm, r.Scheme); err != nil {
			return err
		}

		if cm.Labels == nil {
			cm.Labels = make(map[string]string)
		}
		cm.Labels["app.kubernetes.io/name"] = "knight"
		cm.Labels["app.kubernetes.io/instance"] = knight.Name
		cm.Labels["app.kubernetes.io/managed-by"] = "roundtable-operator"
		cm.Labels["roundtable.io/domain"] = knight.Spec.Domain

		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}

		// Generate mise.toml for tool provisioning
		cm.Data["mise.toml"] = knightpkg.GenerateMiseToml(knight)

		// Generate apt.txt for system packages
		if knight.Spec.Tools != nil && len(knight.Spec.Tools.Apt) > 0 {
			cm.Data["apt.txt"] = strings.Join(knight.Spec.Tools.Apt, "\n")
		}

		// Skill categories for the skill-filter sidecar
		cm.Data["KNIGHT_SKILLS"] = strings.Join(knight.Spec.Skills, ",")

		// Generate flake.nix for Nix-managed tools
		if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
			cm.Data["flake.nix"] = knightpkg.GenerateFlakeNix(knight)
		}

		// Generate TOOLS.md listing available tools and paths
		if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
			var toolsDoc strings.Builder
			toolsDoc.WriteString("# Available Tools\n\n")
			toolsDoc.WriteString("Tools are installed at `/data/nix-env/bin/` and are in your PATH.\n\n")
			toolsDoc.WriteString("## Nix Packages\n")
			for _, pkg := range knight.Spec.Tools.Nix {
				toolsDoc.WriteString(fmt.Sprintf("- %s\n", pkg))
			}
			toolsDoc.WriteString("\n## Shared Workspace\n")
			toolsDoc.WriteString("- `/shared/` — RWX volume shared with all knights\n")
			toolsDoc.WriteString("- `/shared/repos/` — Pre-cloned git repositories\n")
			toolsDoc.WriteString("- `/shared/chains/` — Chain working directories\n")
			toolsDoc.WriteString("\n## Git Configuration\n")
			toolsDoc.WriteString("- `GH_TOKEN` / `GITHUB_TOKEN` env vars are set for GitHub API access\n")
			toolsDoc.WriteString("- Use `gh` CLI for PR creation: `gh pr create --title ... --body ...`\n")
			toolsDoc.WriteString("- Use authenticated clone: `git clone https://${GH_TOKEN}@github.com/...`\n")
			toolsDoc.WriteString("\n## Self-Installing Tools\n")
			toolsDoc.WriteString("You can install additional tools at runtime using Nix:\n")
			toolsDoc.WriteString("```bash\n")
			toolsDoc.WriteString("# Install a package (persists on your PVC across restarts)\n")
			toolsDoc.WriteString("nix profile install nixpkgs#<package>\n")
			toolsDoc.WriteString("# Search for packages\n")
			toolsDoc.WriteString("nix search nixpkgs <query>\n")
			toolsDoc.WriteString("```\n")
			toolsDoc.WriteString("Installed tools persist in /nix on your PVC. For permanent additions,\n")
			toolsDoc.WriteString("request them via the fleet-self-improvement chain.\n")
			cm.Data["TOOLS.md"] = toolsDoc.String()
		}

		// Prompt overrides
		if knight.Spec.Prompt != nil {
			if knight.Spec.Prompt.Identity != "" {
				cm.Data["SOUL.md"] = knight.Spec.Prompt.Identity
			}
			if knight.Spec.Prompt.Instructions != "" {
				cm.Data["AGENTS.md"] = knight.Spec.Prompt.Instructions
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("configmap reconcile failed: %w", err)
	}

	logf.FromContext(ctx).Info("ConfigMap reconciled", "operation", op)
	return nil
}

// reconcilePVC creates the knight's persistent workspace volume.
// Skips creation when spec.workspace.existingClaim is set (migration mode).
func (r *KnightReconciler) reconcilePVC(ctx context.Context, knight *aiv1alpha1.Knight) error {
	// Workspace PVC — skip if using an existing claim (migration mode)
	if knight.Spec.Workspace != nil && knight.Spec.Workspace.ExistingClaim != "" {
		logf.FromContext(ctx).Info("Using existing PVC", "claim", knight.Spec.Workspace.ExistingClaim)
	} else {
		if err := r.ensureWorkspacePVC(ctx, knight); err != nil {
			return err
		}
	}

	// Create Nix PVC if nix tools are configured, recycle if tools changed
	hasNixTools := (knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0) || len(knight.Spec.NixPackages) > 0
	if hasNixTools {
		nixPVCName := fmt.Sprintf("knight-%s-nix", knight.Name)
		currentHash := knightpkg.NixToolsHash(knight)
		nixPVC := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{Name: nixPVCName, Namespace: knight.Namespace}, nixPVC)

		if err == nil {
			// PVC exists — check if tools changed
			existingHash := nixPVC.Annotations[nixToolsHashAnnotation]
			if existingHash != "" && existingHash != currentHash {
				logf.FromContext(ctx).Info("Nix tools changed — recycling PVC",
					"name", nixPVCName,
					"oldHash", existingHash,
					"newHash", currentHash)
				r.Recorder.Event(knight, corev1.EventTypeNormal, "NixPVCCleaned", "Stale Nix PVC deleted due to tools change")
				if err := r.Delete(ctx, nixPVC); err != nil {
					return fmt.Errorf("Nix PVC delete for recycle failed: %w", err)
				}
				// Return early — next reconcile will create the fresh PVC
				// The Deployment will be updated with the new hash annotation,
				// triggering a rolling restart that waits for the new PVC.
				return nil
			}
		}

		if apierrors.IsNotFound(err) {
			nixPVC = &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nixPVCName,
					Namespace: knight.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       "knight",
						"app.kubernetes.io/instance":   knight.Name,
						"app.kubernetes.io/managed-by": "roundtable-operator",
						"roundtable.io/purpose":        "nix-store",
					},
					Annotations: map[string]string{
						nixToolsHashAnnotation: currentHash,
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("5Gi"),
						},
					},
				},
			}
			if err := controllerutil.SetControllerReference(knight, nixPVC, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, nixPVC); err != nil {
				return fmt.Errorf("Nix PVC create failed: %w", err)
			}
			logf.FromContext(ctx).Info("Nix PVC created", "name", nixPVCName, "toolsHash", currentHash)
		} else if err != nil {
			return fmt.Errorf("Nix PVC get failed: %w", err)
		}
	}

	return nil
}

// ensureWorkspacePVC creates a new workspace PVC if one doesn't exist.
func (r *KnightReconciler) ensureWorkspacePVC(ctx context.Context, knight *aiv1alpha1.Knight) error {
	pvcName := knight.Name
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: knight.Namespace}, pvc)

	if apierrors.IsNotFound(err) {
		storageSize := "1Gi"
		if knight.Spec.Workspace != nil && knight.Spec.Workspace.Size != "" {
			storageSize = knight.Spec.Workspace.Size
		}

		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: knight.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       "knight",
					"app.kubernetes.io/instance":   knight.Name,
					"app.kubernetes.io/managed-by": "roundtable-operator",
					"roundtable.io/domain":         knight.Spec.Domain,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(storageSize),
					},
				},
			},
		}
		if err := controllerutil.SetControllerReference(knight, pvc, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, pvc); err != nil {
			return fmt.Errorf("PVC create failed: %w", err)
		}
		logf.FromContext(ctx).Info("PVC created", "name", pvcName)
	} else if err != nil {
		return fmt.Errorf("PVC get failed: %w", err)
	}
	return nil
}

// reconcileDeployment creates/updates the knight's Deployment.
// Uses a spec hash annotation to avoid unnecessary updates that would trigger
// a reconciliation hot loop.
func (r *KnightReconciler) reconcileDeployment(ctx context.Context, knight *aiv1alpha1.Knight) error {
	log := logf.FromContext(ctx)

	// Build the desired state in a temporary deployment to compute the hash
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knight.Name,
			Namespace: knight.Namespace,
		},
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "knight",
		"app.kubernetes.io/instance":   knight.Name,
		"app.kubernetes.io/managed-by": "roundtable-operator",
		"roundtable.io/domain":         knight.Spec.Domain,
	}

	replicas := int32(1)
	desired.Spec.Replicas = &replicas
	desired.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RecreateDeploymentStrategyType,
	}
	desired.Spec.Template.ObjectMeta.Labels = labels
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
	desired.Spec.Template.Spec = r.BuildPodSpec(ctx, knight)

	// Compute hash of desired state
	desiredHash := knightpkg.DeploymentSpecHash(desired)

	// Fetch or create the deployment
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knight.Name,
			Namespace: knight.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(knight, deploy, r.Scheme); err != nil {
			return err
		}

		// Check if the spec hash matches — if so, skip mutation
		existingHash := ""
		if deploy.Spec.Template.Annotations != nil {
			existingHash = deploy.Spec.Template.Annotations[specHashAnnotation]
		}
		if existingHash == desiredHash {
			// No changes needed — return without modifying the object
			// so CreateOrUpdate sees no diff and reports "unchanged"
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

		// Add spec hash to pod annotations
		podAnnotations[specHashAnnotation] = desiredHash
		deploy.Spec.Template.ObjectMeta.Annotations = podAnnotations

		deploy.Spec.Template.Spec = r.BuildPodSpec(ctx, knight)

		return nil
	})

	if err != nil {
		return fmt.Errorf("deployment reconcile failed: %w", err)
	}

	log.Info("Deployment reconciled", "operation", op,
		"specImage", knight.Spec.Image,
		"defaultImage", r.DefaultImage,
		"resolvedImage", deploy.Spec.Template.Spec.Containers[0].Image)
	return nil
}

// BuildDeploymentSpec constructs the full DeploymentSpec for a Knight.
// Exported so it can be passed as a PodSpecBuilder to RuntimeBackend.
func (r *KnightReconciler) BuildDeploymentSpec(ctx context.Context, knight *aiv1alpha1.Knight) appsv1.DeploymentSpec {
	labels := map[string]string{
		"app.kubernetes.io/name":       "knight",
		"app.kubernetes.io/instance":   knight.Name,
		"app.kubernetes.io/managed-by": "roundtable-operator",
		"roundtable.io/domain":         knight.Spec.Domain,
	}
	replicas := int32(1)
	return appsv1.DeploymentSpec{
		Replicas: &replicas,
		Strategy: appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
		},
		Selector: &metav1.LabelSelector{
			MatchLabels: labels,
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
			},
			Spec: r.BuildPodSpec(ctx, knight),
		},
	}
}

// BuildPodSpec constructs the complete pod spec for a knight using the composable builder.
// Exported so it can be passed to RuntimeBackend implementations (e.g., SandboxBackend).
func (r *KnightReconciler) BuildPodSpec(ctx context.Context, k *aiv1alpha1.Knight) corev1.PodSpec {
	configMapName := fmt.Sprintf("knight-%s-config", k.Name)

	builder := knightpkg.NewPodBuilder(k, r.DefaultImage).
		WithReader(r.Client).
		WithWorkspace().
		WithConfig(configMapName).
		WithNixStore().
		WithVault().
		WithSharedWorkspace(ctx).
		WithArsenal().
		WithSkillFilter().
		WithGitSync()

	// Optional capabilities
	if k.Spec.Capabilities != nil && k.Spec.Capabilities.Browser {
		builder.WithBrowser()
	}

	return builder.Build(ctx)
}

func (r *KnightReconciler) updateStatus(ctx context.Context, knight *aiv1alpha1.Knight, reconcileErr error) error {
	// Check deployment readiness — prefer RuntimeBackend if available
	backend := r.runtimeBackendFor(knight)
	var isReady bool
	if backend != nil {
		ready, err := backend.IsReady(ctx, knight)
		if err == nil {
			isReady = ready
		}
	} else {
		deploy := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy); err == nil {
			isReady = deploy.Status.ReadyReplicas > 0
		}
	}

	if reconcileErr != nil {
		r.Recorder.Eventf(knight, corev1.EventTypeWarning, "ReconcileFailed", "Reconciliation failed: %v", reconcileErr)
		knight.Status.Phase = aiv1alpha1.KnightPhaseDegraded
		knight.Status.Ready = false
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionKnightAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonKnightReconcileError,
			Message:            reconcileErr.Error(),
			ObservedGeneration: knight.Generation,
		})
	} else if isReady {
		// Record event when transitioning to Ready (avoid duplicate events)
		if knight.Status.Phase != aiv1alpha1.KnightPhaseReady {
			r.Recorder.Event(knight, corev1.EventTypeNormal, "Ready", "Knight is ready and accepting tasks")
		}
		knight.Status.Phase = aiv1alpha1.KnightPhaseReady
		knight.Status.Ready = true
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionKnightAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             aiv1alpha1.ReasonKnightReady,
			Message:            fmt.Sprintf("Knight %s is ready and accepting tasks", knight.Name),
			ObservedGeneration: knight.Generation,
		})
	} else {
		knight.Status.Phase = aiv1alpha1.KnightPhaseProvisioning
		knight.Status.Ready = false
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               aiv1alpha1.ConditionKnightAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             aiv1alpha1.ReasonKnightProvisioning,
			Message:            "Knight deployment is being provisioned",
			ObservedGeneration: knight.Generation,
		})
	}

	// Set NATS consumer name in status
	consumerName := knight.Spec.NATS.ConsumerName
	if consumerName == "" {
		consumerName = fmt.Sprintf("knight-%s", knight.Name)
	}
	knight.Status.NATSConsumer = consumerName
	knight.Status.ObservedGeneration = knight.Generation

	// Update Prometheus metrics
	tableName := knight.Labels[aiv1alpha1.LabelRoundTable]
	if tableName == "" {
		tableName = "none"
	}
	// Note: KnightsTotal is a gauge. In a real aggregation, we'd track all knights,
	// but for now we set 1 for this knight's current phase. A separate aggregator
	// or the RoundTable controller should reset/recompute totals.
	rtmetrics.KnightsTotal.WithLabelValues(string(knight.Status.Phase), tableName).Set(1)

	return r.Status().Update(ctx, knight)
}

// runtimeBackendFor returns the appropriate RuntimeBackend for a Knight.
// It checks knight.Spec.Runtime against the RuntimeBackends map, falling back
// to the default RuntimeBackend, and finally to nil (inline reconciliation).
func (r *KnightReconciler) runtimeBackendFor(knight *aiv1alpha1.Knight) rtruntime.RuntimeBackend {
	if r.RuntimeBackends != nil && knight.Spec.Runtime != "" {
		if backend, ok := r.RuntimeBackends[knight.Spec.Runtime]; ok {
			return backend
		}
	}
	return r.RuntimeBackend
}

// SetupWithManager sets up the controller with the Manager.
func (r *KnightReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Knight{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&sandboxv1alpha1.Sandbox{}).
		Named("knight").
		Complete(r)
}

// deriveResultsPrefix is a re-export from the knight package for backward compatibility with tests.
func deriveResultsPrefix(subjects []string) string {
	return knightpkg.DeriveResultsPrefix(subjects)
}
