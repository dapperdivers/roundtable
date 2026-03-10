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

package nats

import (
	"fmt"
	"time"
)

// TaskSubject constructs a NATS subject for publishing tasks to a knight.
// Format: {prefix}.tasks.{domain}.{knight}
func TaskSubject(prefix, domain, knight string) string {
	return fmt.Sprintf("%s.tasks.%s.%s", prefix, domain, knight)
}

// ResultSubject constructs a NATS subject for task results.
// Format: {prefix}.results.{taskID}
func ResultSubject(prefix, taskID string) string {
	return fmt.Sprintf("%s.results.%s", prefix, taskID)
}

// ResultSubjectWildcard constructs a NATS subject pattern for polling task results.
// Format: {prefix}.results.{taskPrefix}.*
func ResultSubjectWildcard(prefix, taskPrefix string) string {
	return fmt.Sprintf("%s.results.%s.*", prefix, taskPrefix)
}

// StreamSubject constructs a NATS subject pattern for stream capture.
// Format: {prefix}.{streamType}.>
func StreamSubject(prefix, streamType string) string {
	return fmt.Sprintf("%s.%s.>", prefix, streamType)
}

// ChainConsumerName generates a consumer name for chain result polling.
// Format: chain-poll-{chainName}-{stepName}-{timestamp}
func ChainConsumerName(chainName, stepName string) string {
	return fmt.Sprintf("chain-poll-%s-%s-%d", chainName, stepName, time.Now().UnixMilli())
}

// KnightConsumerName generates a consumer name for a knight.
// Format: knight-{knightName}
func KnightConsumerName(knightName string) string {
	return fmt.Sprintf("knight-%s", knightName)
}
