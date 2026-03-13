// Meta-missions planning phase implementation
// This code should be integrated into internal/controller/mission_controller.go

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

// PlannerOutput represents the JSON output from the planner knight.
type PlannerOutput struct {
	PlanVersion string          `json:"planVersion"`
	Metadata    PlannerMetadata `json:"metadata"`
	Chains      []PlannerChain  `json:"chains,omitempty"`
	Knights     []PlannerKnight `json:"knights,omitempty"`
	Skills      []PlannerSkill  `json:"skills,omitempty"`
}

type PlannerMetadata struct {
	Objective         string `json:"objective"`
	Reasoning         string `json:"reasoning,omitempty"`
	EstimatedDuration string `json:"estimatedDuration,omitempty"`
	EstimatedCost     string `json:"estimatedCost,omitempty"`
}

type PlannerChain struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description,omitempty"`
	Phase       string                      `json:"phase,omitempty"`
	Steps       []aiv1alpha1.ChainStep      `json:"steps"`
	Input       string                      `json:"input,omitempty"`
	Timeout     *int32                      `json:"timeout,omitempty"`
	RetryPolicy *aiv1alpha1.ChainRetryPolicy `json:"retryPolicy,omitempty"`
}

type PlannerKnight struct {
	Name          string                             `json:"name"`
	Role          string                             `json:"role,omitempty"`
	Ephemeral     bool                               `json:"ephemeral"`
	TemplateRef   string                             `json:"templateRef,omitempty"`
	EphemeralSpec *aiv1alpha1.KnightSpec             `json:"ephemeralSpec,omitempty"`
	SpecOverrides *aiv1alpha1.KnightSpecOverrides    `json:"specOverrides,omitempty"`
}

type PlannerSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Content     string `json:"content,omitempty"`
	Source      *struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256,omitempty"`
	} `json:"source,omitempty"`
	Enabled bool `json:"enabled,omitempty"`
}

// reconcilePlanning handles the Planning phase for meta-missions.
// ADD TO RECONCILE SWITCH CASE between Provisioning and Assembling
func (r *MissionReconciler) reconcilePlanning(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Skip if not a meta-mission — transition directly to Assembling
	if !mission.Spec.MetaMission {
		log.Info("Not a meta-mission, skipping Planning phase")
		mission.Status.Phase = aiv1alpha1.MissionPhaseAssembling
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Auto-initialize Planner spec with defaults if metaMission but no explicit planner config
	if mission.Spec.Planner == nil {
		log.Info("MetaMission enabled without explicit planner config, using defaults")
		mission.Spec.Planner = &aiv1alpha1.MissionPlanner{
			Timeout:    600,
			MaxChains:  10,
			MaxKnights: 10,
		}
		// Find the built-in planner knight
		plannerKnights := &aiv1alpha1.KnightList{}
		if err := r.List(ctx, plannerKnights, client.InNamespace(mission.Namespace), client.MatchingLabels{"ai.roundtable.io/role": "planner"}); err == nil && len(plannerKnights.Items) > 0 {
			mission.Spec.Planner.KnightRef = plannerKnights.Items[0].Name
			log.Info("Auto-discovered planner knight", "name", plannerKnights.Items[0].Name)
		}
		if err := r.Update(ctx, mission); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to initialize planner config: %w", err)
		}
	}

	// Initialize planning result if needed
	if mission.Status.PlanningResult == nil {
		log.Info("Initializing planning phase")
		mission.Status.PlanningResult = &aiv1alpha1.PlanningResult{}
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	pr := mission.Status.PlanningResult

	// Check if planning already completed
	if pr.CompletedAt != nil {
		log.Info("Planning already complete, transitioning to Assembling",
			"chains", pr.ChainsGenerated,
			"knights", pr.KnightsGenerated)
		mission.Status.Phase = aiv1alpha1.MissionPhaseAssembling
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Check for planning error (terminal state)
	if pr.Error != "" {
		log.Error(fmt.Errorf("%s", pr.Error), "Planning failed, marking mission as failed")
		mission.Status.Phase = aiv1alpha1.MissionPhaseFailed
		mission.Status.Result = fmt.Sprintf("Planning failed: %s", pr.Error)
		mission.Status.ObservedGeneration = mission.Generation
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Check timeout
	timeout := mission.Spec.Planner.Timeout
	if timeout == 0 {
		timeout = 300 // Default 5 minutes
	}
	elapsed := time.Since(mission.Status.StartedAt.Time)
	if elapsed > time.Duration(timeout)*time.Second {
		log.Error(fmt.Errorf("planning timeout"), "Planning exceeded timeout",
			"timeout", timeout,
			"elapsed", elapsed)
		pr.Error = fmt.Sprintf("planning timeout after %d seconds", timeout)
		now := metav1.Now()
		pr.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Ensure planner knight exists
	plannerKnight, err := r.ensurePlannerKnight(ctx, mission)
	if err != nil {
		log.Error(err, "Failed to ensure planner knight")
		pr.Error = fmt.Sprintf("failed to create planner knight: %v", err)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.Status().Update(ctx, mission)
	}

	// Wait for planner knight to be ready
	if plannerKnight.Status.Phase != aiv1alpha1.KnightPhaseReady {
		log.Info("Waiting for planner knight to be ready",
			"knight", plannerKnight.Name,
			"phase", plannerKnight.Status.Phase)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Dispatch planning task if not already dispatched
	if mission.Status.PlanningTaskID == "" {
		taskID, err := r.dispatchPlanningTask(ctx, mission, plannerKnight)
		if err != nil {
			log.Error(err, "Failed to dispatch planning task")
			pr.Error = fmt.Sprintf("failed to dispatch planning task: %v", err)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, r.Status().Update(ctx, mission)
		}
		log.Info("Dispatched planning task", "taskID", taskID, "knight", plannerKnight.Name)
		mission.Status.PlanningTaskID = taskID
		return ctrl.Result{RequeueAfter: 5 * time.Second}, r.Status().Update(ctx, mission)
	}

	// Poll for planning result
	taskID := mission.Status.PlanningTaskID

	result, err := r.pollPlanningResult(ctx, mission, taskID)
	if err != nil {
		log.Error(err, "Failed to poll planning result")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Still waiting for result
	if result == nil {
		log.V(1).Info("Waiting for planning result", "taskID", taskID)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Check for task error
	if taskErr := result.GetError(); taskErr != "" {
		log.Error(fmt.Errorf("%s", taskErr), "Planner knight returned error")
		pr.Error = fmt.Sprintf("planner error: %s", taskErr)
		pr.RawOutput = truncate(result.GetOutput(), 10000)
		now := metav1.Now()
		pr.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Parse planner output
	output := result.GetOutput()
	plan, err := r.parsePlannerOutput(output)
	if err != nil {
		log.Error(err, "Failed to parse planner output")
		pr.Error = fmt.Sprintf("failed to parse planner output: %v", err)
		pr.RawOutput = truncate(output, 10000)
		now := metav1.Now()
		pr.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Validate plan
	if err := r.validatePlan(ctx, mission, plan); err != nil {
		log.Error(err, "Plan validation failed")
		pr.Error = fmt.Sprintf("plan validation failed: %v", err)
		pr.RawOutput = truncate(output, 10000)
		now := metav1.Now()
		pr.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Apply plan to mission spec
	if err := r.applyPlan(ctx, mission, plan); err != nil {
		log.Error(err, "Failed to apply plan")
		pr.Error = fmt.Sprintf("failed to apply plan: %v", err)
		now := metav1.Now()
		pr.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, mission)
	}

	// Mark planning complete
	now := metav1.Now()
	pr.CompletedAt = &now
	pr.ChainsGenerated = int32(len(plan.Chains))
	pr.KnightsGenerated = int32(len(plan.Knights))
	pr.SkillsGenerated = int32(len(plan.Skills))
	pr.RawOutput = truncate(output, 10000)

	log.Info("Planning completed successfully",
		"chains", pr.ChainsGenerated,
		"knights", pr.KnightsGenerated,
		"skills", pr.SkillsGenerated)

	// Clear temporary annotation
	delete(mission.Annotations, "ai.roundtable.io/planning-task-id")

	// Update spec and status together
	if err := r.Update(ctx, mission); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.Status().Update(ctx, mission)
}

// ensurePlannerKnight creates or retrieves the planner knight.
func (r *MissionReconciler) ensurePlannerKnight(ctx context.Context, mission *aiv1alpha1.Mission) (*aiv1alpha1.Knight, error) {
	log := logf.FromContext(ctx)
	planner := mission.Spec.Planner

	// If knightRef provided, fetch existing knight
	if planner.KnightRef != "" && planner.TemplateRef == "" && planner.EphemeralSpec == nil {
		knight := &aiv1alpha1.Knight{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      planner.KnightRef,
			Namespace: mission.Namespace,
		}, knight)
		if err != nil {
			return nil, fmt.Errorf("planner knight %q not found: %w", planner.KnightRef, err)
		}
		log.Info("Using existing planner knight", "knight", knight.Name)
		return knight, nil
	}

	// Create ephemeral planner knight
	plannerKnightName := fmt.Sprintf("%s-planner", mission.Name)

	// Check if already exists
	knight := &aiv1alpha1.Knight{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      plannerKnightName,
		Namespace: mission.Namespace,
	}, knight)
	if err == nil {
		log.Info("Planner knight already exists", "knight", knight.Name)
		return knight, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	// Resolve spec from template or inline
	var spec *aiv1alpha1.KnightSpec
	if planner.TemplateRef != "" {
		// Fetch from RoundTable templates
		rt, err := r.resolveRoundTable(ctx, mission)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve RoundTable: %w", err)
		}
		
		// Check mission templates first (local override)
		found := false
		for _, template := range mission.Spec.KnightTemplates {
			if template.Name == planner.TemplateRef {
				spec = template.Spec.DeepCopy()
				found = true
				log.Info("Using mission template for planner", "template", planner.TemplateRef)
				break
			}
		}
		
		// Fall back to RoundTable templates
		if !found {
			templateSpec, ok := rt.Spec.KnightTemplates[planner.TemplateRef]
			if !ok {
				return nil, fmt.Errorf("planner template %q not found in mission or RoundTable", planner.TemplateRef)
			}
			spec = &templateSpec
			log.Info("Using RoundTable template for planner", "template", planner.TemplateRef)
		}
	} else if planner.EphemeralSpec != nil {
		spec = planner.EphemeralSpec.DeepCopy()
		log.Info("Using inline spec for planner")
	} else {
		return nil, fmt.Errorf("planner must have knightRef, templateRef, or ephemeralSpec")
	}

	// Create knight CR
	knight = &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      plannerKnightName,
			Namespace: mission.Namespace,
			Labels: map[string]string{
				aiv1alpha1.LabelMission:   mission.Name,
				aiv1alpha1.LabelEphemeral: "true",
				"ai.roundtable.io/role":   "planner",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
			},
		},
		Spec: *spec,
	}

	if err := r.Create(ctx, knight); err != nil {
		return nil, fmt.Errorf("failed to create planner knight: %w", err)
	}

	log.Info("Created ephemeral planner knight", "knight", knight.Name)
	return knight, nil
}

// dispatchPlanningTask sends the planning task to the planner knight via NATS.
func (r *MissionReconciler) dispatchPlanningTask(ctx context.Context, mission *aiv1alpha1.Mission, plannerKnight *aiv1alpha1.Knight) (string, error) {
	log := logf.FromContext(ctx)

	if err := r.ensureNATS(ctx); err != nil {
		return "", err
	}

	// Generate unique task ID
	taskID := fmt.Sprintf("planning-%s-%s", mission.Name, uuid.New().String()[:8])

	// Build planning prompt
	prompt := r.buildPlanningPrompt(mission)

	// Construct task payload
	payload := natspkg.TaskPayload{
		TaskID: taskID,
		Task:   prompt,
	}

	// Publish to planner knight's task subject.
	// For built-in (non-ephemeral) planner knights, derive the prefix from
	// the knight's NATS subjects (e.g. "fleet-a.tasks.operator.>" → "fleet-a").
	// Ephemeral planners use the mission's NATS prefix.
	prefix := natsPrefix(mission)
	if plannerKnight.Spec.NATS.Subjects != nil && len(plannerKnight.Spec.NATS.Subjects) > 0 {
		// Extract prefix from first subject: "fleet-a.tasks.domain.>" → "fleet-a"
		parts := strings.SplitN(plannerKnight.Spec.NATS.Subjects[0], ".tasks.", 2)
		if len(parts) == 2 {
			prefix = parts[0]
		}
	}
	subject := natspkg.TaskSubject(prefix, plannerKnight.Spec.Domain, plannerKnight.Name)

	if err := r.natsClient.PublishJSON(subject, payload); err != nil {
		return "", fmt.Errorf("failed to publish planning task: %w", err)
	}

	log.Info("Published planning task",
		"taskID", taskID,
		"subject", subject,
		"knight", plannerKnight.Name)

	return taskID, nil
}

// buildPlanningPrompt constructs the planning prompt for the planner knight.
func (r *MissionReconciler) buildPlanningPrompt(mission *aiv1alpha1.Mission) string {
	var sb strings.Builder

	sb.WriteString("You are a mission planner for the Round Table AI agent orchestration system. ")
	sb.WriteString("Your task is to generate a detailed execution plan for the following mission.\n\n")

	sb.WriteString("**Mission Objective:**\n")
	sb.WriteString(mission.Spec.Objective)
	sb.WriteString("\n\n")

	if mission.Spec.SuccessCriteria != "" {
		sb.WriteString("**Success Criteria:**\n")
		sb.WriteString(mission.Spec.SuccessCriteria)
		sb.WriteString("\n\n")
	}

	if mission.Spec.Planner.Context != "" {
		sb.WriteString("**Additional Context:**\n")
		sb.WriteString(mission.Spec.Planner.Context)
		sb.WriteString("\n\n")
	}

	// List available resources
	sb.WriteString("**Available Resources:**\n")

	// Mission knight templates
	if len(mission.Spec.KnightTemplates) > 0 {
		sb.WriteString("Mission Knight Templates:\n")
		for _, t := range mission.Spec.KnightTemplates {
			sb.WriteString(fmt.Sprintf("- %s: domain=%s, skills=%v\n",
				t.Name, t.Spec.Domain, t.Spec.Skills))
		}
	}

	// Existing knights (if recruiting allowed)
	if mission.Spec.RecruitExisting && len(mission.Spec.Knights) > 0 {
		sb.WriteString("Existing Knights:\n")
		for _, k := range mission.Spec.Knights {
			if !k.Ephemeral {
				sb.WriteString(fmt.Sprintf("- %s (role: %s)\n", k.Name, k.Role))
			}
		}
	}

	sb.WriteString("\n")

	// Constraints
	sb.WriteString("**Constraints:**\n")
	if mission.Spec.Planner.MaxChains > 0 {
		sb.WriteString(fmt.Sprintf("- Maximum Chains: %d\n", mission.Spec.Planner.MaxChains))
	}
	if mission.Spec.Planner.MaxKnights > 0 {
		sb.WriteString(fmt.Sprintf("- Maximum Knights: %d\n", mission.Spec.Planner.MaxKnights))
	}
	if !mission.Spec.Planner.AllowSkillGeneration {
		sb.WriteString("- Skill Generation: Not allowed (use existing skills only)\n")
	}
	if mission.Spec.Timeout > 0 {
		sb.WriteString(fmt.Sprintf("- Mission Timeout: %d seconds\n", mission.Spec.Timeout))
	}
	if mission.Spec.CostBudgetUSD != "" && mission.Spec.CostBudgetUSD != "0" {
		sb.WriteString(fmt.Sprintf("- Cost Budget: $%s USD\n", mission.Spec.CostBudgetUSD))
	}

	sb.WriteString("\n")

	// Instructions
	sb.WriteString("**Instructions:**\n")
	sb.WriteString("Generate a complete execution plan in JSON format with the following structure:\n")
	sb.WriteString("```json\n")
	sb.WriteString(`{
  "planVersion": "v1alpha1",
  "metadata": {
    "objective": "Echo the mission objective here for validation",
    "reasoning": "Brief explanation of your planning strategy"
  },
  "knights": [
    {
      "name": "knight-name",
      "role": "description of role",
      "ephemeral": true,
      "templateRef": "template-name",
      "specOverrides": {
        "skills": ["skill1", "skill2"],
        "model": "claude-sonnet-4-20250514"
      }
    }
  ],
  "chains": [
    {
      "name": "chain-name",
      "description": "what this chain does",
      "phase": "Active",
      "steps": [
        {
          "name": "step-name",
          "knightRef": "knight-name",
          "task": "detailed task description",
          "timeout": 300
        }
      ],
      "timeout": 1800
    }
  ]
}
`)
	sb.WriteString("```\n\n")

	sb.WriteString("**Important Guidelines:**\n")
	sb.WriteString("1. All knightRef values in chain steps must match knight names in the knights array\n")
	sb.WriteString("2. Use templateRef to reference existing knight templates when possible\n")
	sb.WriteString("3. Chain phases can be: Setup, Active, or Teardown\n")
	sb.WriteString("4. Steps can use Go template syntax like {{ .Steps.stepName.Output }} to pass data\n")
	sb.WriteString("5. Ensure step dependencies (dependsOn) form a valid DAG (no cycles)\n")
	sb.WriteString("6. Keep task descriptions clear and actionable\n")
	sb.WriteString("7. Return ONLY the JSON plan, no other text\n\n")

	sb.WriteString("Generate the plan now:")

	return sb.String()
}

// pollPlanningResult polls the NATS results stream for the planning result.
// The planner knight publishes results to its own table's results stream,
// so we need to resolve the correct stream and prefix from the planner knight's config.
func (r *MissionReconciler) pollPlanningResult(ctx context.Context, mission *aiv1alpha1.Mission, taskID string) (*natspkg.TaskResult, error) {
	log := logf.FromContext(ctx)

	if err := r.ensureNATS(ctx); err != nil {
		return nil, err
	}

	// Resolve the planner knight to determine which results stream to poll
	plannerKnight, err := r.ensurePlannerKnight(ctx, mission)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve planner knight: %w", err)
	}

	// Derive the results stream and subject prefix from the planner knight's NATS config.
	// The planner publishes to its own table's results stream, not the mission's.
	resultsStream := plannerKnight.Spec.NATS.ResultsStream
	subjectPrefix := "fleet-a" // fallback
	if len(plannerKnight.Spec.NATS.Subjects) > 0 {
		// Extract prefix from first subject: "fleet-a.tasks.domain.>" → "fleet-a"
		parts := strings.SplitN(plannerKnight.Spec.NATS.Subjects[0], ".tasks.", 2)
		if len(parts) == 2 {
			subjectPrefix = parts[0]
		}
	}

	subject := natspkg.ResultSubject(subjectPrefix, taskID)
	consumerName := fmt.Sprintf("mission-planner-%s", mission.Name)

	log.V(1).Info("Polling for planning result",
		"taskID", taskID,
		"stream", resultsStream,
		"subject", subject,
		"consumer", consumerName)

	msg, err := r.natsClient.PollMessage(subject, 2*time.Second,
		natspkg.WithDurable(consumerName),
		natspkg.WithAckExplicit(),
		natspkg.WithBindStream(resultsStream),
		natspkg.WithDeliverAll(),
		natspkg.WithFallbackAutoDetect(),
	)

	if err != nil {
		log.V(1).Info("Planning result not yet available", "taskID", taskID, "error", err.Error())
		return nil, nil
	}
	if msg == nil {
		log.V(1).Info("Planning result not yet available", "taskID", taskID)
		return nil, nil
	}

	// Got the result — ack and clean up the consumer
	if err := msg.Ack(); err != nil {
		log.Error(err, "Failed to ack planning result message")
	}
	_ = r.natsClient.DeleteConsumer(resultsStream, consumerName)

	// Parse result
	var taskResult natspkg.TaskResult
	if err := json.Unmarshal(msg.Data, &taskResult); err != nil {
		return nil, fmt.Errorf("failed to parse planning result: %w", err)
	}

	log.Info("Retrieved planning result from stream",
		"taskID", taskID,
		"stream", resultsStream,
		"outputLen", len(taskResult.GetOutput()))
	return &taskResult, nil
}

// parsePlannerOutput parses the JSON output from the planner knight.
func (r *MissionReconciler) parsePlannerOutput(output string) (*PlannerOutput, error) {
	// Try to extract JSON if it's wrapped in markdown code blocks
	output = extractJSON(output)

	var plan PlannerOutput
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return &plan, nil
}

// extractJSON extracts JSON from markdown code blocks if present.
func extractJSON(s string) string {
	// Look for ```json ... ``` or ``` ... ```
	if idx := strings.Index(s, "```json"); idx >= 0 {
		s = s[idx+7:]
		if idx := strings.Index(s, "```"); idx >= 0 {
			return strings.TrimSpace(s[:idx])
		}
	}
	if idx := strings.Index(s, "```"); idx >= 0 {
		s = s[idx+3:]
		if idx := strings.Index(s, "```"); idx >= 0 {
			return strings.TrimSpace(s[:idx])
		}
	}
	return strings.TrimSpace(s)
}

// validatePlan validates the planner output against schema and constraints.
func (r *MissionReconciler) validatePlan(ctx context.Context, mission *aiv1alpha1.Mission, plan *PlannerOutput) error {
	log := logf.FromContext(ctx)

	// Validate plan version
	if plan.PlanVersion != "v1alpha1" {
		return fmt.Errorf("unsupported plan version: %s (expected v1alpha1)", plan.PlanVersion)
	}

	// Validate metadata
	if plan.Metadata.Objective == "" {
		return fmt.Errorf("metadata.objective is required")
	}

	// Validate at least one chain or knight
	if len(plan.Chains) == 0 && len(plan.Knights) == 0 {
		return fmt.Errorf("plan must contain at least one chain or knight")
	}

	// Validate chain count against limits
	maxChains := mission.Spec.Planner.MaxChains
	if maxChains == 0 {
		maxChains = 5
	}
	if int32(len(plan.Chains)) > maxChains {
		return fmt.Errorf("too many chains: %d > %d", len(plan.Chains), maxChains)
	}

	// Validate knight count against limits
	maxKnights := mission.Spec.Planner.MaxKnights
	if maxKnights == 0 {
		maxKnights = 10
	}
	if int32(len(plan.Knights)) > maxKnights {
		return fmt.Errorf("too many knights: %d > %d", len(plan.Knights), maxKnights)
	}

	// Validate skill generation if any skills present
	if len(plan.Skills) > 0 && !mission.Spec.Planner.AllowSkillGeneration {
		return fmt.Errorf("skill generation not allowed but plan includes %d skills", len(plan.Skills))
	}
	if len(plan.Skills) > 10 {
		return fmt.Errorf("too many skills: %d > 10 (hard limit)", len(plan.Skills))
	}

	// Build knight name map for validation
	knightNames := make(map[string]bool)
	for _, k := range plan.Knights {
		if k.Name == "" {
			return fmt.Errorf("knight name is required")
		}
		if knightNames[k.Name] {
			return fmt.Errorf("duplicate knight name: %s", k.Name)
		}
		knightNames[k.Name] = true

		// Validate knight has either templateRef or ephemeralSpec
		if !k.Ephemeral {
			// Non-ephemeral knights must already exist
			knight := &aiv1alpha1.Knight{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      k.Name,
				Namespace: mission.Namespace,
			}, knight)
			if err != nil {
				return fmt.Errorf("non-ephemeral knight %q not found: %w", k.Name, err)
			}
		} else {
			// Ephemeral knights need templateRef or ephemeralSpec
			if k.TemplateRef == "" && k.EphemeralSpec == nil {
				return fmt.Errorf("ephemeral knight %q must have templateRef or ephemeralSpec", k.Name)
			}

			// If templateRef, verify template exists
			if k.TemplateRef != "" {
				if err := r.validateTemplateExists(ctx, mission, k.TemplateRef); err != nil {
					return fmt.Errorf("knight %q: %w", k.Name, err)
				}
			}
		}

		// Validate knight name is RFC 1123 compliant
		if !isValidK8sName(k.Name) {
			return fmt.Errorf("invalid knight name %q: must be RFC 1123 DNS label", k.Name)
		}
	}

	// Validate chains
	chainNames := make(map[string]bool)
	for i, chain := range plan.Chains {
		if chain.Name == "" {
			return fmt.Errorf("chain[%d]: name is required", i)
		}
		if chainNames[chain.Name] {
			return fmt.Errorf("duplicate chain name: %s", chain.Name)
		}
		chainNames[chain.Name] = true

		// Validate chain name
		if !isValidK8sName(chain.Name) {
			return fmt.Errorf("invalid chain name %q: must be RFC 1123 DNS label", chain.Name)
		}

		// Validate phase
		if chain.Phase != "" && chain.Phase != "Setup" && chain.Phase != "Active" && chain.Phase != "Teardown" {
			return fmt.Errorf("chain %q: invalid phase %q (must be Setup, Active, or Teardown)", chain.Name, chain.Phase)
		}

		// Validate steps
		if len(chain.Steps) == 0 {
			return fmt.Errorf("chain %q: at least one step is required", chain.Name)
		}

		// Validate each step
		stepNames := make(map[string]bool)
		for j, step := range chain.Steps {
			if step.Name == "" {
				return fmt.Errorf("chain %q step[%d]: name is required", chain.Name, j)
			}
			if stepNames[step.Name] {
				return fmt.Errorf("chain %q: duplicate step name %q", chain.Name, step.Name)
			}
			stepNames[step.Name] = true

			if step.KnightRef == "" {
				return fmt.Errorf("chain %q step %q: knightRef is required", chain.Name, step.Name)
			}
			if !knightNames[step.KnightRef] {
				return fmt.Errorf("chain %q step %q: knight %q not found in plan", chain.Name, step.Name, step.KnightRef)
			}
			if step.Task == "" {
				return fmt.Errorf("chain %q step %q: task is required", chain.Name, step.Name)
			}
		}

		// Validate DAG (no cycles in dependencies)
		if err := validateDAG(chain.Steps); err != nil {
			return fmt.Errorf("chain %q: %w", chain.Name, err)
		}
	}

	// Validate skills
	skillNames := make(map[string]bool)
	for i, skill := range plan.Skills {
		if skill.Name == "" {
			return fmt.Errorf("skill[%d]: name is required", i)
		}
		if skillNames[skill.Name] {
			return fmt.Errorf("duplicate skill name: %s", skill.Name)
		}
		skillNames[skill.Name] = true

		// Validate skill name is safe
		if !isValidSkillName(skill.Name) {
			return fmt.Errorf("invalid skill name %q: must be alphanumeric with hyphens", skill.Name)
		}
	}

	log.Info("Plan validation passed",
		"chains", len(plan.Chains),
		"knights", len(plan.Knights),
		"skills", len(plan.Skills))

	return nil
}

// validateTemplateExists checks if a knight template exists in mission or RoundTable.
func (r *MissionReconciler) validateTemplateExists(ctx context.Context, mission *aiv1alpha1.Mission, templateName string) error {
	// Check mission templates
	for _, t := range mission.Spec.KnightTemplates {
		if t.Name == templateName {
			return nil
		}
	}

	// Check RoundTable templates
	rt, err := r.resolveRoundTable(ctx, mission)
	if err != nil {
		return fmt.Errorf("failed to resolve RoundTable: %w", err)
	}

	if _, ok := rt.Spec.KnightTemplates[templateName]; ok {
		return nil
	}

	return fmt.Errorf("template %q not found in mission or RoundTable", templateName)
}

// validateDAG checks that step dependencies form a valid directed acyclic graph.
func validateDAG(steps []aiv1alpha1.ChainStep) error {
	// Build dependency graph
	graph := make(map[string][]string)
	stepSet := make(map[string]bool)

	for _, step := range steps {
		stepSet[step.Name] = true
		for _, dep := range step.DependsOn {
			graph[dep] = append(graph[dep], step.Name)
		}
	}

	// Verify all dependencies exist
	for _, step := range steps {
		for _, dep := range step.DependsOn {
			if !stepSet[dep] {
				return fmt.Errorf("step %q depends on unknown step %q", step.Name, dep)
			}
		}
	}

	// Detect cycles using DFS
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(node string) bool {
		visited[node] = true
		recStack[node] = true

		for _, neighbor := range graph[node] {
			if !visited[neighbor] {
				if hasCycle(neighbor) {
					return true
				}
			} else if recStack[neighbor] {
				return true
			}
		}

		recStack[node] = false
		return false
	}

	for step := range stepSet {
		if !visited[step] {
			if hasCycle(step) {
				return fmt.Errorf("circular dependency detected in steps")
			}
		}
	}

	return nil
}

// applyPlan applies the validated plan to the mission spec.
func (r *MissionReconciler) applyPlan(ctx context.Context, mission *aiv1alpha1.Mission, plan *PlannerOutput) error {
	log := logf.FromContext(ctx)

	// Convert planner knights to MissionKnight format
	for _, pk := range plan.Knights {
		mk := aiv1alpha1.MissionKnight{
			Name:          pk.Name,
			Role:          pk.Role,
			Ephemeral:     pk.Ephemeral,
			TemplateRef:   pk.TemplateRef,
			EphemeralSpec: pk.EphemeralSpec,
			SpecOverrides: pk.SpecOverrides,
		}
		mission.Spec.GeneratedKnights = append(mission.Spec.GeneratedKnights, mk)
	}

	// Convert planner chains to GeneratedChain format and create Chain CRs
	for _, pc := range plan.Chains {
		gc := aiv1alpha1.GeneratedChain{
			Name:        pc.Name,
			Description: pc.Description,
			Steps:       pc.Steps,
			Phase:       pc.Phase,
			Input:       pc.Input,
			Timeout:     pc.Timeout,
			RetryPolicy: pc.RetryPolicy,
		}

		// Set default phase if not specified
		if gc.Phase == "" {
			gc.Phase = "Active"
		}

		mission.Spec.GeneratedChains = append(mission.Spec.GeneratedChains, gc)

		// Create Chain CR
		chainName := fmt.Sprintf("%s-%s", mission.Name, pc.Name)
		chain := &aiv1alpha1.Chain{
			ObjectMeta: metav1.ObjectMeta{
				Name:      chainName,
				Namespace: mission.Namespace,
				Labels: map[string]string{
					aiv1alpha1.LabelMission:   mission.Name,
					aiv1alpha1.LabelEphemeral: "true",
					"ai.roundtable.io/generated-by": "planner",
				},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
				},
			},
			Spec: aiv1alpha1.ChainSpec{
				Description: pc.Description,
				Steps:       pc.Steps,
				Input:       pc.Input,
				MissionRef:  mission.Name,
			},
		}

		// Set timeout if specified
		if pc.Timeout != nil {
			chain.Spec.Timeout = *pc.Timeout
		}

		// Set retry policy if specified
		if pc.RetryPolicy != nil {
			chain.Spec.RetryPolicy = pc.RetryPolicy
		}

		// Create or update chain
		if err := r.Create(ctx, chain); err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				return fmt.Errorf("failed to create chain %q: %w", chainName, err)
			}
			log.Info("Chain already exists, skipping", "chain", chainName)
		} else {
			log.Info("Created chain CR", "chain", chainName, "steps", len(pc.Steps))
		}

		// Add chain reference to mission spec
		mission.Spec.Chains = append(mission.Spec.Chains, aiv1alpha1.MissionChainRef{
			Name:  pc.Name,
			Phase: pc.Phase,
		})
	}

	// Handle skill generation if present (stored as ConfigMaps)
	if len(plan.Skills) > 0 {
		if err := r.createSkillConfigMaps(ctx, mission, plan.Skills); err != nil {
			log.Error(err, "Failed to create skill ConfigMaps (non-fatal)")
			// Don't fail the whole plan for skill creation issues
		}
	}

	log.Info("Successfully applied plan to mission",
		"generatedKnights", len(mission.Spec.GeneratedKnights),
		"generatedChains", len(mission.Spec.GeneratedChains),
		"chains", len(mission.Spec.Chains))

	return nil
}

// createSkillConfigMaps creates ConfigMaps for generated skills.
func (r *MissionReconciler) createSkillConfigMaps(ctx context.Context, mission *aiv1alpha1.Mission, skills []PlannerSkill) error {
	log := logf.FromContext(ctx)

	for _, skill := range skills {
		cmName := fmt.Sprintf("%s-skill-%s", mission.Name, skill.Name)

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: mission.Namespace,
				Labels: map[string]string{
					aiv1alpha1.LabelMission:   mission.Name,
					aiv1alpha1.LabelEphemeral: "true",
					"ai.roundtable.io/skill":  skill.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(mission, aiv1alpha1.GroupVersion.WithKind("Mission")),
				},
			},
			Data: map[string]string{
				"skill.sh":    skill.Content,
				"description": skill.Description,
			},
		}

		if err := r.Create(ctx, cm); err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				return fmt.Errorf("failed to create skill ConfigMap %q: %w", cmName, err)
			}
			log.Info("Skill ConfigMap already exists", "name", cmName)
		} else {
			log.Info("Created skill ConfigMap", "name", cmName, "skill", skill.Name)
		}
	}

	return nil
}

// isValidK8sName validates a name against RFC 1123 DNS label rules.
func isValidK8sName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		return false
	}
	return true
}

// isValidSkillName validates a skill name (alphanumeric with hyphens).
func isValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' {
			continue
		}
		return false
	}
	return true
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

// resolveRoundTable resolves the RoundTable for this mission.
// MUST BE IMPLEMENTED IN mission_controller.go - this is a placeholder
func (r *MissionReconciler) resolveRoundTable(ctx context.Context, mission *aiv1alpha1.Mission) (*aiv1alpha1.RoundTable, error) {
	if mission.Spec.RoundTableRef == "" {
		return nil, fmt.Errorf("no RoundTable reference configured")
	}

	rt := &aiv1alpha1.RoundTable{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      mission.Spec.RoundTableRef,
		Namespace: mission.Namespace,
	}, rt)
	if err != nil {
		return nil, err
	}

	return rt, nil
}
