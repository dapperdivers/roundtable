package util

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestIsValidK8sName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid lowercase name",
			input: "my-knight",
			want:  true,
		},
		{
			name:  "valid name with numbers",
			input: "knight-123",
			want:  true,
		},
		{
			name:  "valid name all lowercase",
			input: "galahad",
			want:  true,
		},
		{
			name:  "invalid uppercase",
			input: "MyKnight",
			want:  false,
		},
		{
			name:  "invalid starts with hyphen",
			input: "-knight",
			want:  false,
		},
		{
			name:  "invalid ends with hyphen",
			input: "knight-",
			want:  false,
		},
		{
			name:  "invalid special characters",
			input: "my_knight",
			want:  false,
		},
		{
			name:  "invalid too long",
			input: "this-is-a-very-long-name-that-exceeds-the-maximum-length-of-sixtythree-characters",
			want:  false,
		},
		{
			name:  "invalid empty",
			input: "",
			want:  false,
		},
		{
			name:  "valid max length",
			input: "a123456789012345678901234567890123456789012345678901234567890ab",
			want:  true,
		},
		{
			name:  "valid single character",
			input: "a",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidK8sName(tt.input); got != tt.want {
				t.Errorf("IsValidK8sName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidSkillName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid lowercase name",
			input: "my-skill",
			want:  true,
		},
		{
			name:  "valid name with numbers",
			input: "skill-123",
			want:  true,
		},
		{
			name:  "valid starts with hyphen (allowed for skills)",
			input: "-skill",
			want:  true,
		},
		{
			name:  "valid ends with hyphen (allowed for skills)",
			input: "skill-",
			want:  true,
		},
		{
			name:  "invalid uppercase",
			input: "MySkill",
			want:  false,
		},
		{
			name:  "invalid special characters",
			input: "my_skill",
			want:  false,
		},
		{
			name:  "invalid too long",
			input: "this-is-a-very-long-skill-name-that-exceeds-sixtythree-chars-limit",
			want:  false,
		},
		{
			name:  "invalid empty",
			input: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidSkillName(tt.input); got != tt.want {
				t.Errorf("IsValidSkillName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBoolPtr(t *testing.T) {
	tests := []struct {
		name string
		b    bool
	}{
		{name: "true", b: true},
		{name: "false", b: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BoolPtr(tt.b)
			if got == nil {
				t.Error("BoolPtr() returned nil")
				return
			}
			if *got != tt.b {
				t.Errorf("BoolPtr(%v) = %v, want %v", tt.b, *got, tt.b)
			}
		})
	}
}

func TestFSGroupChangePolicyPtr(t *testing.T) {
	tests := []struct {
		name   string
		policy corev1.PodFSGroupChangePolicy
	}{
		{name: "OnRootMismatch", policy: corev1.FSGroupChangeOnRootMismatch},
		{name: "Always", policy: corev1.FSGroupChangeAlways},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FSGroupChangePolicyPtr(tt.policy)
			if got == nil {
				t.Error("FSGroupChangePolicyPtr() returned nil")
				return
			}
			if *got != tt.policy {
				t.Errorf("FSGroupChangePolicyPtr(%v) = %v, want %v", tt.policy, *got, tt.policy)
			}
		})
	}
}

func TestIntstrPort(t *testing.T) {
	tests := []struct {
		name string
		port int
		want int32
	}{
		{name: "port 80", port: 80, want: 80},
		{name: "port 443", port: 443, want: 443},
		{name: "port 8080", port: 8080, want: 8080},
		{name: "port 0", port: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IntstrPort(tt.port)
			if got.Type != intstr.Int {
				t.Errorf("IntstrPort(%d) type = %v, want Int", tt.port, got.Type)
			}
			if got.IntVal != tt.want {
				t.Errorf("IntstrPort(%d) = %v, want %v", tt.port, got.IntVal, tt.want)
			}
		})
	}
}

func TestSanitizeK8sName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"security_scanner", "security-scanner"},
		{"report_writer", "report-writer"},
		{"already-valid", "already-valid"},
		{"MixedCase_Name", "mixedcase-name"},
		{"__leading__", "leading"},
		{"has spaces!", "hasspaces"},
		{"", ""},
		{"a", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeK8sName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeK8sName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
			if got != "" && !IsValidK8sName(got) {
				t.Errorf("SanitizeK8sName(%q) = %q is not a valid K8s name", tt.input, got)
			}
		})
	}
}
