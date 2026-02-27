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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChainSpec defines the desired state of a Chain — a declarative multi-knight task pipeline.
type ChainSpec struct {
	// description is a human-readable summary of what this chain accomplishes.
	// +optional
	Description string `json:"description,omitempty"`

	// steps defines the ordered list of pipeline steps.
	// Steps execute sequentially unless parallel grouping is used via `parallel`.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Steps []ChainStep `json:"steps"`

	// timeout is the overall chain timeout in seconds. The entire chain is failed if exceeded.
	// +kubebuilder:default=600
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=86400
	// +optional
	Timeout int32 `json:"timeout,omitempty"`

	// schedule is an optional cron expression to trigger this chain on a recurring basis.
	// Uses standard cron syntax (e.g., "0 */6 * * *").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// input provides initial data passed to the first step(s) as JSON.
	// +optional
	Input string `json:"input,omitempty"`

	// roundTableRef references the RoundTable this chain belongs to.
	// If omitted, the chain operates in the default namespace NATS prefix.
	// +optional
	RoundTableRef string `json:"roundTableRef,omitempty"`

	// suspended, if true, prevents scheduled runs and disallows new executions.
	// +kubebuilder:default=false
	// +optional
	Suspended bool `json:"suspended,omitempty"`

	// retryPolicy configures retry behavior for failed steps.
	// +optional
	RetryPolicy *ChainRetryPolicy `json:"retryPolicy,omitempty"`
}

// ChainStep defines a single step in the pipeline.
type ChainStep struct {
	// name is a unique identifier for this step within the chain.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// knightRef is the name of the Knight to execute this step.
	// +kubebuilder:validation:Required
	KnightRef string `json:"knightRef"`

	// task is the task prompt or instruction to send to the knight.
	// Supports Go template syntax with access to prior step outputs: {{ .Steps.step_name.Output }}
	// +kubebuilder:validation:Required
	Task string `json:"task"`

	// dependsOn lists step names that must complete successfully before this step runs.
	// If empty, the step runs immediately (or after the previous step in sequence).
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// timeout is the per-step timeout in seconds. Overrides the knight's default taskTimeout.
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=3600
	// +optional
	Timeout int32 `json:"timeout,omitempty"`

	// outputKey is the key name under which this step's output is stored for downstream steps.
	// Defaults to the step name if not specified.
	// +optional
	OutputKey string `json:"outputKey,omitempty"`

	// continueOnFailure allows downstream steps to proceed even if this step fails.
	// +kubebuilder:default=false
	// +optional
	ContinueOnFailure bool `json:"continueOnFailure,omitempty"`
}

// ChainRetryPolicy configures retry behavior for failed steps.
type ChainRetryPolicy struct {
	// maxRetries is the maximum number of retries per step.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=5
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// backoffSeconds is the delay between retries in seconds.
	// +kubebuilder:default=30
	// +optional
	BackoffSeconds int32 `json:"backoffSeconds,omitempty"`
}

// ChainPhase represents the current lifecycle phase of the Chain.
// +kubebuilder:validation:Enum=Idle;Running;Succeeded;Failed;Suspended
type ChainPhase string

const (
	ChainPhaseIdle      ChainPhase = "Idle"
	ChainPhaseRunning   ChainPhase = "Running"
	ChainPhaseSucceeded ChainPhase = "Succeeded"
	ChainPhaseFailed    ChainPhase = "Failed"
	ChainPhaseSuspended ChainPhase = "Suspended"
)

// ChainStepPhase represents the status of an individual step.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped
type ChainStepPhase string

const (
	ChainStepPhasePending   ChainStepPhase = "Pending"
	ChainStepPhaseRunning   ChainStepPhase = "Running"
	ChainStepPhaseSucceeded ChainStepPhase = "Succeeded"
	ChainStepPhaseFailed    ChainStepPhase = "Failed"
	ChainStepPhaseSkipped   ChainStepPhase = "Skipped"
)

// ChainStepStatus tracks the execution status of an individual step.
type ChainStepStatus struct {
	// name matches the step name from the spec.
	Name string `json:"name"`

	// phase is the current execution phase of this step.
	// +optional
	Phase ChainStepPhase `json:"phase,omitempty"`

	// startedAt is when the step began execution.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the step finished execution.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// output is the result data from this step (truncated if large).
	// +optional
	Output string `json:"output,omitempty"`

	// error contains the error message if the step failed.
	// +optional
	Error string `json:"error,omitempty"`

	// retries is the number of retry attempts made.
	// +optional
	Retries int32 `json:"retries,omitempty"`
}

// ChainStatus defines the observed state of Chain.
type ChainStatus struct {
	// phase is the current lifecycle phase of the chain.
	// +optional
	Phase ChainPhase `json:"phase,omitempty"`

	// stepStatuses tracks the status of each step.
	// +optional
	StepStatuses []ChainStepStatus `json:"stepStatuses,omitempty"`

	// startedAt is when the current chain run began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the current chain run finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// runsCompleted is the total number of successful chain runs.
	// +optional
	RunsCompleted int64 `json:"runsCompleted,omitempty"`

	// runsFailed is the total number of failed chain runs.
	// +optional
	RunsFailed int64 `json:"runsFailed,omitempty"`

	// lastScheduledAt is when the chain was last triggered by its cron schedule.
	// +optional
	LastScheduledAt *metav1.Time `json:"lastScheduledAt,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the Chain resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Steps",type=integer,JSONPath=`.spec.steps`,priority=1
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.runsCompleted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Chain is the Schema for the chains API.
// A Chain represents a declarative multi-knight task pipeline in the Round Table,
// with support for parallel fan-out, sequential dependencies, data passing between
// steps, and optional cron scheduling.
type Chain struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Chain
	// +required
	Spec ChainSpec `json:"spec"`

	// status defines the observed state of Chain
	// +optional
	Status ChainStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ChainList contains a list of Chain
type ChainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Chain `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Chain{}, &ChainList{})
}
