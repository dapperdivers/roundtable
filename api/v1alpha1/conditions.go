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

// Condition types follow Kubernetes API conventions:
// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
//
// Type names should be:
// - Adjectives (e.g., "Ready", "Available") or past-tense verbs (e.g., "Succeeded", "Degraded")
// - PascalCase for multi-word types
// - Describe the current state, not an action in progress
//
// Reason values should be:
// - PascalCase
// - Brief identifier for the condition
//
// Status values should be:
// - metav1.ConditionTrue, metav1.ConditionFalse, or metav1.ConditionUnknown

const (
	// ===== Knight Condition Types =====

	// ConditionKnightAvailable indicates whether the knight is available to accept tasks.
	// Status=True means the knight pod is running and NATS consumer is active.
	// Status=False means the knight is suspended, degraded, or provisioning.
	ConditionKnightAvailable = "Available"

	// ===== RoundTable Condition Types =====

	// ConditionRoundTableAvailable indicates whether the RoundTable is operational.
	// Status=True means all knights are ready and within budget.
	// Status=False means knights are degraded, suspended, or over budget.
	ConditionRoundTableAvailable = "Available"

	// ConditionNATSReady indicates whether NATS JetStream streams are configured.
	// Status=True means streams are created and healthy.
	// Status=False means stream creation failed or streams are unhealthy.
	ConditionNATSReady = "NATSReady"

	// ===== Chain Condition Types =====

	// ConditionChainValid indicates whether the chain spec passed validation.
	// Status=True means all steps, knight refs, templates, and DAG are valid.
	// Status=False means validation failed (cyclic deps, invalid refs, bad templates).
	ConditionChainValid = "Valid"

	// ConditionChainComplete indicates whether the chain execution finished.
	// Status=True means all steps completed (succeeded, partially succeeded, or failed).
	// Status=False means chain is still running or pending.
	ConditionChainComplete = "Complete"

	// ===== Mission Condition Types =====

	// ConditionMissionComplete indicates whether the mission finished execution.
	// Status=True means mission succeeded, failed, timed out, or was over budget.
	// Status=False means mission is still running.
	ConditionMissionComplete = "Complete"

	// ConditionBriefingPublished indicates whether the mission briefing was published.
	// Status=True means briefing was successfully sent to all knights via NATS.
	// Status=False means briefing publish failed or no briefing was configured.
	ConditionBriefingPublished = "BriefingPublished"

	// ConditionCleanupComplete indicates whether mission cleanup finished.
	// Status=True means all ephemeral resources were deleted.
	// Status=False means cleanup is in progress.
	ConditionCleanupComplete = "CleanupComplete"
)

const (
	// ===== Knight Condition Reasons =====

	// ReasonKnightReady indicates the knight is fully operational.
	ReasonKnightReady = "KnightReady"

	// ReasonKnightProvisioning indicates the knight deployment is being created.
	ReasonKnightProvisioning = "Provisioning"

	// ReasonKnightSuspended indicates the knight was manually suspended.
	ReasonKnightSuspended = "Suspended"

	// ReasonKnightReconcileError indicates the knight reconcile encountered an error.
	ReasonKnightReconcileError = "ReconcileError"

	// ===== RoundTable Condition Reasons =====

	// ReasonAllKnightsReady indicates all knights in the roundtable are ready.
	ReasonAllKnightsReady = "AllKnightsReady"

	// ReasonKnightsDegraded indicates some knights are not ready.
	ReasonKnightsDegraded = "KnightsDegraded"

	// ReasonRoundTableSuspended indicates the roundtable was manually suspended.
	ReasonRoundTableSuspended = "Suspended"

	// ReasonRoundTableProvisioning indicates the roundtable is being provisioned.
	ReasonRoundTableProvisioning = "Provisioning"

	// ReasonOverBudget indicates the roundtable exceeded its cost budget.
	ReasonOverBudget = "OverBudget"

	// ReasonStreamsReady indicates NATS streams are configured and healthy.
	ReasonStreamsReady = "StreamsReady"

	// ReasonStreamError indicates NATS stream creation or update failed.
	ReasonStreamError = "StreamError"

	// ===== Chain Condition Reasons =====

	// ReasonChainValid indicates the chain spec passed all validation checks.
	ReasonChainValid = "Valid"

	// ReasonMissingRoundTableRef indicates the chain is missing roundTableRef.
	ReasonMissingRoundTableRef = "MissingRoundTableRef"

	// ReasonInvalidKnightRef indicates a step references a non-existent knight.
	ReasonInvalidKnightRef = "InvalidKnightRef"

	// ReasonCyclicDependency indicates the chain has cyclic step dependencies.
	ReasonCyclicDependency = "CyclicDependency"

	// ReasonInvalidTemplate indicates a step's Go template failed to parse.
	ReasonInvalidTemplate = "InvalidTemplate"

	// ReasonChainSucceeded indicates all chain steps completed successfully.
	ReasonChainSucceeded = "Succeeded"

	// ReasonChainPartiallySucceeded indicates some steps failed but had continueOnFailure.
	ReasonChainPartiallySucceeded = "PartiallySucceeded"

	// ReasonChainFailed indicates one or more steps failed without continueOnFailure.
	ReasonChainFailed = "Failed"

	// ReasonChainTimeout indicates the chain exceeded its timeout duration.
	ReasonChainTimeout = "Timeout"

	// ===== Mission Condition Reasons =====

	// ReasonMissionSucceeded indicates all mission chains completed successfully.
	ReasonMissionSucceeded = "Succeeded"

	// ReasonMissionChainFailed indicates one or more mission chains failed.
	ReasonMissionChainFailed = "ChainFailed"

	// ReasonMissionTimeout indicates the mission exceeded its timeout.
	ReasonMissionTimeout = "Timeout"

	// ReasonMissionExpired indicates the mission exceeded its TTL.
	ReasonMissionExpired = "Expired"

	// ReasonBriefingPublished indicates briefing was published successfully.
	ReasonBriefingPublished = "Published"

	// ReasonBriefingPublishFailed indicates briefing publish to NATS failed.
	ReasonBriefingPublishFailed = "PublishFailed"

	// ReasonNoBriefing indicates no briefing text was configured.
	ReasonNoBriefing = "NoBriefing"

	// ReasonCleanupComplete indicates mission cleanup finished successfully.
	ReasonCleanupComplete = "CleanedUp"
)
