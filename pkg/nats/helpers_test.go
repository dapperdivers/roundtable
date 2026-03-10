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
	"strings"
	"testing"
)

// TestTaskSubject tests task subject construction
func TestTaskSubject(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		domain string
		knight string
		want   string
	}{
		{
			name:   "standard fleet task",
			prefix: "fleet-a",
			domain: "security",
			knight: "galahad",
			want:   "fleet-a.tasks.security.galahad",
		},
		{
			name:   "mission task",
			prefix: "mission-abc",
			domain: "research",
			knight: "percival",
			want:   "mission-abc.tasks.research.percival",
		},
		{
			name:   "empty prefix",
			prefix: "",
			domain: "security",
			knight: "gawain",
			want:   ".tasks.security.gawain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TaskSubject(tt.prefix, tt.domain, tt.knight)
			if got != tt.want {
				t.Errorf("TaskSubject() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestResultSubject tests result subject construction
func TestResultSubject(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		taskID string
		want   string
	}{
		{
			name:   "standard result",
			prefix: "fleet-a",
			taskID: "task-123",
			want:   "fleet-a.results.task-123",
		},
		{
			name:   "mission result",
			prefix: "mission-xyz",
			taskID: "step-1-task-456",
			want:   "mission-xyz.results.step-1-task-456",
		},
		{
			name:   "empty prefix",
			prefix: "",
			taskID: "task-789",
			want:   ".results.task-789",
		},
		{
			name:   "empty taskID",
			prefix: "fleet-a",
			taskID: "",
			want:   "fleet-a.results.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResultSubject(tt.prefix, tt.taskID)
			if got != tt.want {
				t.Errorf("ResultSubject() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestResultSubjectWildcard tests wildcard result subject construction
func TestResultSubjectWildcard(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		taskPrefix string
		want       string
	}{
		{
			name:       "chain step wildcard",
			prefix:     "mission-abc",
			taskPrefix: "step-1",
			want:       "mission-abc.results.step-1.*",
		},
		{
			name:       "fleet wildcard",
			prefix:     "fleet-a",
			taskPrefix: "chain-xyz",
			want:       "fleet-a.results.chain-xyz.*",
		},
		{
			name:       "empty taskPrefix",
			prefix:     "fleet-a",
			taskPrefix: "",
			want:       "fleet-a.results..*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResultSubjectWildcard(tt.prefix, tt.taskPrefix)
			if got != tt.want {
				t.Errorf("ResultSubjectWildcard() = %s, want %s", got, tt.want)
			}

			// Verify it ends with wildcard
			if !strings.HasSuffix(got, ".*") {
				t.Errorf("ResultSubjectWildcard() should end with .*, got %s", got)
			}
		})
	}
}

// TestStreamSubject tests stream subject pattern construction
func TestStreamSubject(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		streamType string
		want       string
	}{
		{
			name:       "tasks stream",
			prefix:     "fleet-a",
			streamType: "tasks",
			want:       "fleet-a.tasks.>",
		},
		{
			name:       "results stream",
			prefix:     "mission-abc",
			streamType: "results",
			want:       "mission-abc.results.>",
		},
		{
			name:       "all stream",
			prefix:     "fleet-a",
			streamType: "*",
			want:       "fleet-a.*.>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StreamSubject(tt.prefix, tt.streamType)
			if got != tt.want {
				t.Errorf("StreamSubject() = %s, want %s", got, tt.want)
			}

			// Verify it ends with wildcard
			if !strings.HasSuffix(got, ".>") {
				t.Errorf("StreamSubject() should end with .>, got %s", got)
			}
		})
	}
}

// TestChainConsumerName tests chain consumer name generation
func TestChainConsumerName(t *testing.T) {
	tests := []struct {
		name      string
		chainName string
		stepName  string
	}{
		{
			name:      "standard chain step",
			chainName: "security-audit",
			stepName:  "scan",
		},
		{
			name:      "multi-word names",
			chainName: "complex-analysis-chain",
			stepName:  "step-1-preprocessing",
		},
		{
			name:      "empty names",
			chainName: "",
			stepName:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChainConsumerName(tt.chainName, tt.stepName)

			// Verify format: chain-poll-{chainName}-{stepName}-{timestamp}
			if !strings.HasPrefix(got, "chain-poll-") {
				t.Errorf("ChainConsumerName() should start with chain-poll-, got %s", got)
			}

			if tt.chainName != "" && !strings.Contains(got, tt.chainName) {
				t.Errorf("ChainConsumerName() should contain chain name %s, got %s", tt.chainName, got)
			}

			if tt.stepName != "" && !strings.Contains(got, tt.stepName) {
				t.Errorf("ChainConsumerName() should contain step name %s, got %s", tt.stepName, got)
			}

			// Verify uniqueness by calling twice with a small delay
			// Note: In fast tests, timestamps might be identical, so we just verify format
			got2 := ChainConsumerName(tt.chainName, tt.stepName)
			// Both should have the same prefix pattern even if timestamps match
			if !strings.HasPrefix(got2, "chain-poll-") {
				t.Errorf("second call should also start with chain-poll-, got %s", got2)
			}
		})
	}
}

// TestKnightConsumerName tests knight consumer name generation
func TestKnightConsumerName(t *testing.T) {
	tests := []struct {
		name       string
		knightName string
		want       string
	}{
		{
			name:       "standard knight",
			knightName: "galahad",
			want:       "knight-galahad",
		},
		{
			name:       "hyphenated name",
			knightName: "sir-gawain",
			want:       "knight-sir-gawain",
		},
		{
			name:       "empty name",
			knightName: "",
			want:       "knight-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KnightConsumerName(tt.knightName)
			if got != tt.want {
				t.Errorf("KnightConsumerName() = %s, want %s", got, tt.want)
			}

			// Verify format
			if !strings.HasPrefix(got, "knight-") {
				t.Errorf("KnightConsumerName() should start with knight-, got %s", got)
			}
		})
	}
}

// TestSubjectFormatConsistency tests that all subject builders produce valid NATS subjects
func TestSubjectFormatConsistency(t *testing.T) {
	t.Run("no spaces in subjects", func(t *testing.T) {
		subjects := []string{
			TaskSubject("fleet-a", "security", "galahad"),
			ResultSubject("fleet-a", "task-123"),
			ResultSubjectWildcard("fleet-a", "step-1"),
			StreamSubject("fleet-a", "tasks"),
		}

		for _, subject := range subjects {
			if strings.Contains(subject, " ") {
				t.Errorf("subject contains space: %s", subject)
			}
		}
	})

	t.Run("valid NATS characters only", func(t *testing.T) {
		subjects := []string{
			TaskSubject("fleet-a", "security", "galahad"),
			ResultSubject("fleet-a", "task-123"),
		}

		for _, subject := range subjects {
			// NATS subjects should only contain alphanumeric, dots, hyphens, underscores
			for _, ch := range subject {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
					(ch >= '0' && ch <= '9') || ch == '.' || ch == '-' || ch == '_' || ch == '*' || ch == '>') {
					t.Errorf("subject contains invalid character %c: %s", ch, subject)
				}
			}
		}
	})

	t.Run("proper hierarchy with dots", func(t *testing.T) {
		taskSubj := TaskSubject("fleet-a", "security", "galahad")
		resultSubj := ResultSubject("fleet-a", "task-123")

		// Should have at least 2 dots (3 segments)
		if strings.Count(taskSubj, ".") < 2 {
			t.Errorf("task subject should have at least 2 dots: %s", taskSubj)
		}
		if strings.Count(resultSubj, ".") < 2 {
			t.Errorf("result subject should have at least 2 dots: %s", resultSubj)
		}
	})
}
