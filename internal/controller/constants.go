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

import "time"

// Requeue intervals for controller reconciliation loops.
// These constants replace magic numbers to improve code readability
// and make performance tuning easier.
const (
	// RequeueFast is used for rapid polling during active state transitions.
	// Examples: mission phase transitions, chain step execution.
	RequeueFast = 1 * time.Second

	// RequeueMedium is used for moderate polling during provisioning or assembly.
	// Examples: waiting for knight readiness, provisioning resources.
	RequeueMedium = 2 * time.Second

	// RequeueDefault is the standard requeue interval for most reconciliation loops.
	// Examples: mission active monitoring, chain execution monitoring.
	RequeueDefault = 5 * time.Second

	// RequeueModerate is used for medium-frequency checks.
	// Examples: mission monitoring with moderate urgency.
	RequeueModerate = 10 * time.Second

	// RequeueSlow is used for non-urgent checks like knight readiness or errors.
	// Examples: knight status polling, roundtable aggregation, error recovery.
	RequeueSlow = 30 * time.Second

	// RequeueVeryPlow is used for very infrequent checks.
	// Examples: roundtable fleet aggregation, long-running status updates.
	RequeueVerySlow = 60 * time.Second
)

// Operational limits and burst controls.
const (
	// WarmPoolBurstLimit is the maximum number of warm knights created per reconcile.
	// This prevents resource exhaustion when scaling up warm pools.
	WarmPoolBurstLimit = 5
)
