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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MissionSpec defines the desired state of a Mission — an ephemeral round table
// assembling knights for a specific objective.
type MissionSpec struct {
	// objective is the high-level goal of this mission.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Objective string `json:"objective"`

	// successCriteria defines how to determine whether the mission succeeded.
	// Can be a natural language description evaluated by a designated judge knight,
	// or structured criteria.
	// +optional
	SuccessCriteria string `json:"successCriteria,omitempty"`

	// knights lists the knights participating in this mission.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Knights []MissionKnight `json:"knights"`

	// chains lists chains to execute as part of this mission.
	// +optional
	Chains []MissionChainRef `json:"chains,omitempty"`

	// ttl is the mission's time-to-live in seconds. The mission is automatically
	// cleaned up after this duration, regardless of completion status.
	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=604800
	// +optional
	TTL int32 `json:"ttl,omitempty"`

	// timeout is the maximum time in seconds to wait for the mission objective
	// to be achieved before marking it as failed.
	// +kubebuilder:default=1800
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=86400
	// +optional
	Timeout int32 `json:"timeout,omitempty"`

	// natsPrefix overrides the NATS subject prefix for this mission.
	// Defaults to "mission-{name}".
	// +optional
	NATSPrefix string `json:"natsPrefix,omitempty"`

	// roundTableRef references the RoundTable this mission is conducted under.
	// +optional
	RoundTableRef string `json:"roundTableRef,omitempty"`

	// cleanupPolicy controls what happens to ephemeral resources after mission completion.
	// +kubebuilder:default="Delete"
	// +kubebuilder:validation:Enum=Delete;Retain
	// +optional
	CleanupPolicy string `json:"cleanupPolicy,omitempty"`

	// briefing is the initial context/instructions published to all mission knights
	// when the mission starts.
	// +optional
	Briefing string `json:"briefing,omitempty"`

	// knightTemplates defines reusable knight configurations that can be referenced
	// by MissionKnight entries. Allows defining a template once and instantiating
	// multiple ephemeral knights from it.
	// +optional
	KnightTemplates []MissionKnightTemplate `json:"knightTemplates,omitempty"`

	// costBudgetUSD is the maximum cost for this mission. When exceeded, the mission
	// is failed and cleanup begins. "0" means inherit from parent RoundTable.
	// +kubebuilder:default="0"
	// +optional
	CostBudgetUSD string `json:"costBudgetUSD,omitempty"`

	// secrets references secrets to mount into all ephemeral knight pods.
	// Used for mission-specific credentials (e.g., target system access).
	// +optional
	Secrets []corev1.LocalObjectReference `json:"secrets,omitempty"`

	// recruitExisting, if true, allows the mission to use non-ephemeral knights
	// from the parent RoundTable alongside ephemeral ones.
	// When false (default), only ephemeral knights participate.
	// +kubebuilder:default=false
	// +optional
	RecruitExisting bool `json:"recruitExisting,omitempty"`

	// roundTableTemplate overrides defaults for the ephemeral RoundTable created
	// for this mission. If nil, sensible defaults are used.
	// +optional
	RoundTableTemplate *MissionRoundTableTemplate `json:"roundTableTemplate,omitempty"`

	// retainResults, if true, copies mission results to a ConfigMap before cleanup.
	// The ConfigMap persists beyond mission deletion for post-mortem analysis.
	// +kubebuilder:default=true
	// +optional
	RetainResults bool `json:"retainResults,omitempty"`
}

// MissionKnight references a knight participating in a mission.
type MissionKnight struct {
	// name is the knight's name. If it matches an existing Knight CR, that knight is used.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// role describes this knight's role within the mission (e.g., "lead", "researcher", "reviewer").
	// +optional
	Role string `json:"role,omitempty"`

	// ephemeral, if true, creates a temporary Knight for this mission that is cleaned up on completion.
	// When true, ephemeralSpec must be provided.
	// +kubebuilder:default=false
	// +optional
	Ephemeral bool `json:"ephemeral,omitempty"`

	// ephemeralSpec defines the spec for an ephemeral knight. Only used when ephemeral=true.
	// +optional
	EphemeralSpec *KnightSpec `json:"ephemeralSpec,omitempty"`

	// templateRef references a MissionKnightTemplate by name.
	// Only used when ephemeral=true. Mutually exclusive with ephemeralSpec.
	// +optional
	TemplateRef string `json:"templateRef,omitempty"`

	// specOverrides allows patching specific fields when using templateRef.
	// Applied as a strategic merge patch on top of the template spec.
	// +optional
	SpecOverrides *KnightSpecOverrides `json:"specOverrides,omitempty"`
}

// MissionChainRef references a chain to execute within the mission.
type MissionChainRef struct {
	// name is the Chain CR name to execute.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// inputOverride provides mission-specific input data that overrides the chain's default input.
	// +optional
	InputOverride string `json:"inputOverride,omitempty"`

	// phase controls when in the mission lifecycle this chain runs.
	// +kubebuilder:default="Active"
	// +kubebuilder:validation:Enum=Setup;Active;Teardown
	// +optional
	Phase string `json:"phase,omitempty"`
}

// MissionPhase represents the current lifecycle phase of the Mission.
// +kubebuilder:validation:Enum=Pending;Provisioning;Assembling;Briefing;Active;Succeeded;Failed;Expired;CleaningUp
type MissionPhase string

const (
	MissionPhasePending      MissionPhase = "Pending"
	MissionPhaseProvisioning MissionPhase = "Provisioning"
	MissionPhaseAssembling   MissionPhase = "Assembling"
	MissionPhaseBriefing     MissionPhase = "Briefing"
	MissionPhaseActive       MissionPhase = "Active"
	MissionPhaseSucceeded    MissionPhase = "Succeeded"
	MissionPhaseFailed       MissionPhase = "Failed"
	MissionPhaseExpired      MissionPhase = "Expired"
	MissionPhaseCleaningUp   MissionPhase = "CleaningUp"
)

// MissionKnightStatus tracks the status of a knight within the mission.
type MissionKnightStatus struct {
	// name is the knight name.
	Name string `json:"name"`

	// ready indicates the knight is ready and connected to the mission NATS subjects.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// tasksCompleted is the number of tasks this knight completed during the mission.
	// +optional
	TasksCompleted int64 `json:"tasksCompleted,omitempty"`

	// ephemeral indicates whether this knight was created ephemerally for this mission.
	// +optional
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// MissionStatus defines the observed state of Mission.
type MissionStatus struct {
	// phase is the current lifecycle phase of the mission.
	// +optional
	Phase MissionPhase `json:"phase,omitempty"`

	// knightStatuses tracks the status of each participating knight.
	// +optional
	KnightStatuses []MissionKnightStatus `json:"knightStatuses,omitempty"`

	// startedAt is when the mission began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the mission finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// expiresAt is when the mission will be auto-cleaned based on TTL.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// result is a summary of the mission outcome.
	// +optional
	Result string `json:"result,omitempty"`

	// totalCost is the cumulative cost in USD of all tasks during this mission.
	// +optional
	TotalCost string `json:"totalCost,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the Mission resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// roundTableName is the name of the ephemeral RoundTable created for this mission.
	// +optional
	RoundTableName string `json:"roundTableName,omitempty"`

	// natsTasksStream is the JetStream stream name for mission tasks.
	// +optional
	NATSTasksStream string `json:"natsTasksStream,omitempty"`

	// natsResultsStream is the JetStream stream name for mission results.
	// +optional
	NATSResultsStream string `json:"natsResultsStream,omitempty"`

	// chainStatuses tracks the status of each mission chain.
	// +optional
	ChainStatuses []MissionChainStatus `json:"chainStatuses,omitempty"`

	// resultsConfigMap is the name of the ConfigMap containing preserved results
	// (only set when retainResults=true and mission is complete).
	// +optional
	ResultsConfigMap string `json:"resultsConfigMap,omitempty"`
}

// MissionKnightTemplate is a named, reusable knight spec template.
type MissionKnightTemplate struct {
	// name is the template name, referenced by MissionKnight.TemplateRef.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// spec is the knight spec to use when creating ephemeral knights from this template.
	// +kubebuilder:validation:Required
	Spec KnightSpec `json:"spec"`
}

// MissionRoundTableTemplate configures the ephemeral RoundTable.
type MissionRoundTableTemplate struct {
	// defaults overrides for the ephemeral table's knight defaults.
	// +optional
	Defaults *RoundTableDefaults `json:"defaults,omitempty"`

	// policies overrides for the ephemeral table's policies.
	// +optional
	Policies *RoundTablePolicies `json:"policies,omitempty"`

	// natsURL overrides the NATS server URL for the mission table.
	// +optional
	NATSURL string `json:"natsURL,omitempty"`
}

// KnightSpecOverrides allows selectively overriding template fields.
type KnightSpecOverrides struct {
	// model overrides the AI model.
	// +optional
	Model string `json:"model,omitempty"`

	// skills overrides the skill list.
	// +optional
	Skills []string `json:"skills,omitempty"`

	// env adds additional environment variables.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// prompt overrides prompt configuration.
	// +optional
	Prompt *KnightPrompt `json:"prompt,omitempty"`

	// concurrency overrides max concurrent tasks.
	// +optional
	Concurrency *int32 `json:"concurrency,omitempty"`
}

// MissionChainStatus tracks a chain's status within the mission.
type MissionChainStatus struct {
	// name is the chain reference name from the spec.
	Name string `json:"name"`

	// chainCRName is the actual Chain CR name created for this mission.
	// +optional
	ChainCRName string `json:"chainCRName,omitempty"`

	// phase is the chain's current phase.
	// +optional
	Phase ChainPhase `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Objective",type=string,JSONPath=`.spec.objective`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Knights",type=integer,JSONPath=`.spec.knights`,priority=1
// +kubebuilder:printcolumn:name="TTL",type=integer,JSONPath=`.spec.ttl`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Mission is the Schema for the missions API.
// A Mission represents an ephemeral round table — a temporary group of knights
// assembled for a specific objective, with time-bounded execution and automatic cleanup.
type Mission struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Mission
	// +required
	Spec MissionSpec `json:"spec"`

	// status defines the observed state of Mission
	// +optional
	Status MissionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MissionList contains a list of Mission
type MissionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Mission `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Mission{}, &MissionList{})
}
