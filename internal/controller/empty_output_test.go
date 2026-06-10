package controller

import "testing"

func TestIsEmptyStepOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "empty string", output: "", want: true},
		{name: "whitespace only", output: "  \n\t ", want: true},
		{name: "no-output sentinel", output: "[No output from agent]", want: true},
		{name: "sentinel with surrounding whitespace", output: "\n[No output from agent]\n", want: true},
		{name: "real output", output: "The briefing is ready.", want: false},
		{name: "sentinel embedded in real output", output: "prefix [No output from agent] suffix", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEmptyStepOutput(tt.output); got != tt.want {
				t.Errorf("isEmptyStepOutput(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}
