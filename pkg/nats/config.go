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
	"time"

	"github.com/nats-io/nats.go"
)

// Config holds NATS connection configuration.
type Config struct {
	// URL is the NATS server URL (e.g., "nats://nats.database.svc:4222").
	URL string

	// RetryOnFailedConnect enables automatic retry on initial connection failure.
	RetryOnFailedConnect bool

	// MaxReconnects is the maximum number of reconnect attempts. -1 for infinite.
	MaxReconnects int

	// ReconnectWait is the duration to wait between reconnect attempts.
	ReconnectWait time.Duration
}

// DefaultConfig returns a Config with sensible defaults for the Round Table operator.
func DefaultConfig() Config {
	return Config{
		URL:                  "nats://nats.database.svc:4222",
		RetryOnFailedConnect: true,
		MaxReconnects:        -1, // Infinite reconnects
		ReconnectWait:        2 * time.Second,
	}
}

// StreamConfig defines JetStream stream configuration.
type StreamConfig struct {
	// Name is the stream name (e.g., "fleet_a_tasks").
	Name string

	// Subjects is the list of subjects this stream captures (e.g., ["fleet-a.tasks.>"]).
	Subjects []string

	// Retention policy for message retention.
	Retention RetentionPolicy

	// MaxAge is the maximum age of messages in the stream (0 = unlimited).
	MaxAge time.Duration

	// MaxMsgs is the maximum number of messages (0 = unlimited).
	MaxMsgs int64

	// Storage type (File or Memory).
	Storage StorageType

	// Discard policy when limits are exceeded.
	Discard DiscardPolicy
}

// RetentionPolicy defines how messages are retained.
type RetentionPolicy string

const (
	// RetentionLimits retains messages based on limits (MaxAge, MaxMsgs).
	RetentionLimits RetentionPolicy = "Limits"

	// RetentionWorkQueue auto-deletes messages after acknowledgment.
	// Recommended for ephemeral mission tables.
	RetentionWorkQueue RetentionPolicy = "WorkQueue"

	// RetentionInterest retains messages as long as there are consumers.
	RetentionInterest RetentionPolicy = "Interest"
)

// ToNATS converts RetentionPolicy to nats.RetentionPolicy.
func (r RetentionPolicy) ToNATS() nats.RetentionPolicy {
	switch r {
	case RetentionLimits:
		return nats.LimitsPolicy
	case RetentionWorkQueue:
		return nats.WorkQueuePolicy
	case RetentionInterest:
		return nats.InterestPolicy
	default:
		return nats.WorkQueuePolicy
	}
}

// StorageType defines stream storage backend.
type StorageType string

const (
	// StorageFile uses file-based storage.
	StorageFile StorageType = "File"

	// StorageMemory uses memory-based storage (faster but not persistent).
	StorageMemory StorageType = "Memory"
)

// ToNATS converts StorageType to nats.StorageType.
func (s StorageType) ToNATS() nats.StorageType {
	switch s {
	case StorageMemory:
		return nats.MemoryStorage
	default:
		return nats.FileStorage
	}
}

// DiscardPolicy defines what happens when stream limits are exceeded.
type DiscardPolicy string

const (
	// DiscardOld removes the oldest messages when limits are hit.
	DiscardOld DiscardPolicy = "Old"

	// DiscardNew rejects new messages when limits are hit.
	DiscardNew DiscardPolicy = "New"
)

// ToNATS converts DiscardPolicy to nats.DiscardPolicy.
func (d DiscardPolicy) ToNATS() nats.DiscardPolicy {
	switch d {
	case DiscardNew:
		return nats.DiscardNew
	default:
		return nats.DiscardOld
	}
}

// ConsumerConfig defines JetStream consumer configuration.
type ConsumerConfig struct {
	// Durable is the durable consumer name.
	Durable string

	// FilterSubject is the subject filter for this consumer.
	FilterSubject string

	// AckPolicy defines how messages are acknowledged.
	AckPolicy AckPolicy

	// DeliverPolicy defines where to start delivering messages.
	DeliverPolicy DeliverPolicy

	// BindStream is the stream name to bind this consumer to.
	BindStream string
}

// AckPolicy defines message acknowledgment behavior.
type AckPolicy string

const (
	// AckExplicit requires explicit ack from consumer.
	AckExplicit AckPolicy = "Explicit"

	// AckAll acks all messages up to this one.
	AckAll AckPolicy = "All"

	// AckNone does not require acks.
	AckNone AckPolicy = "None"
)

// DeliverPolicy defines where to start delivering messages.
type DeliverPolicy string

const (
	// DeliverAll delivers all messages from the start.
	DeliverAll DeliverPolicy = "All"

	// DeliverLast delivers only the last message.
	DeliverLast DeliverPolicy = "Last"

	// DeliverNew delivers only new messages.
	DeliverNew DeliverPolicy = "New"
)
