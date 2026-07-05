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

// Package notify delivers completion webhooks for Chains and Missions.
// The payload is vendor-neutral (schema roundtable.notify/v1); receivers
// adapt it to their own actions. Delivery is at-least-once within a bounded
// retry window — receivers must dedupe on the idempotency key.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchemaV1 identifies the payload schema. Changes within v1 are additive only.
const SchemaV1 = "roundtable.notify/v1"

const (
	// OutputCap bounds the inline output field; full results stay in NATS KV
	// (see Payload.OutputRef). Discord messages cap at 2k chars anyway.
	OutputCap = 8 * 1024

	// DefaultTimeout bounds a single delivery attempt so a hung receiver
	// cannot stall the reconcile work-queue.
	DefaultTimeout = 5 * time.Second

	// DefaultGiveUpAfter bounds the retry window, measured from the
	// resource's completion time.
	DefaultGiveUpAfter = 15 * time.Minute

	// IdempotencyHeader mirrors Payload.IdempotencyKey for receivers that
	// dedupe at the HTTP layer.
	IdempotencyHeader = "X-Roundtable-Idempotency-Key"
)

// Payload is the roundtable.notify/v1 completion payload.
type Payload struct {
	Schema         string            `json:"schema"`
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace"`
	UID            string            `json:"uid"`
	Phase          string            `json:"phase"`
	RoundTableRef  string            `json:"roundTableRef,omitempty"`
	StartedAt      *metav1.Time      `json:"startedAt,omitempty"`
	FinishedAt     *metav1.Time      `json:"finishedAt,omitempty"`
	Steps          []StepSummary     `json:"steps,omitempty"`
	Output         string            `json:"output,omitempty"`
	Truncated      bool              `json:"truncated,omitempty"`
	OutputRef      *OutputRef        `json:"outputRef,omitempty"`
	Context        map[string]string `json:"context,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey"`
}

// StepSummary is a chain step's terminal state (chains only).
type StepSummary struct {
	Name   string `json:"name"`
	Knight string `json:"knight,omitempty"`
	Phase  string `json:"phase"`
}

// OutputRef points at where the full, untruncated results live.
type OutputRef struct {
	NATSKV *NATSKVRef `json:"natsKV,omitempty"`
}

// NATSKVRef references a NATS KV entry.
type NATSKVRef struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

// Notifier posts completion payloads to allowlisted webhook sinks.
type Notifier struct {
	// Client is the HTTP client used for deliveries. Its Timeout should be
	// short (DefaultTimeout) so a hung receiver cannot stall reconciles.
	Client *http.Client

	// AllowedURLPrefixes is the SSRF guard: a sink URL must match one of
	// these prefixes or delivery is rejected outright. Empty means all
	// notifications are rejected (feature disabled).
	AllowedURLPrefixes []string

	// GiveUpAfter bounds the retry window measured from resource completion.
	GiveUpAfter time.Duration
}

// NewNotifier builds a Notifier with default timeout and retry window.
func NewNotifier(allowedURLPrefixes []string) *Notifier {
	return &Notifier{
		Client:             &http.Client{Timeout: DefaultTimeout},
		AllowedURLPrefixes: allowedURLPrefixes,
		GiveUpAfter:        DefaultGiveUpAfter,
	}
}

// URLAllowed reports whether the sink URL matches the operator allowlist.
func (n *Notifier) URLAllowed(url string) bool {
	for _, prefix := range n.AllowedURLPrefixes {
		if prefix != "" && strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// Expired reports whether the retry window for a resource that completed at
// completedAt has been exhausted.
func (n *Notifier) Expired(completedAt time.Time) bool {
	giveUp := n.GiveUpAfter
	if giveUp == 0 {
		giveUp = DefaultGiveUpAfter
	}
	return time.Since(completedAt) > giveUp
}

// Backoff returns the requeue delay for the next retry, derived from time
// elapsed since completion: it doubles naturally (10s floor, 5m cap) without
// needing an attempt counter persisted anywhere.
func Backoff(completedAt time.Time) time.Duration {
	elapsed := time.Since(completedAt)
	if elapsed < 10*time.Second {
		return 10 * time.Second
	}
	if elapsed > 5*time.Minute {
		return 5 * time.Minute
	}
	return elapsed
}

// Truncate caps s at OutputCap bytes, reporting whether it was cut.
func Truncate(s string) (string, bool) {
	if len(s) <= OutputCap {
		return s, false
	}
	return s[:OutputCap], true
}

// Deliver posts the payload to url with an optional bearer token. Any
// non-2xx response or transport error is returned as an error; the caller
// decides whether to retry. The token is never included in returned errors.
func (n *Notifier) Deliver(ctx context.Context, url, token string, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(IdempotencyHeader, payload.IdempotencyKey)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := n.Client.Do(req)
	if err != nil {
		return fmt.Errorf("post notification: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %s", resp.Status)
	}
	return nil
}
