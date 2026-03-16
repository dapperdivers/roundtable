package util

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// SanitizeK8sName converts a string to a valid RFC 1123 DNS label.
// Replaces underscores with hyphens, lowercases, strips invalid chars,
// and trims leading/trailing hyphens.
func SanitizeK8sName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		}
	}
	name = strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = name[:63]
		name = strings.TrimRight(name, "-")
	}
	return name
}

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
