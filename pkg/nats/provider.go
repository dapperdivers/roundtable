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
	"sync"

	"github.com/go-logr/logr"
)

// Provider manages a shared NATS client instance across controllers.
// Instead of each controller creating its own connection, they share one.
// This reduces connection overhead and ensures consistent NATS configuration.
type Provider struct {
	client Client
	mu     sync.Mutex
	config Config
	log    logr.Logger
}

// NewProvider creates a new NATS provider with the given configuration.
// The actual connection is established lazily on the first call to Client().
func NewProvider(config Config, log logr.Logger) *Provider {
	return &Provider{
		config: config,
		log:    log,
	}
}

// Client returns the shared NATS client, connecting lazily on first call.
// Subsequent calls return the same client instance if still connected.
// Thread-safe for concurrent access from multiple controllers.
func (p *Provider) Client() (Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If client exists and is connected, return it
	if p.client != nil && p.client.IsConnected() {
		return p.client, nil
	}

	// Create new client
	p.log.Info("Creating shared NATS client", "url", p.config.URL)
	p.client = NewClient(p.config, p.log)

	// Connect to NATS
	if err := p.client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	p.log.Info("Successfully connected to NATS", "url", p.config.URL)
	return p.client, nil
}

// Close closes the shared NATS connection.
// Should be called during controller shutdown.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client == nil {
		return nil
	}

	p.log.Info("Closing shared NATS connection")
	err := p.client.Close()
	p.client = nil
	return err
}

// IsConnected returns true if the provider has an active connection.
func (p *Provider) IsConnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.client != nil && p.client.IsConnected()
}
