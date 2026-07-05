package mission

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	"github.com/dapperdivers/roundtable/internal/status"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

// KnightAssembler handles knight assembly and provisioning for missions.
type KnightAssembler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// minAssemblyTimeout floors the assembly window. Ephemeral knights always
// start cold (PVC provisioning, Nix tool builds, image pulls), which
// legitimately takes a couple of minutes — a short mission timeout must not
// fail assembly before the pod has a chance to become ready.
const minAssemblyTimeout = 3 * time.Minute

// ReconcileAssembling handles the Assembling phase - creates ephemeral knights and waits for readiness.
func (a *KnightAssembler) ReconcileAssembling(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Ensure mission ServiceAccount exists for ephemeral knights.
	// When roundTableRef is set, provisioning is skipped, but ephemeral knights
	// still need a mission-scoped SA for their pods.
	hasEphemeral := false
	allKnightsForSA := append(mission.Spec.Knights, mission.Spec.GeneratedKnights...)
	for _, mk := range allKnightsForSA {
		if mk.Ephemeral {
			hasEphemeral = true
			break
		}
	}
	if hasEphemeral {
		serviceAccountName := fmt.Sprintf("mission-%s", mission.Name)
		if err := a.EnsureMissionServiceAccount(ctx, mission, serviceAccountName); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to ensure ServiceAccount: %w", err)
		}
		log.Info("Ensured mission ServiceAccount", "name", serviceAccountName)
	}

	// Resolve the RoundTable lazily — only fetched when an ephemeral knight needs it.
	var rt *aiv1alpha1.RoundTable
	// Track which warm pool knights have been claimed this reconcile cycle
	claimedWarmKnights := make(map[string]bool)
	getRoundTable := func() (*aiv1alpha1.RoundTable, error) {
		if rt != nil {
			return rt, nil
		}
		rtName := mission.Status.RoundTableName
		if rtName == "" {
			rtName = mission.Spec.RoundTableRef
		}
		if rtName == "" {
			return nil, fmt.Errorf("cannot create ephemeral knight: no RoundTable (neither status.roundTableName nor spec.roundTableRef is set)")
		}
		rt = &aiv1alpha1.RoundTable{}
		rtKey := types.NamespacedName{Name: rtName, Namespace: mission.Namespace}
		if err := a.Client.Get(ctx, rtKey, rt); err != nil {
			return nil, fmt.Errorf("failed to get RoundTable %q: %w", rtName, err)
		}
		return rt, nil
	}

	// Track assembly progress
	knightStatuses := make(map[string]aiv1alpha1.MissionKnightStatus)
	for _, existing := range mission.Status.KnightStatuses {
		knightStatuses[existing.Name] = existing
	}

	allReady := true
	var notReadyKnights []string

	// Bug #1 Fix: Merge GeneratedKnights into the knight list being iterated.
	// The planner writes recruited knights to mission.Spec.GeneratedKnights,
	// but the assembler was only iterating mission.Spec.Knights.
	allKnights := append(mission.Spec.Knights, mission.Spec.GeneratedKnights...)

	// Process each knight in spec (including generated knights)
	for _, mk := range allKnights {
		if !mk.Ephemeral {
			// Recruited knight - verify it exists and is Ready
			existingKnight := &aiv1alpha1.Knight{}
			knightKey := types.NamespacedName{Name: mk.Name, Namespace: mission.Namespace}
			if err := a.Client.Get(ctx, knightKey, existingKnight); err != nil {
				if client.IgnoreNotFound(err) == nil {
					log.Error(err, "Recruited knight not found", "knight", mk.Name)
					meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
						Type:               "KnightsReady",
						Status:             metav1.ConditionFalse,
						Reason:             "KnightNotFound",
						Message:            fmt.Sprintf("Recruited knight %q not found", mk.Name),
						ObservedGeneration: mission.Generation,
					})
					mission.Status.ObservedGeneration = mission.Generation
					// Note: Caller should update status
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				}
				return ctrl.Result{}, fmt.Errorf("failed to get recruited knight %q: %w", mk.Name, err)
			}

			if existingKnight.Status.Phase != aiv1alpha1.KnightPhaseReady || !existingKnight.Status.Ready {
				allReady = false
				notReadyKnights = append(notReadyKnights, mk.Name)
			}

			// Update status
			knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
				Name:      mk.Name,
				Ephemeral: false,
				Ready:     existingKnight.Status.Ready,
			}
			continue
		}

		// Ephemeral knight - create if doesn't exist (or claim from warm pool)
		knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)
		knight := &aiv1alpha1.Knight{}
		knightKey := types.NamespacedName{Name: knightName, Namespace: mission.Namespace}
		err := a.Client.Get(ctx, knightKey, knight)

		if err != nil && client.IgnoreNotFound(err) == nil {
			// Knight doesn't exist yet — lazy-load the RoundTable
			resolvedRT, rtErr := getRoundTable()
			if rtErr != nil {
				return ctrl.Result{}, rtErr
			}

			// Try to claim a warm pool knight first
			claimed, claimErr := a.claimWarmKnight(ctx, mission, mk, resolvedRT, claimedWarmKnights)
			if claimErr != nil {
				log.Error(claimErr, "Failed to claim warm pool knight, falling back to cold start", "knight", mk.Name)
			}

			if claimed {
				log.Info("Claimed warm pool knight for mission", "missionKnight", mk.Name)
				// The claim recreated the knight under its mission-prefixed name;
				// wait for the new knight to become ready like a cold start.
				knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
					Name:      mk.Name,
					Ephemeral: true,
					Ready:     false,
				}
				allReady = false
				notReadyKnights = append(notReadyKnights, mk.Name)
				continue
			}

			// No warm knight available — cold start
			knight, err := a.buildEphemeralKnight(ctx, mission, mk, resolvedRT)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to build ephemeral knight %q: %w", mk.Name, err)
			}

			log.Info("Creating ephemeral knight (cold start)", "name", knightName)
			if err := a.Client.Create(ctx, knight); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create ephemeral knight: %w", err)
			}

			// Update status (not ready yet)
			knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
				Name:      mk.Name,
				Ephemeral: true,
				Ready:     false,
			}
			allReady = false
			notReadyKnights = append(notReadyKnights, mk.Name)
			continue
		} else if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get knight %q: %w", knightName, err)
		}

		// Knight exists, check readiness
		if knight.Status.Phase != aiv1alpha1.KnightPhaseReady || !knight.Status.Ready {
			allReady = false
			notReadyKnights = append(notReadyKnights, mk.Name)
		}

		// Update status
		knightStatuses[mk.Name] = aiv1alpha1.MissionKnightStatus{
			Name:      mk.Name,
			Ephemeral: true,
			Ready:     knight.Status.Ready,
		}
	}

	// Update mission status with knight statuses
	mission.Status.KnightStatuses = make([]aiv1alpha1.MissionKnightStatus, 0, len(knightStatuses))
	for _, ks := range knightStatuses {
		mission.Status.KnightStatuses = append(mission.Status.KnightStatuses, ks)
	}

	// Note: Caller should update status

	// Check assembly timeout (Timeout/3, floored at minAssemblyTimeout)
	assemblyTimeout := time.Duration(mission.Spec.Timeout/3) * time.Second
	if assemblyTimeout < minAssemblyTimeout {
		assemblyTimeout = minAssemblyTimeout
	}
	if mission.Status.StartedAt != nil && time.Since(mission.Status.StartedAt.Time) > assemblyTimeout {
		log.Info("Assembly timeout exceeded",
			"timeout", assemblyTimeout,
			"notReady", notReadyKnights)

		if err := status.ForMission(mission).
			Failed(fmt.Sprintf("Assembly timeout: knights not ready: %v", notReadyKnights)).
			Condition("KnightsReady", "AssemblyTimeout",
				fmt.Sprintf("Knights not ready within %v: %v", assemblyTimeout, notReadyKnights),
				metav1.ConditionFalse).
			Apply(ctx, a.Client); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update mission status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// If all knights ready, transition to Briefing. A mission with no knights
	// of its own (chains-only, using standing knights) has nothing to assemble
	// and proceeds immediately — gating on totalKnights > 0 would stall it in
	// Assembling until the assembly timeout failed it.
	totalKnights := len(allKnights)
	if allReady {
		log.Info("All knights assembled, transitioning to Briefing",
			"mission", mission.Name,
			"knightCount", totalKnights)

		meta.SetStatusCondition(&mission.Status.Conditions, metav1.Condition{
			Type:               "KnightsReady",
			Status:             metav1.ConditionTrue,
			Reason:             "AllKnightsReady",
			Message:            fmt.Sprintf("All %d knights are ready", totalKnights),
			ObservedGeneration: mission.Generation,
		})
		mission.Status.Phase = aiv1alpha1.MissionPhaseBriefing
		mission.Status.ObservedGeneration = mission.Generation
		// Note: Caller should update status and requeue
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Not all ready yet, requeue to check again
	log.Info("Waiting for knights to become ready",
		"total", totalKnights,
		"notReady", len(notReadyKnights))

	return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
}

// claimWarmKnight attempts to claim an available warm pool knight for a mission.
// It reserves an unclaimed, ready warm knight, creates the mission knight under
// its mission-prefixed name ("<mission>-<knight>" — the name chain steps
// reference), and deletes the warm knight so the pool replenishes. The mission
// knight starts fresh, so the caller must treat it as not-ready and wait.
// Returns true if a knight was successfully claimed.
func (a *KnightAssembler) claimWarmKnight(
	ctx context.Context,
	mission *aiv1alpha1.Mission,
	mk aiv1alpha1.MissionKnight,
	rt *aiv1alpha1.RoundTable,
	alreadyClaimed map[string]bool,
) (bool, error) {
	log := logf.FromContext(ctx)

	// Check if the RoundTable has a warm pool
	if rt.Spec.WarmPool == nil || rt.Spec.WarmPool.Size == 0 {
		return false, nil
	}

	// List unclaimed, ready warm pool knights
	knightList := &aiv1alpha1.KnightList{}
	if err := a.Client.List(ctx, knightList,
		client.InNamespace(mission.Namespace),
		client.MatchingLabels{
			aiv1alpha1.LabelWarmPool:        "true",
			aiv1alpha1.LabelWarmPoolClaimed: "false",
			aiv1alpha1.LabelRoundTable:      rt.Name,
		},
	); err != nil {
		return false, fmt.Errorf("failed to list warm pool knights: %w", err)
	}

	// Collect ready knight candidates
	var candidates []*aiv1alpha1.Knight
	for i := range knightList.Items {
		k := &knightList.Items[i]
		if k.Status.Phase == aiv1alpha1.KnightPhaseReady && k.Status.Ready && !alreadyClaimed[k.Name] {
			candidates = append(candidates, k)
		}
	}

	if len(candidates) == 0 {
		return false, nil // No warm knights available
	}

	// Try each candidate in order — optimistic locking handles concurrent claims
	for _, warmKnight := range candidates {
		log.Info("Attempting to claim warm pool knight",
			"warmKnight", warmKnight.Name,
			"mission", mission.Name,
			"missionKnight", mk.Name)

		// Reserve the warm knight: mark as claimed and link to the mission.
		// The knight is replaced (not patched) below — chain steps reference the
		// mission-prefixed name, and CR names are immutable.
		warmKnight.Labels[aiv1alpha1.LabelWarmPoolClaimed] = "true"
		warmKnight.Labels[aiv1alpha1.LabelMission] = mission.Name

		// Attempt update — resourceVersion provides optimistic locking.
		// If another mission claimed this knight, we get a Conflict and try the next candidate.
		if err := a.Client.Update(ctx, warmKnight); err != nil {
			if apierrors.IsConflict(err) {
				log.Info("Conflict claiming warm knight, trying next candidate",
					"warmKnight", warmKnight.Name)
				continue
			}
			return false, fmt.Errorf("failed to update warm knight: %w", err)
		}

		// Mark as claimed in this cycle to avoid double-claiming
		alreadyClaimed[warmKnight.Name] = true

		// Create the mission knight under its prefixed name so chain steps
		// (which reference "<mission>-<knight>") resolve to a real CR.
		missionKnight, err := a.buildEphemeralKnight(ctx, mission, mk, rt)
		if err != nil {
			return false, fmt.Errorf("failed to build mission knight for warm claim: %w", err)
		}
		if err := a.Client.Create(ctx, missionKnight); err != nil && !apierrors.IsAlreadyExists(err) {
			// Warm knight stays reserved; the caller falls back to cold start,
			// which retries the same create on the next reconcile.
			return false, fmt.Errorf("failed to create mission knight %q from warm claim: %w", missionKnight.Name, err)
		}

		// Delete the warm knight — its capacity is consumed by the mission knight,
		// and the RoundTable controller will replenish the pool.
		if err := a.Client.Delete(ctx, warmKnight); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete claimed warm knight; pool controller will reap it",
				"warmKnight", warmKnight.Name)
		}

		log.Info("Successfully claimed warm pool knight",
			"warmKnight", warmKnight.Name,
			"missionKnight", missionKnight.Name,
			"mission", mission.Name,
			"domain", missionKnight.Spec.Domain)

		return true, nil
	}

	// All candidates were claimed by concurrent reconciles
	log.Info("All warm knight candidates claimed by concurrent reconciles",
		"mission", mission.Name,
		"candidateCount", len(candidates))
	return false, nil
}

// resolveKnightSpec resolves a KnightSpec from template or inline ephemeral spec.
func (a *KnightAssembler) resolveKnightSpec(
	mission *aiv1alpha1.Mission,
	mk aiv1alpha1.MissionKnight,
	rt *aiv1alpha1.RoundTable,
) (*aiv1alpha1.KnightSpec, error) {
	var baseSpec *aiv1alpha1.KnightSpec

	// Path A: Inline spec
	if mk.EphemeralSpec != nil {
		baseSpec = mk.EphemeralSpec.DeepCopy()
	} else if mk.TemplateRef != "" {
		// Path B: Template reference (check both Mission and RoundTable)
		var templateSpec *aiv1alpha1.KnightSpec

		// 1. Check mission-level templates first (higher priority)
		for i := range mission.Spec.KnightTemplates {
			if mission.Spec.KnightTemplates[i].Name == mk.TemplateRef {
				templateSpec = mission.Spec.KnightTemplates[i].Spec.DeepCopy()
				break
			}
		}

		// 2. If not found, check RoundTable-level templates
		if templateSpec == nil && rt != nil && rt.Spec.KnightTemplates != nil {
			if rtTemplate, ok := rt.Spec.KnightTemplates[mk.TemplateRef]; ok {
				templateSpec = rtTemplate.DeepCopy()
			}
		}

		if templateSpec == nil {
			return nil, fmt.Errorf("template %q not found in mission.spec.knightTemplates or roundTable.spec.knightTemplates", mk.TemplateRef)
		}

		baseSpec = templateSpec

		// Apply overrides (if specified)
		if mk.SpecOverrides != nil {
			a.applySpecOverrides(baseSpec, mk.SpecOverrides)
		}
	} else {
		return nil, fmt.Errorf("ephemeral knight %q has neither ephemeralSpec nor templateRef", mk.Name)
	}

	return baseSpec, nil
}

// applySpecOverrides applies KnightSpecOverrides to a KnightSpec (strategic merge).
func (a *KnightAssembler) applySpecOverrides(
	spec *aiv1alpha1.KnightSpec,
	overrides *aiv1alpha1.KnightSpecOverrides,
) {
	if overrides.Model != "" {
		spec.Model = overrides.Model
	}
	if overrides.Domain != "" {
		spec.Domain = overrides.Domain
	}
	if overrides.Skills != nil {
		spec.Skills = overrides.Skills
	}
	if overrides.Tools != nil {
		spec.Tools = overrides.Tools
	}
	if overrides.Env != nil {
		spec.Env = append(spec.Env, overrides.Env...)
	}
	if overrides.Prompt != nil {
		spec.Prompt = overrides.Prompt
	}
	if overrides.Concurrency != nil {
		spec.Concurrency = *overrides.Concurrency
	}
}

// appendSecretEnvFrom adds a secret-backed EnvFrom source to a KnightSpec,
// skipping secrets already referenced (template, RoundTable, and mission
// lists may overlap).
func appendSecretEnvFrom(spec *aiv1alpha1.KnightSpec, secretRef corev1.LocalObjectReference) {
	for _, existing := range spec.EnvFrom {
		if existing.SecretRef != nil && existing.SecretRef.Name == secretRef.Name {
			return
		}
	}
	spec.EnvFrom = append(spec.EnvFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: secretRef,
		},
	})
}

// buildEphemeralKnight creates a Knight CR for an ephemeral mission knight.
func (a *KnightAssembler) buildEphemeralKnight(
	ctx context.Context,
	mission *aiv1alpha1.Mission,
	mk aiv1alpha1.MissionKnight,
	rt *aiv1alpha1.RoundTable,
) (*aiv1alpha1.Knight, error) {
	// Resolve spec from template or inline (checks both mission and RoundTable templates)
	spec, err := a.resolveKnightSpec(mission, mk, rt)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve knight spec for %q: %w", mk.Name, err)
	}

	// Generate knight name
	knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)

	// Override NATS config to point at mission streams. Subscribe to this
	// knight's exact task subject (chains dispatch via TaskSubject to
	// {prefix}.tasks.{domain}.{knightName}); a domain wildcard would replay
	// retained tasks from other missions in the same domain.
	natsPrefix := rt.Spec.NATS.SubjectPrefix
	spec.NATS = aiv1alpha1.KnightNATS{
		URL:           rt.Spec.NATS.URL,
		Stream:        rt.Spec.NATS.TasksStream,
		ResultsStream: rt.Spec.NATS.ResultsStream,
		Subjects: []string{
			natspkg.TaskSubject(natsPrefix, spec.Domain, knightName),
		},
		ConsumerName: fmt.Sprintf("msn-%s-%s", mission.Name, mk.Name),
		MaxDeliver:   1, // Exactly-once delivery for mission tasks
	}

	// Inject RoundTable-shared secrets, then mission-specific ones. Warm
	// knights reference these secrets in their own manifests; ephemeral
	// knights only get what we inject here (model API keys live in these).
	for _, secretRef := range rt.Spec.Secrets {
		appendSecretEnvFrom(spec, secretRef)
	}
	for _, secretRef := range mission.Spec.Secrets {
		appendSecretEnvFrom(spec, secretRef)
	}

	// Ephemeral knights don't get persistent workspace
	spec.Workspace = nil

	// Use mission-scoped ServiceAccount
	spec.ServiceAccountName = fmt.Sprintf("mission-%s", mission.Name)

	// Build Knight CR
	knight := &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      knightName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:    mission.Name,
				aiv1alpha1.LabelEphemeral:  "true",
				aiv1alpha1.LabelRoundTable: rt.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: *spec,
	}

	// Add role label if specified — sanitize for K8s label constraints (63 chars, alphanumeric/dash/underscore/dot)
	if mk.Role != "" {
		sanitized := sanitizeLabelValue(mk.Role)
		if sanitized != "" {
			knight.Labels[aiv1alpha1.LabelRole] = sanitized
		}
	}

	return knight, nil
}

// ensureMissionServiceAccount creates a mission-scoped ServiceAccount if it doesn't exist.
func (a *KnightAssembler) EnsureMissionServiceAccount(ctx context.Context, mission *aiv1alpha1.Mission, saName string) error {
	sa := &corev1.ServiceAccount{}
	saKey := types.NamespacedName{Name: saName, Namespace: mission.Namespace}
	err := a.Client.Get(ctx, saKey, sa)

	if err != nil && client.IgnoreNotFound(err) == nil {
		// Create ServiceAccount
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: mission.Namespace,
				Labels: map[string]string{
					aiv1alpha1.LabelMission:   mission.Name,
					aiv1alpha1.LabelEphemeral: "true",
				},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
				},
			},
		}

		if err := a.Client.Create(ctx, sa); err != nil {
			return fmt.Errorf("failed to create ServiceAccount: %w", err)
		}
		logf.FromContext(ctx).Info("Created mission ServiceAccount", "name", saName)
	} else if err != nil {
		return fmt.Errorf("failed to get ServiceAccount: %w", err)
	}

	return nil
}

// ensureMissionNetworkPolicy creates a NetworkPolicy to isolate ephemeral knights if it doesn't exist.
func (a *KnightAssembler) EnsureMissionNetworkPolicy(ctx context.Context, mission *aiv1alpha1.Mission) error {
	policyName := fmt.Sprintf("mission-%s-isolation", mission.Name)
	policy := &networkingv1.NetworkPolicy{}
	policyKey := types.NamespacedName{Name: policyName, Namespace: mission.Namespace}
	err := a.Client.Get(ctx, policyKey, policy)

	if err != nil && client.IgnoreNotFound(err) == nil {
		// Build NetworkPolicy
		policy = a.buildMissionNetworkPolicy(mission, policyName)

		if err := a.Client.Create(ctx, policy); err != nil {
			return fmt.Errorf("failed to create NetworkPolicy: %w", err)
		}
		logf.FromContext(ctx).Info("Created mission NetworkPolicy", "name", policyName)
	} else if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy: %w", err)
	}

	return nil
}

// buildMissionNetworkPolicy constructs a NetworkPolicy that restricts ephemeral knight egress.
func (a *KnightAssembler) buildMissionNetworkPolicy(mission *aiv1alpha1.Mission, policyName string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:   mission.Name,
				aiv1alpha1.LabelEphemeral: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Apply to all ephemeral knights in this mission
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					aiv1alpha1.LabelMission: mission.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// Allow egress to NATS server
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "database",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/name": "nats",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(4222)),
						},
					},
				},
				// Allow DNS resolution (UDP and TCP)
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"k8s-app": "kube-dns",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolUDP),
							Port:     ptr.To(intstr.FromInt(53)),
						},
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(53)),
						},
					},
				},
				// Allow HTTPS egress for AI provider APIs
				// Note: This allows egress to ANY HTTPS endpoint
				// For stricter isolation, use an egress proxy with allowlisting
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: ptr.To(corev1.ProtocolTCP),
							Port:     ptr.To(intstr.FromInt(443)),
						},
					},
				},
			},
		},
	}
}

// BuildEphemeralRoundTable creates the spec for an ephemeral RoundTable for a mission.
func (a *KnightAssembler) BuildEphemeralRoundTable(
	ctx context.Context,
	mission *aiv1alpha1.Mission,
	name, natsPrefix, tasksStream, resultsStream string,
) *aiv1alpha1.RoundTable {
	// Get parent RoundTable for defaults (if specified)
	var parentDefaults *aiv1alpha1.RoundTableDefaults
	var parentPolicies *aiv1alpha1.RoundTablePolicies
	natsURL := "nats://nats.database.svc.cluster.local:4222" // Default

	if mission.Spec.RoundTableRef != "" {
		parentRT := &aiv1alpha1.RoundTable{}
		parentKey := types.NamespacedName{Name: mission.Spec.RoundTableRef, Namespace: mission.Namespace}
		// Propagate context for proper cancellation and tracing
		if err := a.Client.Get(ctx, parentKey, parentRT); err == nil {
			if parentRT.Spec.Defaults != nil {
				parentDefaults = parentRT.Spec.Defaults
			}
			if parentRT.Spec.Policies != nil {
				parentPolicies = parentRT.Spec.Policies
			}
			if parentRT.Spec.NATS.URL != "" {
				natsURL = parentRT.Spec.NATS.URL
			}
		}
	}

	// Apply mission template overrides
	if mission.Spec.RoundTableTemplate != nil {
		if mission.Spec.RoundTableTemplate.Defaults != nil {
			parentDefaults = mission.Spec.RoundTableTemplate.Defaults
		}
		if mission.Spec.RoundTableTemplate.Policies != nil {
			parentPolicies = mission.Spec.RoundTableTemplate.Policies
		}
		if mission.Spec.RoundTableTemplate.NATSURL != "" {
			natsURL = mission.Spec.RoundTableTemplate.NATSURL
		}
	}

	// Build RoundTable CR
	rt := &aiv1alpha1.RoundTable{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:   mission.Name,
				aiv1alpha1.LabelEphemeral: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: aiv1alpha1.RoundTableSpec{
			Ephemeral:  true,
			MissionRef: mission.Name,
			NATS: aiv1alpha1.RoundTableNATS{
				URL:             natsURL,
				SubjectPrefix:   natsPrefix,
				TasksStream:     tasksStream,
				ResultsStream:   resultsStream,
				CreateStreams:   true,
				StreamRetention: "WorkQueue", // ALWAYS WorkQueue for ephemeral
			},
		},
	}

	// Apply defaults (with fallback to sensible defaults)
	if parentDefaults != nil {
		rt.Spec.Defaults = parentDefaults
	} else {
		rt.Spec.Defaults = &aiv1alpha1.RoundTableDefaults{
			Model:       "openrouter/deepseek/deepseek-v3.2",
			Concurrency: 3,
			TaskTimeout: 300,
		}
	}

	// Apply policies (initialize if nil)
	if parentPolicies != nil {
		rt.Spec.Policies = parentPolicies
	} else {
		rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{}
	}

	// Override cost budget with mission budget
	if mission.Spec.CostBudgetUSD != "" && mission.Spec.CostBudgetUSD != "0" {
		rt.Spec.Policies.CostBudgetUSD = mission.Spec.CostBudgetUSD
	}

	// Set max knights to mission knight count
	if rt.Spec.Policies == nil {
		rt.Spec.Policies = &aiv1alpha1.RoundTablePolicies{}
	}
	knightCount := int32(len(mission.Spec.Knights))
	rt.Spec.Policies.MaxKnights = knightCount

	return rt
}

// sanitizeLabelValue ensures a string is a valid Kubernetes label value:
// max 63 chars, alphanumeric/dash/underscore/dot, must start and end with alphanumeric.
func sanitizeLabelValue(s string) string {
	// Replace spaces and invalid chars with dashes
	result := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			result = append(result, c)
		} else if c == ' ' {
			result = append(result, '-')
		}
	}
	// Truncate to 63 chars
	if len(result) > 63 {
		result = result[:63]
	}
	// Trim non-alphanumeric from start and end
	s2 := string(result)
	for len(s2) > 0 && !isAlphanumeric(s2[0]) {
		s2 = s2[1:]
	}
	for len(s2) > 0 && !isAlphanumeric(s2[len(s2)-1]) {
		s2 = s2[:len(s2)-1]
	}
	return s2
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
