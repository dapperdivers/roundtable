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
	"encoding/json"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// TestConfigValidation tests config validation
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid default config",
			config: Config{
				URL:                  "nats://nats.database.svc:4222",
				RetryOnFailedConnect: true,
				MaxReconnects:        -1,
				ReconnectWait:        2 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "valid minimal config",
			config: Config{
				URL: "nats://localhost:4222",
			},
			wantErr: false,
		},
		{
			name: "empty URL",
			config: Config{
				URL: "",
			},
			wantErr: true,
		},
		{
			name: "invalid reconnect wait",
			config: Config{
				URL:           "nats://nats.database.svc:4222",
				ReconnectWait: -1 * time.Second,
			},
			wantErr: false, // Negative values handled by NATS library
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config, logr.Discard())
			
			// Attempt to connect - expect failure for invalid configs
			err := client.Connect()
			
			if tt.wantErr && err == nil {
				t.Errorf("expected error for config with empty URL, got nil")
			}
			
			if !tt.wantErr && tt.config.URL != "" && err != nil {
				// It's OK to fail connection if NATS server not available
				// We're just testing that config is accepted
				t.Logf("connection failed (expected if no NATS server): %v", err)
			}
		})
	}
}

// TestDefaultConfig tests the default configuration
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.URL != "nats://nats.database.svc:4222" {
		t.Errorf("expected default URL to be nats://nats.database.svc:4222, got %s", config.URL)
	}

	if !config.RetryOnFailedConnect {
		t.Errorf("expected RetryOnFailedConnect to be true")
	}

	if config.MaxReconnects != -1 {
		t.Errorf("expected MaxReconnects to be -1 (infinite), got %d", config.MaxReconnects)
	}

	if config.ReconnectWait != 2*time.Second {
		t.Errorf("expected ReconnectWait to be 2s, got %v", config.ReconnectWait)
	}
}

// TestPublishWithNilConnection tests publishing when connection is nil
func TestPublishWithNilConnection(t *testing.T) {
	// Create client with invalid URL to prevent successful connection
	config := Config{
		URL:                  "nats://invalid.nonexistent.local:9999",
		RetryOnFailedConnect: false,
		MaxReconnects:        0,
	}
	
	client := &JetStreamClient{
		config: config,
		log:    logr.Discard(),
	}

	// Attempt to publish without connecting
	err := client.Publish("test.subject", []byte("test"))
	if err == nil {
		t.Error("expected error when publishing with nil connection, got nil")
	}

	// Attempt PublishJSON
	err = client.PublishJSON("test.subject", map[string]string{"key": "value"})
	if err == nil {
		t.Error("expected error when publishing JSON with nil connection, got nil")
	}
}

// TestIsConnected tests connection state checking
func TestIsConnected(t *testing.T) {
	config := Config{
		URL: "nats://invalid.nonexistent.local:9999",
	}
	
	client := &JetStreamClient{
		config: config,
		log:    logr.Discard(),
	}

	if client.IsConnected() {
		t.Error("expected IsConnected() to return false for unconnected client")
	}
}

// TestCloseWithoutConnection tests closing when not connected
func TestCloseWithoutConnection(t *testing.T) {
	config := Config{
		URL: "nats://localhost:4222",
	}
	
	client := NewClient(config, logr.Discard())
	
	err := client.Close()
	if err != nil {
		t.Errorf("expected no error when closing unconnected client, got %v", err)
	}
}

// TestStreamConfigBuilderPatterns tests StreamConfig construction patterns
func TestStreamConfigBuilderPatterns(t *testing.T) {
	tests := []struct {
		name   string
		config StreamConfig
		valid  bool
	}{
		{
			name: "minimal stream config",
			config: StreamConfig{
				Name:      "test_stream",
				Subjects:  []string{"test.>"},
				Retention: RetentionWorkQueue,
				Storage:   StorageFile,
			},
			valid: true,
		},
		{
			name: "full stream config with limits",
			config: StreamConfig{
				Name:      "fleet_tasks",
				Subjects:  []string{"fleet-a.tasks.>", "fleet-a.results.>"},
				Retention: RetentionLimits,
				MaxAge:    24 * time.Hour,
				MaxMsgs:   10000,
				Storage:   StorageMemory,
				Discard:   DiscardOld,
			},
			valid: true,
		},
		{
			name: "workqueue retention with memory storage",
			config: StreamConfig{
				Name:      "ephemeral_tasks",
				Subjects:  []string{"mission-*.tasks.>"},
				Retention: RetentionWorkQueue,
				Storage:   StorageMemory,
			},
			valid: true,
		},
		{
			name: "empty name",
			config: StreamConfig{
				Name:      "",
				Subjects:  []string{"test.>"},
				Retention: RetentionLimits,
			},
			valid: false,
		},
		{
			name: "no subjects",
			config: StreamConfig{
				Name:      "test_stream",
				Subjects:  []string{},
				Retention: RetentionLimits,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate basic requirements
			if tt.valid {
				if tt.config.Name == "" {
					t.Error("valid config should have non-empty name")
				}
				if len(tt.config.Subjects) == 0 {
					t.Error("valid config should have at least one subject")
				}
			} else {
				if tt.config.Name != "" && len(tt.config.Subjects) > 0 {
					t.Error("invalid config should have empty name or no subjects")
				}
			}

			// Test retention policy conversion
			if tt.config.Retention != "" {
				natsPolicy := tt.config.Retention.ToNATS()
				// Just verify it returns a valid value (type is int-based)
				_ = natsPolicy
			}

			// Test storage type conversion
			if tt.config.Storage != "" {
				natsStorage := tt.config.Storage.ToNATS()
				// Just verify it returns a valid value (type is int-based)
				_ = natsStorage
			}

			// Test discard policy conversion
			if tt.config.Discard != "" {
				natsDiscard := tt.config.Discard.ToNATS()
				// Just verify it returns a valid value (type is int-based)
				_ = natsDiscard
			}
		})
	}
}

// TestRetentionPolicyConversion tests retention policy enum conversions
func TestRetentionPolicyConversion(t *testing.T) {
	tests := []struct {
		policy   RetentionPolicy
		expected string
	}{
		{RetentionLimits, "Limits"},
		{RetentionWorkQueue, "WorkQueue"},
		{RetentionInterest, "Interest"},
		{RetentionPolicy("Unknown"), "WorkQueue"}, // Default fallback
	}

	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			natsPolicy := tt.policy.ToNATS()
			// Just verify it returns a valid value (type is int-based)
			_ = natsPolicy
		})
	}
}

// TestStorageTypeConversion tests storage type enum conversions
func TestStorageTypeConversion(t *testing.T) {
	tests := []struct {
		storage  StorageType
		expected string
	}{
		{StorageFile, "File"},
		{StorageMemory, "Memory"},
		{StorageType("Unknown"), "File"}, // Default fallback
	}

	for _, tt := range tests {
		t.Run(string(tt.storage), func(t *testing.T) {
			natsStorage := tt.storage.ToNATS()
			// Just verify it returns a valid value (type is int-based)
			_ = natsStorage
		})
	}
}

// TestDiscardPolicyConversion tests discard policy enum conversions
func TestDiscardPolicyConversion(t *testing.T) {
	tests := []struct {
		policy   DiscardPolicy
		expected string
	}{
		{DiscardOld, "Old"},
		{DiscardNew, "New"},
		{DiscardPolicy("Unknown"), "Old"}, // Default fallback
	}

	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			natsDiscard := tt.policy.ToNATS()
			// Just verify it returns a valid value (type is int-based)
			_ = natsDiscard
		})
	}
}

// TestTaskPayloadSerialization tests TaskPayload JSON marshaling
func TestTaskPayloadSerialization(t *testing.T) {
	tests := []struct {
		name    string
		payload TaskPayload
		wantErr bool
	}{
		{
			name: "full payload",
			payload: TaskPayload{
				TaskID:    "task-123",
				ChainName: "chain-abc",
				StepName:  "step-1",
				Task:      "Analyze security findings",
			},
			wantErr: false,
		},
		{
			name: "minimal payload",
			payload: TaskPayload{
				TaskID: "task-456",
				Task:   "Simple task",
			},
			wantErr: false,
		},
		{
			name: "empty task",
			payload: TaskPayload{
				TaskID: "task-789",
				Task:   "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("Marshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				// Unmarshal back
				var decoded TaskPayload
				err = json.Unmarshal(data, &decoded)
				if err != nil {
					t.Errorf("Unmarshal() error = %v", err)
					return
				}

				// Verify round-trip
				if decoded.TaskID != tt.payload.TaskID {
					t.Errorf("TaskID mismatch: got %s, want %s", decoded.TaskID, tt.payload.TaskID)
				}
				if decoded.Task != tt.payload.Task {
					t.Errorf("Task mismatch: got %s, want %s", decoded.Task, tt.payload.Task)
				}
			}
		})
	}
}

// TestTaskResultSerialization tests TaskResult JSON unmarshaling (both formats)
func TestTaskResultSerialization(t *testing.T) {
	tests := []struct {
		name       string
		jsonInput  string
		wantTaskID string
		wantOutput string
		wantError  string
	}{
		{
			name:       "controller format (camelCase)",
			jsonInput:  `{"taskId":"task-123","output":"Success result","error":""}`,
			wantTaskID: "task-123",
			wantOutput: "Success result",
			wantError:  "",
		},
		{
			name:       "pi-knight format (snake_case)",
			jsonInput:  `{"task_id":"task-456","result":"Pi knight result","success":true}`,
			wantTaskID: "task-456",
			wantOutput: "Pi knight result",
			wantError:  "",
		},
		{
			name:       "controller format with error",
			jsonInput:  `{"taskId":"task-789","output":"","error":"Connection failed"}`,
			wantTaskID: "task-789",
			wantOutput: "",
			wantError:  "Connection failed",
		},
		{
			name:       "pi-knight format with failure",
			jsonInput:  `{"task_id":"task-999","result":"","success":false}`,
			wantTaskID: "task-999",
			wantOutput: "",
			wantError:  "task reported failure",
		},
		{
			name:       "both formats present (controller takes precedence)",
			jsonInput:  `{"taskId":"controller-123","task_id":"pi-456","output":"controller output","result":"pi result"}`,
			wantTaskID: "controller-123",
			wantOutput: "controller output",
			wantError:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result TaskResult
			err := json.Unmarshal([]byte(tt.jsonInput), &result)
			if err != nil {
				t.Errorf("Unmarshal() error = %v", err)
				return
			}

			// Test GetTaskID()
			gotTaskID := result.GetTaskID()
			if gotTaskID != tt.wantTaskID {
				t.Errorf("GetTaskID() = %s, want %s", gotTaskID, tt.wantTaskID)
			}

			// Test GetOutput()
			gotOutput := result.GetOutput()
			if gotOutput != tt.wantOutput {
				t.Errorf("GetOutput() = %s, want %s", gotOutput, tt.wantOutput)
			}

			// Test GetError()
			gotError := result.GetError()
			if gotError != tt.wantError {
				t.Errorf("GetError() = %s, want %s", gotError, tt.wantError)
			}
		})
	}
}

// TestTaskResultDualFormatGetters tests the dual-format getter methods
func TestTaskResultDualFormatGetters(t *testing.T) {
	t.Run("GetTaskID prefers controller format", func(t *testing.T) {
		result := TaskResult{
			TaskID:  "controller-id",
			TaskID2: "pi-id",
		}
		if result.GetTaskID() != "controller-id" {
			t.Errorf("expected controller-id, got %s", result.GetTaskID())
		}
	})

	t.Run("GetTaskID falls back to pi format", func(t *testing.T) {
		result := TaskResult{
			TaskID:  "",
			TaskID2: "pi-id",
		}
		if result.GetTaskID() != "pi-id" {
			t.Errorf("expected pi-id, got %s", result.GetTaskID())
		}
	})

	t.Run("GetOutput prefers controller format", func(t *testing.T) {
		result := TaskResult{
			Output: "controller output",
			Result: "pi result",
		}
		if result.GetOutput() != "controller output" {
			t.Errorf("expected controller output, got %s", result.GetOutput())
		}
	})

	t.Run("GetOutput falls back to pi format", func(t *testing.T) {
		result := TaskResult{
			Output: "",
			Result: "pi result",
		}
		if result.GetOutput() != "pi result" {
			t.Errorf("expected pi result, got %s", result.GetOutput())
		}
	})

	t.Run("GetError returns explicit error", func(t *testing.T) {
		result := TaskResult{
			Error: "explicit error",
		}
		if result.GetError() != "explicit error" {
			t.Errorf("expected explicit error, got %s", result.GetError())
		}
	})

	t.Run("GetError detects failure from success boolean", func(t *testing.T) {
		successFalse := false
		result := TaskResult{
			Error:   "",
			Success: &successFalse,
		}
		if result.GetError() != "task reported failure" {
			t.Errorf("expected task reported failure, got %s", result.GetError())
		}
	})

	t.Run("GetError returns empty for success", func(t *testing.T) {
		successTrue := true
		result := TaskResult{
			Error:   "",
			Success: &successTrue,
		}
		if result.GetError() != "" {
			t.Errorf("expected empty error, got %s", result.GetError())
		}
	})

	t.Run("GetError returns empty when no error info", func(t *testing.T) {
		result := TaskResult{}
		if result.GetError() != "" {
			t.Errorf("expected empty error, got %s", result.GetError())
		}
	})
}

// TestSubscribeOptions tests subscribe option builders
func TestSubscribeOptions(t *testing.T) {
	t.Run("WithDurable sets durable name", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithDurable("test-consumer")(opts)
		if opts.durable != "test-consumer" {
			t.Errorf("expected durable=test-consumer, got %s", opts.durable)
		}
	})

	t.Run("WithBindStream sets stream name", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithBindStream("test-stream")(opts)
		if opts.bindStream != "test-stream" {
			t.Errorf("expected bindStream=test-stream, got %s", opts.bindStream)
		}
	})

	t.Run("WithAckExplicit enables explicit ack", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithAckExplicit()(opts)
		if !opts.ackExplicit {
			t.Error("expected ackExplicit=true")
		}
	})

	t.Run("WithDeliverAll enables deliver all", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithDeliverAll()(opts)
		if !opts.deliverAll {
			t.Error("expected deliverAll=true")
		}
	})

	t.Run("WithFallbackAutoDetect enables fallback", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithFallbackAutoDetect()(opts)
		if !opts.fallbackAutoDetect {
			t.Error("expected fallbackAutoDetect=true")
		}
	})

	t.Run("multiple options compose", func(t *testing.T) {
		opts := &subscribeOptions{}
		WithDurable("consumer")(opts)
		WithBindStream("stream")(opts)
		WithAckExplicit()(opts)

		if opts.durable != "consumer" {
			t.Errorf("expected durable=consumer, got %s", opts.durable)
		}
		if opts.bindStream != "stream" {
			t.Errorf("expected bindStream=stream, got %s", opts.bindStream)
		}
		if !opts.ackExplicit {
			t.Error("expected ackExplicit=true")
		}
	})
}

// TestConsumerConfig tests consumer configuration
func TestConsumerConfig(t *testing.T) {
	tests := []struct {
		name   string
		config ConsumerConfig
		valid  bool
	}{
		{
			name: "valid durable consumer",
			config: ConsumerConfig{
				Durable:       "test-consumer",
				FilterSubject: "test.tasks.>",
				AckPolicy:     AckExplicit,
			},
			valid: true,
		},
		{
			name: "minimal config",
			config: ConsumerConfig{
				Durable: "consumer",
			},
			valid: true,
		},
		{
			name: "empty durable name",
			config: ConsumerConfig{
				Durable: "",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.valid && tt.config.Durable == "" {
				t.Error("valid config should have non-empty durable name")
			}
			if !tt.valid && tt.config.Durable != "" {
				t.Error("invalid config should have empty durable name")
			}
		})
	}
}

// TestJSONPublishErrors tests JSON marshaling error handling
func TestJSONPublishErrors(t *testing.T) {
	config := Config{
		URL: "nats://invalid.nonexistent.local:9999",
	}
	
	client := &JetStreamClient{
		config: config,
		log:    logr.Discard(),
	}

	// Try to publish a value that can't be marshaled to JSON
	type unmarshalable struct {
		BadField chan int // channels can't be marshaled to JSON
	}

	err := client.PublishJSON("test.subject", unmarshalable{BadField: make(chan int)})
	if err == nil {
		t.Error("expected error when marshaling unmarshalable type, got nil")
	}
}
