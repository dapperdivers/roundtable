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

// RoundTableSpec defines the desired state of a RoundTable — a fleet of knights
// with shared configuration and policies.
type RoundTableSpec struct {
	// description is a human-readable description of this round table's purpose.
	// +optional
	Description string `json:"description,omitempty"`

	// nats configures the shared NATS infrastructure for all knights in this table.
	// +kubebuilder:validation:Required
	NATS RoundTableNATS `json:"nats"`

	// defaults defines default configuration applied to all knights in this table.
	// Individual knight specs can override these values.
	// +optional
	Defaults *RoundTableDefaults `json:"defaults,omitempty"`

	// policies defines fleet-level operational policies.
	// +optional
	Policies *RoundTablePolicies `json:"policies,omitempty"`

	// knightSelector is a label selector for Knights that belong to this table.
	// Knights matching this selector are automatically managed by this RoundTable.
	// +optional
	KnightSelector *metav1.LabelSelector `json:"knightSelector,omitempty"`

	// secrets references shared secrets available to all knights in this table.
	// +optional
	Secrets []corev1.LocalObjectReference `json:"secrets,omitempty"`

	// vault configures the shared Obsidian vault for all knights in this table.
	// +optional
	Vault *KnightVault `json:"vault,omitempty"`

	// suspended, if true, suspends all knights in this table.
	// +kubebuilder:default=false
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// RoundTableNATS configures the shared NATS infrastructure for a round table.
type RoundTableNATS struct {
	// url is the NATS server URL.
	// +kubebuilder:default="nats://nats.database.svc:4222"
	// +optional
	URL string `json:"url,omitempty"`

	// subjectPrefix is the NATS subject prefix for this table (e.g., "fleet-a").
	// All knights in this table use subjects under this prefix.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	SubjectPrefix string `json:"subjectPrefix"`

	// tasksStream is the JetStream stream name for tasks.
	// +kubebuilder:validation:Required
	TasksStream string `json:"tasksStream"`

	// resultsStream is the JetStream stream name for results.
	// +kubebuilder:validation:Required
	ResultsStream string `json:"resultsStream"`

	// createStreams, if true, tells the controller to create/update the JetStream streams.
	// +kubebuilder:default=false
	// +optional
	CreateStreams bool `json:"createStreams,omitempty"`

	// streamRetention configures the retention policy for auto-created streams.
	// +kubebuilder:default="WorkQueue"
	// +kubebuilder:validation:Enum=Limits;Interest;WorkQueue
	// +optional
	StreamRetention string `json:"streamRetention,omitempty"`
}

// RoundTableDefaults defines default configuration inherited by knights in this table.
type RoundTableDefaults struct {
	// model is the default AI model for knights in this table.
	// +optional
	Model string `json:"model,omitempty"`

	// image is the default container image for knights.
	// +optional
	Image string `json:"image,omitempty"`

	// taskTimeout is the default task timeout in seconds.
	// +kubebuilder:default=120
	// +optional
	TaskTimeout int32 `json:"taskTimeout,omitempty"`

	// concurrency is the default max concurrent tasks per knight.
	// +kubebuilder:default=2
	// +optional
	Concurrency int32 `json:"concurrency,omitempty"`

	// resources defines default compute resource requirements.
	// +optional
	Resources *KnightResources `json:"resources,omitempty"`

	// arsenal configures the default skill arsenal for knights.
	// +optional
	Arsenal *KnightArsenal `json:"arsenal,omitempty"`
}

// RoundTablePolicies defines fleet-level operational policies.
type RoundTablePolicies struct {
	// maxConcurrentTasks is the maximum total concurrent tasks across all knights in this table.
	// 0 means unlimited.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxConcurrentTasks int32 `json:"maxConcurrentTasks,omitempty"`

	// costBudgetUSD is the maximum cumulative cost in USD across all knights.
	// When reached, all knights are suspended. "0" means unlimited.
	// +kubebuilder:default="0"
	// +optional
	CostBudgetUSD string `json:"costBudgetUSD,omitempty"`

	// costResetSchedule is a cron expression for resetting the cost counter (e.g., "0 0 1 * *" for monthly).
	// +optional
	CostResetSchedule string `json:"costResetSchedule,omitempty"`

	// maxKnights is the maximum number of knights allowed in this table.
	// 0 means unlimited.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxKnights int32 `json:"maxKnights,omitempty"`

	// maxMissions is the maximum number of concurrent active missions.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxMissions int32 `json:"maxMissions,omitempty"`
}

// RoundTablePhase represents the current lifecycle phase of the RoundTable.
// +kubebuilder:validation:Enum=Provisioning;Ready;Degraded;Suspended;OverBudget
type RoundTablePhase string

const (
	RoundTablePhaseProvisioning RoundTablePhase = "Provisioning"
	RoundTablePhaseReady        RoundTablePhase = "Ready"
	RoundTablePhaseDegraded     RoundTablePhase = "Degraded"
	RoundTablePhaseSuspended    RoundTablePhase = "Suspended"
	RoundTablePhaseOverBudget   RoundTablePhase = "OverBudget"
)

// RoundTableKnightSummary provides an aggregated view of a knight's status.
type RoundTableKnightSummary struct {
	// name is the knight name.
	Name string `json:"name"`

	// ready indicates whether this knight is ready.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// phase is the knight's current phase.
	// +optional
	Phase KnightPhase `json:"phase,omitempty"`
}

// RoundTableStatus defines the observed state of RoundTable.
type RoundTableStatus struct {
	// phase is the current lifecycle phase of the round table.
	// +optional
	Phase RoundTablePhase `json:"phase,omitempty"`

	// knightsReady is the number of knights in Ready phase.
	// +optional
	KnightsReady int32 `json:"knightsReady,omitempty"`

	// knightsTotal is the total number of knights in this table.
	// +optional
	KnightsTotal int32 `json:"knightsTotal,omitempty"`

	// knights provides a summary of each knight's status.
	// +optional
	Knights []RoundTableKnightSummary `json:"knights,omitempty"`

	// totalTasksCompleted is the aggregate tasks completed across all knights.
	// +optional
	TotalTasksCompleted int64 `json:"totalTasksCompleted,omitempty"`

	// totalCost is the aggregate cost in USD across all knights since last reset.
	// +optional
	TotalCost string `json:"totalCost,omitempty"`

	// activeMissions is the number of currently active missions under this table.
	// +optional
	ActiveMissions int32 `json:"activeMissions,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the RoundTable resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rt
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Knights",type=string,JSONPath=`.status.knightsReady`,description="Ready/Total knights"
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.knightsTotal`
// +kubebuilder:printcolumn:name="Tasks",type=integer,JSONPath=`.status.totalTasksCompleted`
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.status.totalCost`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RoundTable is the Schema for the roundtables API.
// A RoundTable represents a fleet of knights with shared configuration,
// NATS infrastructure, and operational policies. It is the top-level
// organizational resource in the Round Table operator.
type RoundTable struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RoundTable
	// +required
	Spec RoundTableSpec `json:"spec"`

	// status defines the observed state of RoundTable
	// +optional
	Status RoundTableStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RoundTableList contains a list of RoundTable
type RoundTableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RoundTable `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoundTable{}, &RoundTableList{})
}
