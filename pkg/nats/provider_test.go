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
	"sync"
	"testing"

	"github.com/go-logr/logr"
)

func TestNewProvider(t *testing.T) {
	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	if provider == nil {
		t.Fatal("NewProvider returned nil")
	}

	if provider.config.URL != config.URL {
		t.Errorf("Expected config URL %s, got %s", config.URL, provider.config.URL)
	}

	if provider.IsConnected() {
		t.Error("New provider should not be connected yet (lazy connection)")
	}
}

func TestProvider_Client_LazyConnection(t *testing.T) {
	// Note: This test will fail if no NATS server is available
	// In production, we'd use a mock client for testing
	t.Skip("Skipping test that requires NATS server")

	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	// First call should create and connect
	client1, err := provider.Client()
	if err != nil {
		t.Fatalf("Failed to get client: %v", err)
	}

	if client1 == nil {
		t.Fatal("Client() returned nil")
	}

	if !provider.IsConnected() {
		t.Error("Provider should be connected after Client() call")
	}

	// Second call should return same instance
	client2, err := provider.Client()
	if err != nil {
		t.Fatalf("Failed to get client on second call: %v", err)
	}

	// In Go, we can't compare interface pointers directly,
	// but we can verify both work
	if client2 == nil {
		t.Fatal("Second Client() returned nil")
	}
}

func TestProvider_Close(t *testing.T) {
	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	// Closing unconnected provider should not error
	if err := provider.Close(); err != nil {
		t.Errorf("Close() on unconnected provider failed: %v", err)
	}

	if provider.IsConnected() {
		t.Error("Provider should not be connected after Close()")
	}
}

func TestProvider_ConcurrentAccess(t *testing.T) {
	t.Skip("Skipping test that requires NATS server")

	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	// Test concurrent access to Client()
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := provider.Client()
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent Client() call failed: %v", err)
	}
}

func TestProvider_IsConnected(t *testing.T) {
	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	// Should not be connected initially
	if provider.IsConnected() {
		t.Error("New provider should not be connected")
	}

	// Close should be safe even when not connected
	if err := provider.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}

	// Still should not be connected
	if provider.IsConnected() {
		t.Error("Provider should not be connected after Close()")
	}
}

func TestProvider_MultipleClose(t *testing.T) {
	config := DefaultConfig()
	log := logr.Discard()

	provider := NewProvider(config, log)

	// Multiple closes should be safe
	if err := provider.Close(); err != nil {
		t.Errorf("First Close() failed: %v", err)
	}

	if err := provider.Close(); err != nil {
		t.Errorf("Second Close() failed: %v", err)
	}

	if provider.IsConnected() {
		t.Error("Provider should not be connected after multiple Close() calls")
	}
}
