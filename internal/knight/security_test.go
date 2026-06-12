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
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestDefaultPodSecurityContext(t *testing.T) {
	sc := DefaultPodSecurity().PodSecurityContext()
	if *sc.RunAsUser != 1000 || *sc.RunAsGroup != 1000 || *sc.FSGroup != 1000 {
		t.Errorf("expected 1000/1000/1000, got %d/%d/%d", *sc.RunAsUser, *sc.RunAsGroup, *sc.FSGroup)
	}
	if !*sc.RunAsNonRoot {
		t.Error("expected RunAsNonRoot=true")
	}
	if *sc.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Errorf("expected OnRootMismatch, got %s", *sc.FSGroupChangePolicy)
	}
}

func TestPodSecurityOrDefault(t *testing.T) {
	// Zero value must not produce a root (uid 0) pod.
	sc := (PodSecurity{}).PodSecurityContext()
	if *sc.RunAsUser != 1000 {
		t.Errorf("zero value should default to 1000, got %d", *sc.RunAsUser)
	}
	if !*sc.RunAsNonRoot {
		t.Error("zero value should still be non-root")
	}

	// Explicit values are preserved.
	custom := PodSecurity{RunAsUser: 2000, RunAsGroup: 2000, FSGroup: 3000, FSGroupChangePolicy: corev1.FSGroupChangeAlways}
	if custom.OrDefault().RunAsUser != 2000 || custom.OrDefault().FSGroup != 3000 {
		t.Error("explicit values should be preserved by OrDefault")
	}
}

func TestPodSecurityFromEnv(t *testing.T) {
	t.Setenv("KNIGHT_RUN_AS_USER", "2000")
	t.Setenv("KNIGHT_FS_GROUP", "3000")
	t.Setenv("KNIGHT_FS_GROUP_CHANGE_POLICY", "Always")
	s := PodSecurityFromEnv()
	if s.RunAsUser != 2000 {
		t.Errorf("expected RunAsUser 2000, got %d", s.RunAsUser)
	}
	if s.RunAsGroup != 1000 {
		t.Errorf("unset RunAsGroup should default to 1000, got %d", s.RunAsGroup)
	}
	if s.FSGroup != 3000 {
		t.Errorf("expected FSGroup 3000, got %d", s.FSGroup)
	}
	if s.FSGroupChangePolicy != corev1.FSGroupChangeAlways {
		t.Errorf("expected Always, got %s", s.FSGroupChangePolicy)
	}
}
