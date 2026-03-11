package controller

import (
	"testing"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

func TestValidateTemplates(t *testing.T) {
	r := &ChainReconciler{}

	tests := []struct {
		name    string
		chain   *aiv1alpha1.Chain
		wantErr bool
	}{
		{
			name: "valid template with .Steps.x.Output",
			chain: &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "research", Task: "do research"},
						{Name: "compose", Task: "Use this: {{ .Steps.research.Output }}"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "no templates at all",
			chain: &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "simple", Task: "just do it"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid template syntax - missing dot prefix",
			chain: &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "research", Task: "do research"},
						{Name: "compose", Task: "Use: {{ steps.research.output }}"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid template syntax - unclosed braces",
			chain: &aiv1alpha1.Chain{
				Spec: aiv1alpha1.ChainSpec{
					Steps: []aiv1alpha1.ChainStep{
						{Name: "broken", Task: "Use: {{ .Steps.x.Output"},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.validateTemplates(tt.chain)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTemplates() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
