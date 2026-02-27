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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KnightSpec defines the desired state of a Knight — an AI agent in the Round Table.
type KnightSpec struct {
	// domain is the knight's area of expertise (e.g., "security", "infrastructure", "finance").
	// Used for NATS subject routing and skill filtering.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`

	// model is the AI model to use (e.g., "claude-sonnet-4-20250514", "claude-haiku-35-20241022").
	// +kubebuilder:default="claude-sonnet-4-20250514"
	// +optional
	Model string `json:"model,omitempty"`

	// image is the container image for the knight runtime.
	// +kubebuilder:default="ghcr.io/dapperdivers/pi-knight:latest"
	// +optional
	Image string `json:"image,omitempty"`

	// skills defines which skill categories this knight has access to.
	// The operator will configure the skill-filter sidecar accordingly.
	// +kubebuilder:validation:MinItems=1
	Skills []string `json:"skills"`

	// tools defines additional system packages and tools the knight needs.
	// +optional
	Tools *KnightTools `json:"tools,omitempty"`

	// nats configures the knight's NATS JetStream consumer and subjects.
	// +kubebuilder:validation:Required
	NATS KnightNATS `json:"nats"`

	// vault configures the shared Obsidian vault mount.
	// +optional
	Vault *KnightVault `json:"vault,omitempty"`

	// prompt allows overriding the knight's system prompt components.
	// +optional
	Prompt *KnightPrompt `json:"prompt,omitempty"`

	// resources defines compute resource requirements for the knight container.
	// +optional
	Resources *KnightResources `json:"resources,omitempty"`

	// concurrency is the maximum number of concurrent tasks the knight can process.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +optional
	Concurrency int32 `json:"concurrency,omitempty"`

	// taskTimeout is the default task timeout in seconds.
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=3600
	// +optional
	TaskTimeout int32 `json:"taskTimeout,omitempty"`

	// env defines additional environment variables for the knight container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// envFrom defines sources of environment variables (secrets, configmaps).
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// arsenal configures the skill arsenal git-sync sidecar.
	// +optional
	Arsenal *KnightArsenal `json:"arsenal,omitempty"`

	// workspace configures the knight's persistent workspace.
	// +optional
	Workspace *KnightWorkspace `json:"workspace,omitempty"`

	// suspended, if true, scales the knight deployment to 0 replicas.
	// +kubebuilder:default=false
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// KnightArsenal configures the git-sync sidecar for the skill arsenal.
type KnightArsenal struct {
	// repo is the git repository URL containing skills.
	// +kubebuilder:default="https://github.com/dapperdivers/roundtable-arsenal"
	// +optional
	Repo string `json:"repo,omitempty"`

	// ref is the git ref to sync.
	// +kubebuilder:default="main"
	// +optional
	Ref string `json:"ref,omitempty"`

	// period is how often to sync (e.g., "300s").
	// +kubebuilder:default="300s"
	// +optional
	Period string `json:"period,omitempty"`

	// image overrides the git-sync container image.
	// +kubebuilder:default="registry.k8s.io/git-sync/git-sync:v4.4.0"
	// +optional
	Image string `json:"image,omitempty"`
}

// KnightWorkspace configures the knight's persistent workspace storage.
type KnightWorkspace struct {
	// existingClaim references an existing PVC to use instead of creating a new one.
	// Useful for migrating existing knights to operator management.
	// +optional
	ExistingClaim string `json:"existingClaim,omitempty"`

	// size is the storage request for auto-created PVCs.
	// +kubebuilder:default="1Gi"
	// +optional
	Size string `json:"size,omitempty"`
}

// KnightTools defines system-level tools the knight needs installed.
type KnightTools struct {
	// nix is a list of nixpkgs packages to install via Nix flakes (e.g., "nmap", "whois", "dnsutils").
	// These get compiled into a flake.nix and built on first boot, cached on the Nix PVC.
	// +optional
	Nix []string `json:"nix,omitempty"`

	// apt is a list of apt packages to install (fallback, requires root — prefer nix).
	// +optional
	Apt []string `json:"apt,omitempty"`

	// mise is a list of tools to install via mise (e.g., "shodan", "kubectl").
	// +optional
	Mise []string `json:"mise,omitempty"`
}

// KnightNATS configures the knight's NATS JetStream connection and consumers.
type KnightNATS struct {
	// url is the NATS server URL.
	// +kubebuilder:default="nats://nats.database.svc:4222"
	// +optional
	URL string `json:"url,omitempty"`

	// subjects defines the JetStream filter subjects for task consumption.
	// e.g., ["fleet-a.tasks.security.>"]
	// +kubebuilder:validation:MinItems=1
	Subjects []string `json:"subjects"`

	// stream is the JetStream stream name to consume from.
	// +kubebuilder:default="fleet_a_tasks"
	// +optional
	Stream string `json:"stream,omitempty"`

	// resultsStream is the JetStream stream to publish results to.
	// +kubebuilder:default="fleet_a_results"
	// +optional
	ResultsStream string `json:"resultsStream,omitempty"`

	// consumerName overrides the auto-generated durable consumer name.
	// Defaults to "knight-{name}".
	// +optional
	ConsumerName string `json:"consumerName,omitempty"`

	// maxDeliver is the maximum number of delivery attempts per message.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxDeliver int32 `json:"maxDeliver,omitempty"`
}

// KnightVault configures the shared Obsidian vault mount.
type KnightVault struct {
	// claimName is the PVC name for the shared vault.
	// +kubebuilder:default="obsidian-vault"
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// readOnly mounts the base vault as read-only.
	// +kubebuilder:default=true
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// writablePaths are subpaths within the vault the knight can write to.
	// +kubebuilder:default={"Briefings/","Roundtable/"}
	// +optional
	WritablePaths []string `json:"writablePaths,omitempty"`
}

// KnightPrompt allows overriding system prompt components.
type KnightPrompt struct {
	// identity overrides the knight's identity/persona description.
	// +optional
	Identity string `json:"identity,omitempty"`

	// instructions provides additional instructions appended to the system prompt.
	// +optional
	Instructions string `json:"instructions,omitempty"`

	// configMapRef references a ConfigMap containing prompt overrides.
	// Keys: "AGENTS.md", "TOOLS.md", "SOUL.md"
	// +optional
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`
}

// KnightResources defines compute resource requirements.
type KnightResources struct {
	// memory is the memory limit for the knight container.
	// +kubebuilder:default="256Mi"
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`

	// cpu is the CPU limit for the knight container.
	// +kubebuilder:default="200m"
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`
}

// KnightPhase represents the current lifecycle phase of the Knight.
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Degraded;Suspended
type KnightPhase string

const (
	KnightPhasePending      KnightPhase = "Pending"
	KnightPhaseProvisioning KnightPhase = "Provisioning"
	KnightPhaseReady        KnightPhase = "Ready"
	KnightPhaseDegraded     KnightPhase = "Degraded"
	KnightPhaseSuspended    KnightPhase = "Suspended"
)

// KnightStatus defines the observed state of Knight.
type KnightStatus struct {
	// phase is the current lifecycle phase of the knight.
	// +optional
	Phase KnightPhase `json:"phase,omitempty"`

	// ready indicates whether the knight is ready to accept tasks.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// tasksCompleted is the total number of tasks completed since creation.
	// +optional
	TasksCompleted int64 `json:"tasksCompleted,omitempty"`

	// tasksFailed is the total number of tasks that failed.
	// +optional
	TasksFailed int64 `json:"tasksFailed,omitempty"`

	// lastTaskAt is the timestamp of the last completed task.
	// +optional
	LastTaskAt *metav1.Time `json:"lastTaskAt,omitempty"`

	// totalCost is the cumulative cost in USD of all tasks processed.
	// +optional
	TotalCost string `json:"totalCost,omitempty"`

	// natsConsumer is the name of the reconciled NATS durable consumer.
	// +optional
	NATSConsumer string `json:"natsConsumer,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represent the current state of the Knight resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Tasks",type=integer,JSONPath=`.status.tasksCompleted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Knight is the Schema for the knights API.
// A Knight represents a specialized AI agent in the Round Table,
// deployed as a Kubernetes workload with NATS JetStream connectivity,
// skill injection, and shared vault access.
type Knight struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Knight
	// +required
	Spec KnightSpec `json:"spec"`

	// status defines the observed state of Knight
	// +optional
	Status KnightStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// KnightList contains a list of Knight
type KnightList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Knight `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Knight{}, &KnightList{})
}
