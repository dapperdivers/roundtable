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
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/nats-io/nats.go"
)

// Client is the interface for NATS operations in the Round Table operator.
type Client interface {
	// Connect establishes a connection to NATS (if not already connected).
	Connect() error

	// Close closes the NATS connection.
	Close() error

	// IsConnected returns true if the client is connected to NATS.
	IsConnected() bool

	// Publish publishes raw bytes to a subject.
	Publish(subject string, data []byte) error

	// PublishJSON publishes a JSON-encoded value to a subject.
	PublishJSON(subject string, v interface{}) error

	// Subscribe creates a synchronous subscription to a subject.
	Subscribe(subject string, opts ...SubscribeOption) (*nats.Subscription, error)

	// CreateStream creates a JetStream stream with the given configuration.
	CreateStream(config StreamConfig) error

	// DeleteStream deletes a JetStream stream.
	DeleteStream(name string) error

	// StreamInfo returns information about a stream.
	StreamInfo(name string) (*nats.StreamInfo, error)

	// EnsureConsumer creates or updates a JetStream consumer.
	EnsureConsumer(stream, name string, config ConsumerConfig) error

	// DeleteConsumer deletes a JetStream consumer.
	DeleteConsumer(stream, consumer string) error

	// PollMessage polls for a single message with a timeout.
	PollMessage(subject string, timeout time.Duration, opts ...SubscribeOption) (*nats.Msg, error)
}

// JetStreamClient implements the Client interface using NATS JetStream.
type JetStreamClient struct {
	config Config
	nc     *nats.Conn
	js     nats.JetStreamContext
	mu     sync.Mutex
	log    logr.Logger
}

// NewClient creates a new NATS client with the given configuration.
func NewClient(config Config, log logr.Logger) Client {
	return &JetStreamClient{
		config: config,
		log:    log,
	}
}

// Connect establishes a connection to NATS if not already connected.
func (c *JetStreamClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.nc != nil && c.nc.IsConnected() {
		return nil // Already connected
	}

	opts := []nats.Option{}
	if c.config.RetryOnFailedConnect {
		opts = append(opts, nats.RetryOnFailedConnect(true))
	}
	if c.config.MaxReconnects != 0 {
		opts = append(opts, nats.MaxReconnects(c.config.MaxReconnects))
	}
	if c.config.ReconnectWait > 0 {
		opts = append(opts, nats.ReconnectWait(c.config.ReconnectWait))
	}

	nc, err := nats.Connect(c.config.URL, opts...)
	if err != nil {
		return fmt.Errorf("NATS connect to %s failed: %w", c.config.URL, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("JetStream context failed: %w", err)
	}

	c.nc = nc
	c.js = js
	c.log.Info("Connected to NATS", "url", c.config.URL)

	return nil
}

// Close closes the NATS connection.
func (c *JetStreamClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.nc != nil {
		c.nc.Close()
		c.nc = nil
		c.js = nil
		c.log.Info("Closed NATS connection")
	}
	return nil
}

// IsConnected returns true if the client is connected to NATS.
func (c *JetStreamClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nc != nil && c.nc.IsConnected()
}

// Publish publishes raw bytes to a subject.
func (c *JetStreamClient) Publish(subject string, data []byte) error {
	if err := c.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	_, err := js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("NATS publish to %s failed: %w", subject, err)
	}

	return nil
}

// PublishJSON publishes a JSON-encoded value to a subject.
func (c *JetStreamClient) PublishJSON(subject string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal JSON payload: %w", err)
	}
	return c.Publish(subject, data)
}

// Subscribe creates a synchronous subscription to a subject.
func (c *JetStreamClient) Subscribe(subject string, opts ...SubscribeOption) (*nats.Subscription, error) {
	if err := c.Connect(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	// Apply subscribe options
	subOpts := &subscribeOptions{}
	for _, opt := range opts {
		opt(subOpts)
	}

	// Build NATS options
	natsOpts := []nats.SubOpt{}
	if subOpts.durable != "" {
		natsOpts = append(natsOpts, nats.Durable(subOpts.durable))
	}
	if subOpts.bindStream != "" {
		natsOpts = append(natsOpts, nats.BindStream(subOpts.bindStream))
	}
	if subOpts.ackExplicit {
		natsOpts = append(natsOpts, nats.AckExplicit())
	}
	if subOpts.deliverAll {
		natsOpts = append(natsOpts, nats.DeliverAll())
	}

	sub, err := js.SubscribeSync(subject, natsOpts...)
	if err != nil {
		// Try fallback without BindStream if that was the issue
		if subOpts.bindStream != "" && subOpts.fallbackAutoDetect {
			c.log.Info("BindStream failed, trying auto-detect", "error", err.Error())
			natsOptsFallback := []nats.SubOpt{}
			if subOpts.durable != "" {
				natsOptsFallback = append(natsOptsFallback, nats.Durable(subOpts.durable))
			}
			if subOpts.ackExplicit {
				natsOptsFallback = append(natsOptsFallback, nats.AckExplicit())
			}
			if subOpts.deliverAll {
				natsOptsFallback = append(natsOptsFallback, nats.DeliverAll())
			}
			sub, err = js.SubscribeSync(subject, natsOptsFallback...)
		}
		if err != nil {
			return nil, fmt.Errorf("NATS subscribe to %s failed: %w", subject, err)
		}
	}

	return sub, nil
}

// CreateStream creates a JetStream stream with the given configuration.
func (c *JetStreamClient) CreateStream(config StreamConfig) error {
	if err := c.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	// Check if stream already exists
	_, err := js.StreamInfo(config.Name)
	if err == nil {
		return nil // Stream already exists
	}

	// Create stream
	streamConfig := &nats.StreamConfig{
		Name:      config.Name,
		Subjects:  config.Subjects,
		Retention: config.Retention.ToNATS(),
		Storage:   config.Storage.ToNATS(),
	}

	if config.MaxAge > 0 {
		streamConfig.MaxAge = config.MaxAge
	}
	if config.MaxMsgs > 0 {
		streamConfig.MaxMsgs = config.MaxMsgs
	}
	if config.Discard != "" {
		streamConfig.Discard = config.Discard.ToNATS()
	}

	_, err = js.AddStream(streamConfig)
	if err != nil {
		return fmt.Errorf("failed to create stream %s: %w", config.Name, err)
	}

	c.log.Info("Created JetStream stream", "name", config.Name, "retention", config.Retention)
	return nil
}

// DeleteStream deletes a JetStream stream.
func (c *JetStreamClient) DeleteStream(name string) error {
	if err := c.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	err := js.DeleteStream(name)
	if err != nil {
		return fmt.Errorf("failed to delete stream %s: %w", name, err)
	}

	c.log.Info("Deleted JetStream stream", "name", name)
	return nil
}

// StreamInfo returns information about a stream.
func (c *JetStreamClient) StreamInfo(name string) (*nats.StreamInfo, error) {
	if err := c.Connect(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	info, err := js.StreamInfo(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream info for %s: %w", name, err)
	}

	return info, nil
}

// EnsureConsumer creates or updates a JetStream consumer.
func (c *JetStreamClient) EnsureConsumer(stream, name string, config ConsumerConfig) error {
	if err := c.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	consumerConfig := &nats.ConsumerConfig{
		Durable: name,
	}

	if config.FilterSubject != "" {
		consumerConfig.FilterSubject = config.FilterSubject
	}
	if config.AckPolicy == AckExplicit {
		consumerConfig.AckPolicy = nats.AckExplicitPolicy
	}

	_, err := js.AddConsumer(stream, consumerConfig)
	if err != nil {
		return fmt.Errorf("failed to create consumer %s on stream %s: %w", name, stream, err)
	}

	return nil
}

// DeleteConsumer deletes a JetStream consumer.
func (c *JetStreamClient) DeleteConsumer(stream, consumer string) error {
	if err := c.Connect(); err != nil {
		return err
	}

	c.mu.Lock()
	js := c.js
	c.mu.Unlock()

	err := js.DeleteConsumer(stream, consumer)
	if err != nil {
		// Best effort - log but don't fail on consumer deletion errors
		c.log.Info("Failed to delete consumer (best effort)", "stream", stream, "consumer", consumer, "error", err.Error())
	}

	return nil
}

// PollMessage polls for a single message with a timeout.
func (c *JetStreamClient) PollMessage(subject string, timeout time.Duration, opts ...SubscribeOption) (*nats.Msg, error) {
	sub, err := c.Subscribe(subject, opts...)
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()

	msg, err := sub.NextMsg(timeout)
	if err != nil {
		if err == nats.ErrTimeout {
			return nil, nil // No message available, not an error
		}
		return nil, fmt.Errorf("NATS next msg: %w", err)
	}

	return msg, nil
}

// SubscribeOption configures subscription behavior.
type SubscribeOption func(*subscribeOptions)

type subscribeOptions struct {
	durable            string
	bindStream         string
	ackExplicit        bool
	deliverAll         bool
	fallbackAutoDetect bool
}

// WithDurable sets the durable consumer name.
func WithDurable(name string) SubscribeOption {
	return func(o *subscribeOptions) {
		o.durable = name
	}
}

// WithBindStream binds the subscription to a specific stream.
func WithBindStream(stream string) SubscribeOption {
	return func(o *subscribeOptions) {
		o.bindStream = stream
	}
}

// WithAckExplicit requires explicit acknowledgment of messages.
func WithAckExplicit() SubscribeOption {
	return func(o *subscribeOptions) {
		o.ackExplicit = true
	}
}

// WithDeliverAll delivers all messages from the start.
func WithDeliverAll() SubscribeOption {
	return func(o *subscribeOptions) {
		o.deliverAll = true
	}
}

// WithFallbackAutoDetect enables fallback to auto-detect stream if BindStream fails.
func WithFallbackAutoDetect() SubscribeOption {
	return func(o *subscribeOptions) {
		o.fallbackAutoDetect = true
	}
}
