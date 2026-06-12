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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	knightpkg "github.com/dapperdivers/roundtable/internal/knight"
)

// buildQueueDir returns the mount path of the RWX build-queue PVC the operator
// shares with the roundtable-nix-builder Deployment. When this directory exists
// the operator dispatches Nix builds through the queue (the persistent
// nix-daemon builder); when it is absent it falls back to the legacy per-knight
// build Job. Overridable via BUILD_QUEUE_DIR for tests.
func buildQueueDir() string {
	if d := os.Getenv("BUILD_QUEUE_DIR"); d != "" {
		return d
	}
	return "/queue"
}

// reconcileNixBuild ensures the knight's Nix tools are built into the shared
// store. It prefers the queue-backed nix-daemon builder and falls back to the
// legacy build Job when the queue PVC is not mounted. It returns a non-zero
// requeue interval while a build is pending so the operator polls for the
// result — queue results are plain files, not watched Kubernetes objects.
func (r *KnightReconciler) reconcileNixBuild(ctx context.Context, knight *aiv1alpha1.Knight) (time.Duration, error) {
	queue := buildQueueDir()
	if _, err := os.Stat(queue); err != nil {
		// Queue not mounted — legacy Job path (requeue driven by owner watch).
		return 0, r.reconcileNixBuildJob(ctx, knight)
	}
	return r.reconcileNixBuildQueue(ctx, knight, queue)
}

// reconcileNixBuildQueue drives a build through the file-based work queue. The
// queue protocol (keyed by "<knight>__<toolsHash>") is documented in
// pi-knight's scripts/nix-builder.sh.
func (r *KnightReconciler) reconcileNixBuildQueue(ctx context.Context, knight *aiv1alpha1.Knight, queue string) (time.Duration, error) {
	log := logf.FromContext(ctx)

	// GenerateFlakeNix renders from Spec.Tools.Nix; gate on the same field so a
	// legacy NixPackages-only knight (which it can't render) takes the Job path.
	if knight.Spec.Tools == nil || len(knight.Spec.Tools.Nix) == 0 {
		return 0, nil
	}

	currentHash := knightpkg.NixToolsHash(knight)
	if knight.Status.NixToolsHash == currentHash {
		return 0, nil // already published
	}

	key := knight.Name + "__" + currentHash
	reqDir := filepath.Join(queue, "requests", key)
	okPath := filepath.Join(queue, "results", key+".ok")
	errPath := filepath.Join(queue, "results", key+".err")

	// Builder published a result?
	if _, err := os.Stat(okPath); err == nil {
		knight.Status.NixToolsHash = currentHash // persisted by updateStatus
		r.Recorder.Eventf(knight, corev1.EventTypeNormal, "NixBuildComplete",
			"Nix tools (hash %s) published to shared store", currentHash)
		_ = os.RemoveAll(reqDir) // best-effort cleanup of the consumed request
		_ = os.Remove(okPath)
		_ = os.Remove(errPath)
		r.pruneStaleBuildRequests(knight.Name, currentHash, queue)
		return 0, nil
	}

	if _, err := os.Stat(errPath); err == nil {
		// Surface the failure, but don't auto-loop on a genuinely broken flake.
		// Deleting the .err marker (or changing the tool set) re-arms the build.
		r.Recorder.Eventf(knight, corev1.EventTypeWarning, "NixBuildFailed",
			"Nix build for hash %s failed; delete results/%s.err to retry", currentHash, key)
		return RequeueVerySlow, nil
	}

	// No result yet — ensure exactly one ready request exists for this hash.
	if _, err := os.Stat(filepath.Join(reqDir, "ready")); err == nil {
		return RequeueSlow, nil // in flight
	}

	if err := writeBuildRequest(reqDir, knightpkg.GenerateFlakeNix(knight)); err != nil {
		return 0, fmt.Errorf("write nix build request: %w", err)
	}
	r.pruneStaleBuildRequests(knight.Name, currentHash, queue)
	log.Info("Nix build requested via queue", "knight", knight.Name, "toolsHash", currentHash)
	r.Recorder.Eventf(knight, corev1.EventTypeNormal, "NixBuildRequested",
		"Requested Nix tools build (hash %s) from shared builder", currentHash)
	return RequeueSlow, nil
}

// writeBuildRequest writes flake.nix and then the ready marker last, so the
// builder never observes a partially written request.
func writeBuildRequest(reqDir, flake string) error {
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(reqDir, "flake.nix"), []byte(flake), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(reqDir, "ready"), nil, 0o644)
}

// pruneStaleBuildRequests removes request dirs and result markers belonging to
// this knight whose hash differs from keepHash (superseded tool sets), keeping
// the queue from accumulating cruft as tools change mid-reconcile.
func (r *KnightReconciler) pruneStaleBuildRequests(knightName, keepHash, queue string) {
	prefix := knightName + "__"
	keepKey := knightName + "__" + keepHash

	if entries, err := os.ReadDir(filepath.Join(queue, "requests")); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), prefix) && e.Name() != keepKey {
				_ = os.RemoveAll(filepath.Join(queue, "requests", e.Name()))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(queue, "results")); err == nil {
		for _, e := range entries {
			base := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".ok"), ".err")
			if strings.HasPrefix(base, prefix) && base != keepKey {
				_ = os.Remove(filepath.Join(queue, "results", e.Name()))
			}
		}
	}
}
