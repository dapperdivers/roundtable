package util

import (
	"testing"
)

func TestValidateDAG(t *testing.T) {
	tests := []struct {
		name      string
		nodes     []DAGNode
		wantError bool
		errorMsg  string
	}{
		{
			name:      "empty DAG is valid",
			nodes:     []DAGNode{},
			wantError: false,
		},
		{
			name: "single node with no dependencies",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{}},
			},
			wantError: false,
		},
		{
			name: "valid linear dependency chain",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{}},
				{Name: "step2", DependsOn: []string{"step1"}},
				{Name: "step3", DependsOn: []string{"step2"}},
			},
			wantError: false,
		},
		{
			name: "valid DAG with parallel branches",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{}},
				{Name: "step2", DependsOn: []string{"step1"}},
				{Name: "step3", DependsOn: []string{"step1"}},
				{Name: "step4", DependsOn: []string{"step2", "step3"}},
			},
			wantError: false,
		},
		{
			name: "valid complex DAG",
			nodes: []DAGNode{
				{Name: "setup", DependsOn: []string{}},
				{Name: "build", DependsOn: []string{"setup"}},
				{Name: "test", DependsOn: []string{"build"}},
				{Name: "deploy", DependsOn: []string{"test"}},
				{Name: "notify", DependsOn: []string{"deploy"}},
			},
			wantError: false,
		},
		{
			name: "invalid: simple cycle",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{"step2"}},
				{Name: "step2", DependsOn: []string{"step1"}},
			},
			wantError: true,
			errorMsg:  "circular dependency",
		},
		{
			name: "invalid: self-dependency",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{"step1"}},
			},
			wantError: true,
			errorMsg:  "circular dependency",
		},
		{
			name: "invalid: cycle in longer chain",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{}},
				{Name: "step2", DependsOn: []string{"step1"}},
				{Name: "step3", DependsOn: []string{"step2"}},
				{Name: "step4", DependsOn: []string{"step3"}},
				{Name: "step5", DependsOn: []string{"step4", "step2"}}, // Valid so far
				{Name: "step2", DependsOn: []string{"step1", "step5"}}, // Creates cycle
			},
			wantError: true,
			errorMsg:  "circular dependency",
		},
		{
			name: "invalid: unknown dependency",
			nodes: []DAGNode{
				{Name: "step1", DependsOn: []string{}},
				{Name: "step2", DependsOn: []string{"step1", "nonexistent"}},
			},
			wantError: true,
			errorMsg:  "unknown node",
		},
		{
			name: "valid: multiple independent roots",
			nodes: []DAGNode{
				{Name: "root1", DependsOn: []string{}},
				{Name: "root2", DependsOn: []string{}},
				{Name: "merge", DependsOn: []string{"root1", "root2"}},
			},
			wantError: false,
		},
		{
			name: "valid: diamond dependency",
			nodes: []DAGNode{
				{Name: "start", DependsOn: []string{}},
				{Name: "left", DependsOn: []string{"start"}},
				{Name: "right", DependsOn: []string{"start"}},
				{Name: "end", DependsOn: []string{"left", "right"}},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDAG(tt.nodes)
			if tt.wantError {
				if err == nil {
					t.Errorf("ValidateDAG() expected error but got nil")
					return
				}
				if tt.errorMsg != "" {
					if !contains(err.Error(), tt.errorMsg) {
						t.Errorf("ValidateDAG() error = %v, want error containing %q", err, tt.errorMsg)
					}
				}
			} else {
				if err != nil {
					t.Errorf("ValidateDAG() unexpected error = %v", err)
				}
			}
		})
	}
}

// Helper function for substring check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || 
		(len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr))))
}
