package mission

import (
	"testing"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── extractJSON ────────────────────────────────────────────────────────────

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"key":"value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "json code fence",
			input: "some text\n```json\n{\"key\":\"value\"}\n```\nmore text",
			want:  `{"key":"value"}`,
		},
		{
			name:  "generic code fence",
			input: "```\n{\"key\":\"value\"}\n```",
			want:  `{"key":"value"}`,
		},
		{
			name:  "whitespace around JSON",
			input: "   {\"key\":\"value\"}   ",
			want:  `{"key":"value"}`,
		},
		{
			name:  "json fence with extra whitespace",
			input: "```json\n  {\"a\":1}  \n```",
			want:  `{"a":1}`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "text before json fence",
			input: "Here is the plan:\n```json\n{\"plan\":true}\n```\nEnd.",
			want:  `{"plan":true}`,
		},
		{
			name:  "no closing fence after json tag",
			input: "```json\n{\"open\":true}",
			want:  `{"open":true}`,
		},
		{
			name:  "nested fences - extracts first json block",
			input: "```json\n{\"first\":1}\n```\n```json\n{\"second\":2}\n```",
			want:  `{"first":1}`,
		},
		{
			name:  "multiline JSON in fence",
			input: "```json\n{\n  \"chains\": [],\n  \"knights\": []\n}\n```",
			want:  "{\n  \"chains\": [],\n  \"knights\": []\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ─── parsePlannerOutput ─────────────────────────────────────────────────────

func TestParsePlannerOutput(t *testing.T) {
	p := &Planner{}

	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, po *PlannerOutput)
	}{
		{
			name: "valid plan",
			input: `{
				"planVersion": "v1alpha1",
				"metadata": {"objective": "test"},
				"knights": [{"name": "k1", "role": "tester", "ephemeral": true}],
				"chains": [{"name": "c1", "steps": [{"name": "s1", "knightRef": "k1", "task": "do it"}]}]
			}`,
			wantErr: false,
			check: func(t *testing.T, po *PlannerOutput) {
				if po.PlanVersion != "v1alpha1" {
					t.Errorf("PlanVersion = %q, want v1alpha1", po.PlanVersion)
				}
				if po.Metadata.Objective != "test" {
					t.Errorf("Objective = %q, want test", po.Metadata.Objective)
				}
				if len(po.Knights) != 1 {
					t.Fatalf("Knights count = %d, want 1", len(po.Knights))
				}
				if po.Knights[0].Name != "k1" {
					t.Errorf("Knight name = %q, want k1", po.Knights[0].Name)
				}
				if len(po.Chains) != 1 {
					t.Fatalf("Chains count = %d, want 1", len(po.Chains))
				}
			},
		},
		{
			name:    "invalid JSON",
			input:   `{not json at all`,
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name: "JSON in markdown fence",
			input: "```json\n" + `{"planVersion":"v1alpha1","metadata":{"objective":"fenced"}}` + "\n```",
			wantErr: false,
			check: func(t *testing.T, po *PlannerOutput) {
				if po.Metadata.Objective != "fenced" {
					t.Errorf("Objective = %q, want fenced", po.Metadata.Objective)
				}
			},
		},
		{
			name: "minimal valid - empty arrays",
			input: `{"planVersion":"v1alpha1","metadata":{"objective":"min"}}`,
			wantErr: false,
			check: func(t *testing.T, po *PlannerOutput) {
				if len(po.Chains) != 0 {
					t.Errorf("Chains count = %d, want 0", len(po.Chains))
				}
				if len(po.Knights) != 0 {
					t.Errorf("Knights count = %d, want 0", len(po.Knights))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.parsePlannerOutput(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePlannerOutput() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// ─── sanitizeStepName ───────────────────────────────────────────────────────

func TestSanitizeStepName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no hyphens", "step_one", "step_one"},
		{"single hyphen", "step-one", "step_one"},
		{"multiple hyphens", "my-step-name", "my_step_name"},
		{"all hyphens", "---", "___"},
		{"empty", "", ""},
		{"mixed", "a-b_c-d", "a_b_c_d"},
		{"no change needed", "already_clean", "already_clean"},
		{"leading hyphen", "-start", "_start"},
		{"trailing hyphen", "end-", "end_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeStepName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeStepName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── natsPrefix ─────────────────────────────────────────────────────────────

func TestNatsPrefix(t *testing.T) {
	tests := []struct {
		name    string
		mission *aiv1alpha1.Mission
		want    string
	}{
		{
			name: "custom prefix set",
			mission: &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "my-mission"},
				Spec: aiv1alpha1.MissionSpec{
					NATSPrefix: "custom-prefix",
				},
			},
			want: "custom-prefix",
		},
		{
			name: "no prefix - uses mission name",
			mission: &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "test-mission"},
				Spec:       aiv1alpha1.MissionSpec{},
			},
			want: "mission-test-mission",
		},
		{
			name: "empty prefix - uses mission name",
			mission: &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				Spec: aiv1alpha1.MissionSpec{
					NATSPrefix: "",
				},
			},
			want: "mission-foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := natsPrefix(tt.mission)
			if got != tt.want {
				t.Errorf("natsPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ─── sanitizeLabelValue ─────────────────────────────────────────────────────

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple alpha", "researcher", "researcher"},
		{"with spaces", "security researcher", "security-researcher"},
		{"special chars", "role@#$name", "rolename"},
		{"leading non-alphanum", "---start", "start"},
		{"trailing non-alphanum", "end---", "end"},
		{"both ends non-alphanum", "..middle..", "middle"},
		{"empty", "", ""},
		{"all invalid", "@#$%", ""},
		{"with dots and dashes", "a.b-c_d", "a.b-c_d"},
		{
			name:  "truncate to 63 chars",
			input: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01234567890",
			want:  "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0",
		},
		{"uppercase preserved", "MyRole", "MyRole"},
		{"numbers only", "123", "123"},
		{"single char", "a", "a"},
		{
			name:  "spaces at ends after conversion",
			input: " hello world ",
			want:  "hello-world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLabelValue(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeLabelValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ─── isAlphanumeric ─────────────────────────────────────────────────────────

func TestIsAlphanumeric(t *testing.T) {
	tests := []struct {
		name  string
		input byte
		want  bool
	}{
		{"lowercase a", 'a', true},
		{"lowercase z", 'z', true},
		{"uppercase A", 'A', true},
		{"uppercase Z", 'Z', true},
		{"digit 0", '0', true},
		{"digit 9", '9', true},
		{"hyphen", '-', false},
		{"underscore", '_', false},
		{"dot", '.', false},
		{"space", ' ', false},
		{"at sign", '@', false},
		{"exclamation", '!', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAlphanumeric(tt.input)
			if got != tt.want {
				t.Errorf("isAlphanumeric(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ─── buildMissionNetworkPolicy ──────────────────────────────────────────────

func TestBuildMissionNetworkPolicy(t *testing.T) {
	assembler := &KnightAssembler{}

	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mission",
			Namespace: "ai",
			UID:       "test-uid-123",
		},
	}

	policyName := "mission-test-mission-isolation"
	policy := assembler.buildMissionNetworkPolicy(mission, policyName)

	t.Run("metadata", func(t *testing.T) {
		if policy.Name != policyName {
			t.Errorf("Name = %q, want %q", policy.Name, policyName)
		}
		if policy.Namespace != "ai" {
			t.Errorf("Namespace = %q, want ai", policy.Namespace)
		}
		if policy.Labels[aiv1alpha1.LabelMission] != "test-mission" {
			t.Errorf("Mission label = %q, want test-mission", policy.Labels[aiv1alpha1.LabelMission])
		}
		if policy.Labels[aiv1alpha1.LabelEphemeral] != "true" {
			t.Errorf("Ephemeral label = %q, want true", policy.Labels[aiv1alpha1.LabelEphemeral])
		}
	})

	t.Run("owner reference", func(t *testing.T) {
		if len(policy.OwnerReferences) != 1 {
			t.Fatalf("OwnerReferences count = %d, want 1", len(policy.OwnerReferences))
		}
		ref := policy.OwnerReferences[0]
		if ref.Name != "test-mission" {
			t.Errorf("OwnerRef Name = %q, want test-mission", ref.Name)
		}
		if ref.Kind != "Mission" {
			t.Errorf("OwnerRef Kind = %q, want Mission", ref.Kind)
		}
	})

	t.Run("pod selector targets mission pods", func(t *testing.T) {
		sel := policy.Spec.PodSelector
		if sel.MatchLabels[aiv1alpha1.LabelMission] != "test-mission" {
			t.Errorf("PodSelector mission label = %q, want test-mission",
				sel.MatchLabels[aiv1alpha1.LabelMission])
		}
	})

	t.Run("policy type is egress only", func(t *testing.T) {
		if len(policy.Spec.PolicyTypes) != 1 {
			t.Fatalf("PolicyTypes count = %d, want 1", len(policy.Spec.PolicyTypes))
		}
		if policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
			t.Errorf("PolicyType = %v, want Egress", policy.Spec.PolicyTypes[0])
		}
	})

	t.Run("has three egress rules", func(t *testing.T) {
		if len(policy.Spec.Egress) != 3 {
			t.Fatalf("Egress rules count = %d, want 3 (NATS, DNS, HTTPS)", len(policy.Spec.Egress))
		}
	})

	t.Run("NATS egress rule port 4222", func(t *testing.T) {
		natsRule := policy.Spec.Egress[0]
		if len(natsRule.Ports) != 1 {
			t.Fatalf("NATS rule ports = %d, want 1", len(natsRule.Ports))
		}
		if natsRule.Ports[0].Port.IntValue() != 4222 {
			t.Errorf("NATS port = %d, want 4222", natsRule.Ports[0].Port.IntValue())
		}
	})

	t.Run("DNS egress rule ports 53 UDP and TCP", func(t *testing.T) {
		dnsRule := policy.Spec.Egress[1]
		if len(dnsRule.Ports) != 2 {
			t.Fatalf("DNS rule ports = %d, want 2", len(dnsRule.Ports))
		}
	})

	t.Run("HTTPS egress rule port 443", func(t *testing.T) {
		httpsRule := policy.Spec.Egress[2]
		if len(httpsRule.Ports) != 1 {
			t.Fatalf("HTTPS rule ports = %d, want 1", len(httpsRule.Ports))
		}
		if httpsRule.Ports[0].Port.IntValue() != 443 {
			t.Errorf("HTTPS port = %d, want 443", httpsRule.Ports[0].Port.IntValue())
		}
		// HTTPS rule has no peer restrictions (allows any destination)
		if len(httpsRule.To) != 0 {
			t.Errorf("HTTPS rule To peers = %d, want 0 (unrestricted)", len(httpsRule.To))
		}
	})
}
