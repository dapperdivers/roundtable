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

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
)

func nixQueueKnight() *aiv1alpha1.Knight {
	return &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{Name: "agravain", Namespace: "roundtable"},
		Spec: aiv1alpha1.KnightSpec{
			Tools: &aiv1alpha1.KnightTools{Nix: []string{"nmap", "curl"}},
		},
	}
}

// TestReconcileNixBuildQueue_Lifecycle walks a build through the file queue:
// first reconcile writes a ready request, then the builder's .ok result is
// consumed (status set, request + result cleaned up).
func TestReconcileNixBuildQueue_Lifecycle(t *testing.T) {
	queue := t.TempDir()
	r := &KnightReconciler{Recorder: record.NewFakeRecorder(100)}
	knight := nixQueueKnight()
	hash := knightpkg.NixToolsHash(knight)
	key := knight.Name + "__" + hash

	// 1. No request yet → writes flake + ready, asks to requeue.
	d, err := r.reconcileNixBuildQueue(context.Background(), knight, queue)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if d <= 0 {
		t.Fatalf("expected a poll requeue while pending, got %v", d)
	}
	flake := filepath.Join(queue, "requests", key, "flake.nix")
	if b, err := os.ReadFile(flake); err != nil {
		t.Fatalf("flake.nix not written: %v", err)
	} else if len(b) == 0 {
		t.Fatal("flake.nix is empty")
	}
	if _, err := os.Stat(filepath.Join(queue, "requests", key, "ready")); err != nil {
		t.Fatalf("ready marker not written: %v", err)
	}

	// 2. Reconcile again while pending → still requeues, no duplicate work.
	if _, err := r.reconcileNixBuildQueue(context.Background(), knight, queue); err != nil {
		t.Fatalf("pending reconcile: %v", err)
	}

	// 3. Builder publishes a success result (the builder owns results/).
	if err := os.MkdirAll(filepath.Join(queue, "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queue, "results", key+".ok"), []byte("/nix/store/xxx 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err = r.reconcileNixBuildQueue(context.Background(), knight, queue)
	if err != nil {
		t.Fatalf("result reconcile: %v", err)
	}
	if knight.Status.NixToolsHash != hash {
		t.Fatalf("status hash = %q, want %q", knight.Status.NixToolsHash, hash)
	}
	if d != 0 {
		t.Fatalf("expected no requeue after completion, got %v", d)
	}
	if _, err := os.Stat(filepath.Join(queue, "requests", key)); !os.IsNotExist(err) {
		t.Fatal("request dir should be cleaned up after success")
	}
	if _, err := os.Stat(filepath.Join(queue, "results", key+".ok")); !os.IsNotExist(err) {
		t.Fatal(".ok marker should be cleaned up after success")
	}

	// 4. Already published → no-op.
	if _, err := r.reconcileNixBuildQueue(context.Background(), knight, queue); err != nil {
		t.Fatalf("post-publish reconcile: %v", err)
	}
}

// TestReconcileNixBuildQueue_Failure surfaces a builder .err without looping.
func TestReconcileNixBuildQueue_Failure(t *testing.T) {
	queue := t.TempDir()
	r := &KnightReconciler{Recorder: record.NewFakeRecorder(100)}
	knight := nixQueueKnight()
	hash := knightpkg.NixToolsHash(knight)
	key := knight.Name + "__" + hash

	if _, err := r.reconcileNixBuildQueue(context.Background(), knight, queue); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(queue, "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queue, "results", key+".err"), []byte("boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := r.reconcileNixBuildQueue(context.Background(), knight, queue)
	if err != nil {
		t.Fatalf("failure reconcile: %v", err)
	}
	if knight.Status.NixToolsHash == hash {
		t.Fatal("status hash must not be set on a failed build")
	}
	if d <= 0 {
		t.Fatal("a failed build should still requeue (slowly) for a retry window")
	}
}

// TestPruneStaleBuildRequests drops requests/results for superseded hashes.
func TestPruneStaleBuildRequests(t *testing.T) {
	queue := t.TempDir()
	r := &KnightReconciler{Recorder: record.NewFakeRecorder(100)}
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(queue, "requests", "agravain__OLDHASH", "ready"))
	mk(filepath.Join(queue, "requests", "agravain__KEEP", "ready"))
	mk(filepath.Join(queue, "requests", "kay__OTHER", "ready")) // different knight, untouched
	mk(filepath.Join(queue, "results", "agravain__OLDHASH.err"))

	r.pruneStaleBuildRequests("agravain", "KEEP", queue)

	if _, err := os.Stat(filepath.Join(queue, "requests", "agravain__OLDHASH")); !os.IsNotExist(err) {
		t.Fatal("stale request for agravain should be pruned")
	}
	if _, err := os.Stat(filepath.Join(queue, "results", "agravain__OLDHASH.err")); !os.IsNotExist(err) {
		t.Fatal("stale result for agravain should be pruned")
	}
	if _, err := os.Stat(filepath.Join(queue, "requests", "agravain__KEEP")); err != nil {
		t.Fatal("current-hash request must be kept")
	}
	if _, err := os.Stat(filepath.Join(queue, "requests", "kay__OTHER")); err != nil {
		t.Fatal("another knight's request must not be touched")
	}
}
