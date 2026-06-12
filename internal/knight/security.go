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

package knight

import (
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/dapperdivers/roundtable/internal/util"
)

// PodSecurity is the single source of truth for the pod-level security context
// applied to every knight-owned pod — both knight Deployments and the Nix
// build Jobs. The chart sets these once (operator env vars) and the operator
// reuses them everywhere, so the knight pods and the shared-store builder
// always agree (notably on fsGroup/FSGroupChangePolicy, which the read-only
// shared Nix store depends on).
type PodSecurity struct {
	RunAsUser           int64
	RunAsGroup          int64
	FSGroup             int64
	FSGroupChangePolicy corev1.PodFSGroupChangePolicy
}

// DefaultPodSecurity is the built-in default: uid/gid/fsGroup 1000 with
// OnRootMismatch so kubelet does not recursively chown the (large) shared Nix
// store on every mount.
func DefaultPodSecurity() PodSecurity {
	return PodSecurity{
		RunAsUser:           1000,
		RunAsGroup:          1000,
		FSGroup:             1000,
		FSGroupChangePolicy: corev1.FSGroupChangeOnRootMismatch,
	}
}

// PodSecurityFromEnv reads chart-provided overrides, falling back to defaults:
//
//	KNIGHT_RUN_AS_USER, KNIGHT_RUN_AS_GROUP, KNIGHT_FS_GROUP (ints)
//	KNIGHT_FS_GROUP_CHANGE_POLICY (Always | OnRootMismatch)
func PodSecurityFromEnv() PodSecurity {
	s := DefaultPodSecurity()
	if v, err := strconv.ParseInt(os.Getenv("KNIGHT_RUN_AS_USER"), 10, 64); err == nil && v > 0 {
		s.RunAsUser = v
	}
	if v, err := strconv.ParseInt(os.Getenv("KNIGHT_RUN_AS_GROUP"), 10, 64); err == nil && v > 0 {
		s.RunAsGroup = v
	}
	if v, err := strconv.ParseInt(os.Getenv("KNIGHT_FS_GROUP"), 10, 64); err == nil && v > 0 {
		s.FSGroup = v
	}
	if v := os.Getenv("KNIGHT_FS_GROUP_CHANGE_POLICY"); v != "" {
		s.FSGroupChangePolicy = corev1.PodFSGroupChangePolicy(v)
	}
	return s
}

// OrDefault returns DefaultPodSecurity when the receiver is the zero value, so
// callers that forget to set it never accidentally run pods as root (uid 0).
func (s PodSecurity) OrDefault() PodSecurity {
	if s.RunAsUser == 0 && s.RunAsGroup == 0 && s.FSGroup == 0 && s.FSGroupChangePolicy == "" {
		return DefaultPodSecurity()
	}
	return s
}

// PodSecurityContext renders the pod-level security context.
func (s PodSecurity) PodSecurityContext() *corev1.PodSecurityContext {
	s = s.OrDefault()
	runAsUser := s.RunAsUser
	runAsGroup := s.RunAsGroup
	fsGroup := s.FSGroup
	return &corev1.PodSecurityContext{
		RunAsUser:           &runAsUser,
		RunAsGroup:          &runAsGroup,
		RunAsNonRoot:        util.BoolPtr(runAsUser != 0),
		FSGroup:             &fsGroup,
		FSGroupChangePolicy: util.FSGroupChangePolicyPtr(s.FSGroupChangePolicy),
	}
}
