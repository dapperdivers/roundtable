package util

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// IsValidK8sName validates a name against RFC 1123 DNS label rules.
// Names must be lowercase alphanumeric or '-', start and end with alphanumeric,
// and be 1-63 characters long.
func IsValidK8sName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		return false
	}
	return true
}

// IsValidSkillName validates a skill name (alphanumeric with hyphens).
// Allows hyphens anywhere in the string, unlike K8s names.
func IsValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' {
			continue
		}
		return false
	}
	return true
}

// BoolPtr returns a pointer to the given bool value.
func BoolPtr(b bool) *bool {
	return &b
}

// FSGroupChangePolicyPtr returns a pointer to the given PodFSGroupChangePolicy value.
func FSGroupChangePolicyPtr(p corev1.PodFSGroupChangePolicy) *corev1.PodFSGroupChangePolicy {
	return &p
}

// IntstrPort converts an int port to IntOrString type.
func IntstrPort(port int) intstr.IntOrString {
	return intstr.FromInt32(int32(port))
}
