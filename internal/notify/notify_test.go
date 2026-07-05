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

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestURLAllowed(t *testing.T) {
	n := NewNotifier([]string{"http://molt.ai.svc:18789/", "https://hooks.example.com/rt/"})

	cases := []struct {
		url  string
		want bool
	}{
		{"http://molt.ai.svc:18789/hooks/agent", true},
		{"https://hooks.example.com/rt/wake", true},
		{"http://molt.ai.svc:18789.evil.com/hooks", false},
		{"http://169.254.169.254/latest/meta-data", false},
		{"https://molt.ai.svc:18789/hooks/agent", false}, // scheme must match too
		{"", false},
	}
	for _, tc := range cases {
		if got := n.URLAllowed(tc.url); got != tc.want {
			t.Errorf("URLAllowed(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}

	empty := NewNotifier(nil)
	if empty.URLAllowed("http://molt.ai.svc:18789/hooks/agent") {
		t.Error("empty allowlist must reject everything")
	}
}

func TestDeliverSendsHeadersAndBody(t *testing.T) {
	var gotAuth, gotIdem, gotContentType string
	var gotBody Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get(IdempotencyHeader)
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier([]string{srv.URL})
	payload := Payload{
		Schema:         SchemaV1,
		Kind:           "Chain",
		Name:           "molt-fix-the-thing",
		Namespace:      "roundtable",
		UID:            "abc-123",
		Phase:          "Succeeded",
		Context:        map[string]string{"sessionKey": "discord:channel:123456"},
		IdempotencyKey: "abc-123/run-1/Succeeded",
	}
	if err := n.Deliver(context.Background(), srv.URL, "sekrit", payload); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if gotAuth != "Bearer sekrit" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotIdem != "abc-123/run-1/Succeeded" {
		t.Errorf("idempotency header = %q", gotIdem)
	}
	if gotContentType != "application/json" {
		t.Errorf("content type = %q", gotContentType)
	}
	if gotBody.Schema != SchemaV1 || gotBody.IdempotencyKey != payload.IdempotencyKey ||
		gotBody.Context["sessionKey"] != "discord:channel:123456" {
		t.Errorf("body mismatch: %+v", gotBody)
	}
}

func TestDeliverNoTokenOmitsAuthorization(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
	}))
	defer srv.Close()

	n := NewNotifier([]string{srv.URL})
	if err := n.Deliver(context.Background(), srv.URL, "", Payload{Schema: SchemaV1}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if sawAuth {
		t.Error("Authorization header sent despite empty token")
	}
}

func TestDeliverNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier([]string{srv.URL})
	err := n.Deliver(context.Background(), srv.URL, "sekrit", Payload{Schema: SchemaV1})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if strings.Contains(err.Error(), "sekrit") {
		t.Error("error message leaks bearer token")
	}
}

func TestTruncate(t *testing.T) {
	short, cut := Truncate("hello")
	if short != "hello" || cut {
		t.Errorf("short string should be untouched, got (%q, %v)", short, cut)
	}
	long, cut := Truncate(strings.Repeat("x", OutputCap+1))
	if len(long) != OutputCap || !cut {
		t.Errorf("long string should be capped at %d, got (%d, %v)", OutputCap, len(long), cut)
	}
}

func TestBackoffAndExpiry(t *testing.T) {
	if d := Backoff(time.Now()); d != 10*time.Second {
		t.Errorf("fresh completion backoff = %v, want 10s", d)
	}
	if d := Backoff(time.Now().Add(-time.Hour)); d != 5*time.Minute {
		t.Errorf("old completion backoff = %v, want 5m", d)
	}
	if d := Backoff(time.Now().Add(-time.Minute)); d < 55*time.Second || d > 65*time.Second {
		t.Errorf("1m-old completion backoff = %v, want ~1m", d)
	}

	n := NewNotifier(nil)
	if n.Expired(time.Now()) {
		t.Error("fresh completion should not be expired")
	}
	if !n.Expired(time.Now().Add(-16 * time.Minute)) {
		t.Error("16m-old completion should be expired (default window 15m)")
	}
	n.GiveUpAfter = time.Hour
	if n.Expired(time.Now().Add(-16 * time.Minute)) {
		t.Error("custom window should be honored")
	}
}
