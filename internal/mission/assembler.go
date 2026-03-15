package mission

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
)

// KnightAssembler handles knight assembly and provisioning for missions.
type KnightAssembler struct {
	Client client.Client
	Scheme *runtime.Scheme
}

// ReconcileAssembling handles the Assembling phase - creates ephemeral knights and waits for readiness.
func (a *KnightAssembler) ReconcileAssembling(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Ensure mission ServiceAccount exists for ephemeral knights.
	// When roundTableRef is set, provisioning is skipped, but ephemeral knights
	// still need a mission-scoped SA for their pods.
	hasEphemeral := false
	for _, mk := range mission.Spec.Knights {
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
				return ctrl.Result{}, err
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

		// Ephemeral knight - create if doesn't exist
		knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)
		knight := &aiv1alpha1.Knight{}
		knightKey := types.NamespacedName{Name: knightName, Namespace: mission.Namespace}
		err := a.Client.Get(ctx, knightKey, knight)

		if err != nil && client.IgnoreNotFound(err) == nil {
			// Create ephemeral knight — lazy-load the RoundTable
			resolvedRT, rtErr := getRoundTable()
			if rtErr != nil {
				return ctrl.Result{}, rtErr
			}

			knight, err := a.buildEphemeralKnight(ctx, mission, mk, resolvedRT)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to build ephemeral knight %q: %w", mk.Name, err)
			}

			log.Info("Creating ephemeral knight", "name", knightName)
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

	// Check assembly timeout (Timeout/3)
	assemblyTimeout := time.Duration(mission.Spec.Timeout/3) * time.Second
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
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// If all knights ready, transition to Briefing
	totalKnights := len(allKnights)
	if allReady && totalKnights > 0 {
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
		return nil, err
	}

	// Generate knight name
	knightName := fmt.Sprintf("%s-%s", mission.Name, mk.Name)

	// Override NATS config to point at mission streams
	natsPrefix := rt.Spec.NATS.SubjectPrefix
	spec.NATS = aiv1alpha1.KnightNATS{
		URL:           rt.Spec.NATS.URL,
		Stream:        rt.Spec.NATS.TasksStream,
		ResultsStream: rt.Spec.NATS.ResultsStream,
		Subjects: []string{
			fmt.Sprintf("%s.tasks.%s.>", natsPrefix, spec.Domain),
		},
		ConsumerName: fmt.Sprintf("msn-%s-%s", mission.Name, mk.Name),
		MaxDeliver:   1, // Exactly-once delivery for mission tasks
	}

	// Inject mission secrets (if any)
	if len(mission.Spec.Secrets) > 0 {
		for _, secretRef := range mission.Spec.Secrets {
			spec.EnvFrom = append(spec.EnvFrom, corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: secretRef,
				},
			})
		}
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
		// Use background context for fetching defaults (non-critical)
		if err := a.Client.Get(context.Background(), parentKey, parentRT); err == nil {
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
			Model:       "claude-sonnet-4-20250514",
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
