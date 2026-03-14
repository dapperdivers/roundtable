package mission

import (
	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
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
