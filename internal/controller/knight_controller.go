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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kutilintstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

const (
	knightFinalizer        = "ai.roundtable.io/finalizer"
	fieldOwner             = "roundtable-operator"
	nixToolsHashAnnotation = "roundtable.io/nix-tools-hash"
	specHashAnnotation     = "roundtable.io/spec-hash"
)

// nixToolsHash computes a deterministic hash of the Nix tool list.
// Used to detect when tools change so stale Nix PVCs can be recycled.
func nixToolsHash(tools []string) string {
	sorted := make([]string, len(tools))
	copy(sorted, tools)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(h[:8]) // 16-char hex prefix
}

// deploymentSpecHash computes a deterministic hash of the desired deployment spec
// fields (containers, volumes, labels, annotations) to detect actual changes.
func deploymentSpecHash(deploy *appsv1.Deployment) string {
	hashInput := struct {
		Labels      map[string]string              `json:"labels"`
		Annotations map[string]string              `json:"annotations"`
		Spec        corev1.PodSpec                  `json:"spec"`
		Replicas    *int32                          `json:"replicas"`
		Strategy    appsv1.DeploymentStrategyType   `json:"strategy"`
	}{
		Labels:      deploy.Spec.Template.Labels,
		Annotations: filterAnnotations(deploy.Spec.Template.Annotations),
		Spec:        deploy.Spec.Template.Spec,
		Replicas:    deploy.Spec.Replicas,
		Strategy:    deploy.Spec.Strategy.Type,
	}
	data, _ := json.Marshal(hashInput)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// filterAnnotations returns annotations without the spec-hash itself to avoid circular hashing.
func filterAnnotations(annotations map[string]string) map[string]string {
	filtered := make(map[string]string, len(annotations))
	for k, v := range annotations {
		if k != specHashAnnotation {
			filtered[k] = v
		}
	}
	return filtered
}

// KnightReconciler reconciles a Knight object
type KnightReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	DefaultImage string // Default pi-knight image (set via DEFAULT_KNIGHT_IMAGE env var)
}

// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ai.roundtable.io,resources=knights/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete

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

	// Handle suspended state
	if knight.Spec.Suspended {
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

	// 3. Deployment (pi-knight + skill-filter sidecar)
	if err := r.reconcileDeployment(ctx, knight); err != nil {
		reconcileErr = err
		log.Error(err, "Failed to reconcile Deployment")
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
		}
	}

	knight.Status.Phase = aiv1alpha1.KnightPhaseSuspended
	knight.Status.Ready = false
	meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
		Type:               "Available",
		Status:             metav1.ConditionFalse,
		Reason:             "Suspended",
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
		cm.Data["mise.toml"] = r.generateMiseToml(knight)

		// Generate apt.txt for system packages
		if knight.Spec.Tools != nil && len(knight.Spec.Tools.Apt) > 0 {
			cm.Data["apt.txt"] = strings.Join(knight.Spec.Tools.Apt, "\n")
		}

		// Skill categories for the skill-filter sidecar
		cm.Data["KNIGHT_SKILLS"] = strings.Join(knight.Spec.Skills, ",")

		// Generate flake.nix for Nix-managed tools
		if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
			cm.Data["flake.nix"] = r.generateFlakeNix(knight)
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

	// Create Nix PVC if tools.nix is configured, recycle if tools changed
	if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
		nixPVCName := fmt.Sprintf("knight-%s-nix", knight.Name)
		currentHash := nixToolsHash(knight.Spec.Tools.Nix)
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
	if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
		podAnnotations[nixToolsHashAnnotation] = nixToolsHash(knight.Spec.Tools.Nix)
	}
	desired.Spec.Template.ObjectMeta.Annotations = podAnnotations
	desired.Spec.Template.Spec = r.buildPodSpec(ctx, knight)

	// Compute hash of desired state
	desiredHash := deploymentSpecHash(desired)

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

		deploy.Spec.Template.Spec = r.buildPodSpec(ctx, knight)

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

// buildPodSpec constructs the complete pod spec for a knight.
// Matches the proven deployment pattern from the working Helm-based knights.
func (r *KnightReconciler) buildPodSpec(ctx context.Context, knight *aiv1alpha1.Knight) corev1.PodSpec {
	configMapName := fmt.Sprintf("knight-%s-config", knight.Name)

	// Determine workspace PVC name
	pvcName := knight.Name
	if knight.Spec.Workspace != nil && knight.Spec.Workspace.ExistingClaim != "" {
		pvcName = knight.Spec.Workspace.ExistingClaim
	}

	// Determine image — Knight CR overrides operator default
	image := knight.Spec.Image
	if image == "" {
		image = r.DefaultImage
	}
	if image == "" {
		image = "ghcr.io/dapperdivers/pi-knight:latest"
	}

	// Resource requests only — no limits.
	// Nix flake builds need burst memory; runtime is ~256Mi.
	// Following onedr0p pattern: requests for scheduling, no limits for flexibility.

	// Task timeout in milliseconds (pi-knight expects TASK_TIMEOUT_MS)
	taskTimeoutMs := int64(knight.Spec.TaskTimeout) * 1000

	// Build environment variables — matching pi-knight runtime expectations
	env := []corev1.EnvVar{
		{Name: "KNIGHT_NAME", Value: capitalizeFirst(knight.Name)},
		{Name: "KNIGHT_MODEL", Value: knight.Spec.Model},
		{Name: "NATS_URL", Value: knight.Spec.NATS.URL},
		{Name: "NATS_TASKS_STREAM", Value: knight.Spec.NATS.Stream},
		{Name: "NATS_RESULTS_STREAM", Value: knight.Spec.NATS.ResultsStream},
		{Name: "NATS_RESULTS_PREFIX", Value: deriveResultsPrefix(knight.Spec.NATS.Subjects)},
		{Name: "SUBSCRIBE_TOPICS", Value: strings.Join(knight.Spec.NATS.Subjects, ",")},
		{Name: "MAX_CONCURRENT_TASKS", Value: fmt.Sprintf("%d", knight.Spec.Concurrency)},
		{Name: "TASK_TIMEOUT_MS", Value: fmt.Sprintf("%d", taskTimeoutMs)},
		{Name: "METRICS_PORT", Value: "3000"},
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "TZ", Value: "America/Chicago"},
	}

	// Append user-defined env vars (can override defaults)
	env = append(env, knight.Spec.Env...)

	// --- Volumes ---
	volumes := []corev1.Volume{
		{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
				},
			},
		},
		// arsenal: git-sync populates skills here (emptyDir for now, git-sync initContainer future)
		{Name: "arsenal", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		// skills: skill-filter symlinks active categories here
		{Name: "skills", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}

	// Volume mounts for the knight container
	volumeMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: "/data"},
		{Name: "config", MountPath: "/config", ReadOnly: true},
		{Name: "arsenal", MountPath: "/arsenal", ReadOnly: true},
		{Name: "skills", MountPath: "/skills", ReadOnly: true},
	}

	// Nix store mount (if tools.nix is configured)
	if knight.Spec.Tools != nil && len(knight.Spec.Tools.Nix) > 0 {
		nixPVCName := fmt.Sprintf("knight-%s-nix", knight.Name)
		volumes = append(volumes, corev1.Volume{
			Name: "nix",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: nixPVCName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "nix",
			MountPath: "/nix",
		})
	}

	// Vault mount (if configured)
	if knight.Spec.Vault != nil {
		claimName := knight.Spec.Vault.ClaimName
		if claimName == "" {
			claimName = "obsidian-vault"
		}

		// PVC source must be ReadOnly=false when writablePaths exist,
		// otherwise the kernel-level RO on the volume blocks ALL writes,
		// even SubPath mounts marked ReadOnly=false.
		pvcReadOnly := knight.Spec.Vault.ReadOnly
		if len(knight.Spec.Vault.WritablePaths) > 0 {
			pvcReadOnly = false
		}
		volumes = append(volumes, corev1.Volume{
			Name: "vault",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
					ReadOnly:  pvcReadOnly,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "vault",
			MountPath: "/vault",
			ReadOnly:  knight.Spec.Vault.ReadOnly,
		})

		// Writable subpaths override the read-only base mount
		for _, wp := range knight.Spec.Vault.WritablePaths {
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "vault",
				MountPath: fmt.Sprintf("/vault/%s", strings.TrimSuffix(wp, "/")),
				SubPath:   strings.TrimSuffix(wp, "/"),
				ReadOnly:  false,
			})
		}
	}

	// Shared workspace mount (from parent RoundTable)
	if tableName, ok := knight.Labels["ai.roundtable.io/table"]; ok {
		rt := &aiv1alpha1.RoundTable{}
		if err := r.Get(ctx, types.NamespacedName{Name: tableName, Namespace: knight.Namespace}, rt); err == nil {
			if rt.Spec.SharedWorkspace != nil {
				mountPath := rt.Spec.SharedWorkspace.MountPath
				if mountPath == "" {
					mountPath = "/shared"
				}
				volumes = append(volumes, corev1.Volume{
					Name: "shared-workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: rt.Spec.SharedWorkspace.ClaimName,
						},
					},
				})
				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      "shared-workspace",
					MountPath: mountPath,
				})
			}
		}
	}

	// Health probe port
	probePort := 3000

	// Main knight container
	knightContainer := corev1.Container{
		Name:    "app",
		Image:   image,
		Env:     env,
		EnvFrom: knight.Spec.EnvFrom,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
		},
		VolumeMounts: volumeMounts,
		// Startup probe: generous timeout for Nix flake builds (up to 10 min)
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstrPort(probePort),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			FailureThreshold:    60, // 10 minutes (60 * 10s)
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstrPort(probePort),
				},
			},
			PeriodSeconds: 30,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/ready",
					Port: intstrPort(probePort),
				},
			},
			PeriodSeconds: 15,
		},
	}

	// Skill-filter sidecar — uses alpine with symlink script (matches working pattern)
	skillCategories := strings.Join(knight.Spec.Skills, " ")
	// Arsenal path: git-sync creates /arsenal/<repo-name> symlink
	arsenalPath := "/arsenal"
	if knight.Spec.Arsenal != nil {
		// git-sync creates a worktree at /arsenal/<repo-basename>
		repo := knight.Spec.Arsenal.Repo
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

	// Git-sync sidecar for the skill arsenal
	containers := []corev1.Container{knightContainer, skillFilterContainer}
	if knight.Spec.Arsenal != nil {
		arsenalRepo := knight.Spec.Arsenal.Repo
		if arsenalRepo == "" {
			arsenalRepo = "https://github.com/dapperdivers/roundtable-arsenal"
		}
		arsenalRef := knight.Spec.Arsenal.Ref
		if arsenalRef == "" {
			arsenalRef = "main"
		}
		arsenalPeriod := knight.Spec.Arsenal.Period
		if arsenalPeriod == "" {
			arsenalPeriod = "300s"
		}
		arsenalImage := knight.Spec.Arsenal.Image
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
		containers = append(containers, gitSyncContainer)
	}

	// Pod security context — fsGroup 1000 for PVC write access
	fsGroup := int64(1000)
	runAsUser := int64(1000)
	runAsGroup := int64(1000)

	return corev1.PodSpec{
		Containers:    containers,
		Volumes:       volumes,
		EnableServiceLinks: boolPtr(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser:           &runAsUser,
			RunAsGroup:          &runAsGroup,
			FSGroup:             &fsGroup,
			FSGroupChangePolicy: fsGroupChangePolicyPtr(corev1.FSGroupChangeOnRootMismatch),
		},
		// Use knight-specific ServiceAccount if configured, otherwise namespace default
		ServiceAccountName: knight.Spec.ServiceAccountName,
		// Auto-mount SA token — knights may need it for in-cluster access
		AutomountServiceAccountToken: boolPtr(true),
	}
}

// updateStatus sets the Knight's status based on current state.
func (r *KnightReconciler) updateStatus(ctx context.Context, knight *aiv1alpha1.Knight, reconcileErr error) error {
	// Check deployment readiness
	deploy := &appsv1.Deployment{}
	deployErr := r.Get(ctx, types.NamespacedName{Name: knight.Name, Namespace: knight.Namespace}, deploy)

	if reconcileErr != nil {
		knight.Status.Phase = aiv1alpha1.KnightPhaseDegraded
		knight.Status.Ready = false
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileError",
			Message:            reconcileErr.Error(),
			ObservedGeneration: knight.Generation,
		})
	} else if deployErr == nil && deploy.Status.ReadyReplicas > 0 {
		knight.Status.Phase = aiv1alpha1.KnightPhaseReady
		knight.Status.Ready = true
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionTrue,
			Reason:             "KnightReady",
			Message:            fmt.Sprintf("Knight %s is ready and accepting tasks", knight.Name),
			ObservedGeneration: knight.Generation,
		})
	} else {
		knight.Status.Phase = aiv1alpha1.KnightPhaseProvisioning
		knight.Status.Ready = false
		meta.SetStatusCondition(&knight.Status.Conditions, metav1.Condition{
			Type:               "Available",
			Status:             metav1.ConditionFalse,
			Reason:             "Provisioning",
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

	return r.Status().Update(ctx, knight)
}

// generateFlakeNix produces a Nix flake for tool provisioning.
func (r *KnightReconciler) generateFlakeNix(knight *aiv1alpha1.Knight) string {
	var sb strings.Builder
	sb.WriteString("# Auto-generated by roundtable-operator\n")
	sb.WriteString("# Knight: " + knight.Name + "\n")
	sb.WriteString("{\n")
	sb.WriteString("  inputs.nixpkgs.url = \"github:NixOS/nixpkgs/nixos-unstable\";\n")
	sb.WriteString("  outputs = { self, nixpkgs }:\n")
	sb.WriteString("    let\n")
	sb.WriteString("      system = \"x86_64-linux\";\n")
	sb.WriteString("      pkgs = nixpkgs.legacyPackages.${system};\n")
	sb.WriteString("    in {\n")
	sb.WriteString("      packages.${system}.default = pkgs.buildEnv {\n")
	sb.WriteString("        name = \"knight-" + knight.Name + "-tools\";\n")
	sb.WriteString("        paths = with pkgs; [\n")
	for _, pkg := range knight.Spec.Tools.Nix {
		sb.WriteString("          " + pkg + "\n")
	}
	sb.WriteString("        ];\n")
	sb.WriteString("      };\n")
	sb.WriteString("    };\n")
	sb.WriteString("}\n")
	return sb.String()
}

// generateMiseToml produces the mise configuration for tool provisioning.
func (r *KnightReconciler) generateMiseToml(knight *aiv1alpha1.Knight) string {
	var sb strings.Builder
	sb.WriteString("# Auto-generated by roundtable-operator\n")
	sb.WriteString("# Knight: " + knight.Name + "\n")
	sb.WriteString("# Domain: " + knight.Spec.Domain + "\n\n")

	if knight.Spec.Tools != nil && len(knight.Spec.Tools.Mise) > 0 {
		sb.WriteString("[tools]\n")
		for _, tool := range knight.Spec.Tools.Mise {
			sb.WriteString(fmt.Sprintf("%s = \"latest\"\n", tool))
		}
	}

	return sb.String()
}

// SetupWithManager sets up the controller with the Manager.
func (r *KnightReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Knight{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Named("knight").
		Complete(r)
}

// Helpers
// deriveResultsPrefix extracts the NATS subject prefix for results from task subjects.
// e.g., ["fleet-a.tasks.security.>"] → "fleet-a.results"
//       ["chelonian.tasks.frontend.>"] → "chelonian.results"
//       ["rt-dev.tasks.operator.>"] → "rt-dev.results"
func deriveResultsPrefix(subjects []string) string {
	for _, subj := range subjects {
		// Split on ".tasks." to reliably extract the table prefix
		parts := strings.SplitN(subj, ".tasks.", 2)
		if len(parts) == 2 && parts[0] != "" {
			return parts[0] + ".results"
		}
	}
	// Last resort: try splitting on dots
	if len(subjects) > 0 {
		parts := strings.Split(subjects[0], ".")
		if len(parts) >= 2 {
			return parts[0] + ".results"
		}
	}
	// Fallback: use env-configurable prefix for legacy knights without subjects
	prefix := os.Getenv("NATS_DEFAULT_PREFIX")
	if prefix == "" {
		prefix = "fleet-a"
	}
	return prefix + ".results"
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func boolPtr(b bool) *bool {
	return &b
}

func fsGroupChangePolicyPtr(p corev1.PodFSGroupChangePolicy) *corev1.PodFSGroupChangePolicy {
	return &p
}

func intstrPort(port int) kutilintstr.IntOrString {
	return kutilintstr.FromInt32(int32(port))
}

// end of file
