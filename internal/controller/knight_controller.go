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
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

const (
	knightFinalizer = "ai.roundtable.io/finalizer"
	fieldOwner      = "roundtable-operator"
)

// KnightReconciler reconciles a Knight object
type KnightReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
func (r *KnightReconciler) reconcilePVC(ctx context.Context, knight *aiv1alpha1.Knight) error {
	pvc := &corev1.PersistentVolumeClaim{}
	pvcName := fmt.Sprintf("knight-%s-workspace", knight.Name)
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: knight.Namespace}, pvc)

	if apierrors.IsNotFound(err) {
		// Create new PVC
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
						corev1.ResourceStorage: resource.MustParse("1Gi"),
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
	// PVCs are immutable after creation — no update needed

	return nil
}

// reconcileDeployment creates/updates the knight's Deployment.
func (r *KnightReconciler) reconcileDeployment(ctx context.Context, knight *aiv1alpha1.Knight) error {
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

		labels := map[string]string{
			"app.kubernetes.io/name":       "knight",
			"app.kubernetes.io/instance":   knight.Name,
			"app.kubernetes.io/managed-by": "roundtable-operator",
			"roundtable.io/domain":         knight.Spec.Domain,
		}

		deploy.Labels = labels

		replicas := int32(1)
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: labels,
		}

		deploy.Spec.Template.ObjectMeta.Labels = labels
		deploy.Spec.Template.ObjectMeta.Annotations = map[string]string{
			"roundtable.io/model":    knight.Spec.Model,
			"roundtable.io/skills":   strings.Join(knight.Spec.Skills, ","),
			"roundtable.io/domain":   knight.Spec.Domain,
		}

		// Build the pod spec
		deploy.Spec.Template.Spec = r.buildPodSpec(knight)

		return nil
	})

	if err != nil {
		return fmt.Errorf("deployment reconcile failed: %w", err)
	}

	logf.FromContext(ctx).Info("Deployment reconciled", "operation", op)
	return nil
}

// buildPodSpec constructs the complete pod spec for a knight.
func (r *KnightReconciler) buildPodSpec(knight *aiv1alpha1.Knight) corev1.PodSpec {
	configMapName := fmt.Sprintf("knight-%s-config", knight.Name)
	pvcName := fmt.Sprintf("knight-%s-workspace", knight.Name)

	// Determine image
	image := knight.Spec.Image
	if image == "" {
		image = "ghcr.io/dapperdivers/pi-knight:latest"
	}

	// Resource limits
	memLimit := resource.MustParse("256Mi")
	cpuLimit := resource.MustParse("200m")
	if knight.Spec.Resources != nil {
		if !knight.Spec.Resources.Memory.IsZero() {
			memLimit = knight.Spec.Resources.Memory
		}
		if !knight.Spec.Resources.CPU.IsZero() {
			cpuLimit = knight.Spec.Resources.CPU
		}
	}

	// Build environment variables
	env := []corev1.EnvVar{
		{Name: "KNIGHT_NAME", Value: knight.Name},
		{Name: "KNIGHT_DOMAIN", Value: knight.Spec.Domain},
		{Name: "KNIGHT_MODEL", Value: knight.Spec.Model},
		{Name: "KNIGHT_SKILLS", ValueFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
				Key:                  "KNIGHT_SKILLS",
			},
		}},
		{Name: "NATS_URL", Value: knight.Spec.NATS.URL},
		{Name: "NATS_STREAM", Value: knight.Spec.NATS.Stream},
		{Name: "NATS_RESULTS_STREAM", Value: knight.Spec.NATS.ResultsStream},
		{Name: "NATS_FILTER_SUBJECTS", Value: strings.Join(knight.Spec.NATS.Subjects, ",")},
		{Name: "MAX_CONCURRENT_TASKS", Value: fmt.Sprintf("%d", knight.Spec.Concurrency)},
		{Name: "TASK_TIMEOUT_SECONDS", Value: fmt.Sprintf("%d", knight.Spec.TaskTimeout)},
	}

	// Consumer name
	consumerName := knight.Spec.NATS.ConsumerName
	if consumerName == "" {
		consumerName = fmt.Sprintf("knight-%s", knight.Name)
	}
	env = append(env, corev1.EnvVar{Name: "NATS_CONSUMER_NAME", Value: consumerName})

	// Max deliver
	env = append(env, corev1.EnvVar{
		Name:  "NATS_MAX_DELIVER",
		Value: fmt.Sprintf("%d", knight.Spec.NATS.MaxDeliver),
	})

	// Append user-defined env vars
	env = append(env, knight.Spec.Env...)

	// Volumes
	volumes := []corev1.Volume{
		{
			Name: "workspace",
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
	}

	// Volume mounts for the knight container
	volumeMounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
		{Name: "config", MountPath: "/config", ReadOnly: true},
	}

	// Vault mount (if configured)
	if knight.Spec.Vault != nil {
		claimName := knight.Spec.Vault.ClaimName
		if claimName == "" {
			claimName = "obsidian-vault"
		}

		volumes = append(volumes, corev1.Volume{
			Name: "vault",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
					ReadOnly:  knight.Spec.Vault.ReadOnly,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "vault",
			MountPath: "/vault",
			ReadOnly:  knight.Spec.Vault.ReadOnly,
		})

		// Writable subpaths get their own mounts
		for _, wp := range knight.Spec.Vault.WritablePaths {
			safeName := "vault-rw-" + strings.ReplaceAll(strings.TrimSuffix(wp, "/"), "/", "-")
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "vault",
				MountPath: fmt.Sprintf("/vault/%s", wp),
				SubPath:   wp,
				ReadOnly:  false,
			})
			_ = safeName // name used for clarity in mount identification
		}
	}

	// Security context
	runAsNonRoot := true
	readOnlyRootFS := false // pi-knight needs write access for tool installation
	runAsUser := int64(1000)

	// Main knight container
	knightContainer := corev1.Container{
		Name:  "knight",
		Image: image,
		Env:   env,
		EnvFrom: knight.Spec.EnvFrom,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
				corev1.ResourceCPU:    resource.MustParse("50m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: memLimit,
				corev1.ResourceCPU:    cpuLimit,
			},
		},
		VolumeMounts: volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			RunAsUser:                &runAsUser,
			ReadOnlyRootFilesystem:   &readOnlyRootFS,
			AllowPrivilegeEscalation: boolPtr(false),
		},
	}

	// Skill-filter sidecar
	skillFilterContainer := corev1.Container{
		Name:  "skill-filter",
		Image: "ghcr.io/dapperdivers/pi-knight:latest", // Same image, different entrypoint
		Command: []string{"/bin/sh", "-c"},
		Args: []string{
			"echo 'Skill filter active for: $KNIGHT_SKILLS' && " +
				"/app/scripts/skill-filter.sh && " +
				"sleep infinity",
		},
		Env: []corev1.EnvVar{
			{Name: "KNIGHT_SKILLS", ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
					Key:                  "KNIGHT_SKILLS",
				},
			}},
			{Name: "KNIGHT_NAME", Value: knight.Name},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("32Mi"),
				corev1.ResourceCPU:    resource.MustParse("10m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("64Mi"),
				corev1.ResourceCPU:    resource.MustParse("50m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			RunAsUser:                &runAsUser,
			ReadOnlyRootFilesystem:   &readOnlyRootFS,
			AllowPrivilegeEscalation: boolPtr(false),
		},
	}

	return corev1.PodSpec{
		Containers: []corev1.Container{knightContainer, skillFilterContainer},
		Volumes:    volumes,
		// Don't auto-mount service account token (security best practice)
		AutomountServiceAccountToken: boolPtr(false),
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

// Helper
func boolPtr(b bool) *bool {
	return &b
}

// Ensure we don't import equality without using it
var _ = equality.Semantic
