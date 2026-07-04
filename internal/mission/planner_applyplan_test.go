package mission

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

// TestApplyPlanKnightRefPrefixing covers the fix for the InvalidKnightRef bug:
// ephemeral knights are created as "<mission>-<knight>", so chain steps that
// reference them by plan name must be prefixed the same way. Non-ephemeral
// (recruited) knights keep their bare names.
func TestApplyPlanKnightRefPrefixing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add client-go scheme: %v", err)
	}
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add roundtable scheme: %v", err)
	}

	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mission",
			Namespace: "default",
		},
		Spec: aiv1alpha1.MissionSpec{
			Objective:     "test",
			RoundTableRef: "personal",
		},
	}

	plan := &PlannerOutput{
		PlanVersion: "v1alpha1",
		Knights: []PlannerKnight{
			{Name: "haiku-writer", Ephemeral: true, TemplateRef: "base"},
			{Name: "standing-knight", Ephemeral: false},
		},
		Chains: []PlannerChain{
			{
				Name: "haiku-creation",
				Steps: []aiv1alpha1.ChainStep{
					{Name: "write", KnightRef: "haiku-writer", Task: "write it"},
					{Name: "review", KnightRef: "standing-knight", Task: "review it", DependsOn: []string{"write"}},
				},
			},
		},
	}

	p := &Planner{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	if err := p.applyPlan(context.Background(), mission, plan); err != nil {
		t.Fatalf("applyPlan failed: %v", err)
	}

	if len(mission.Spec.GeneratedChains) != 1 {
		t.Fatalf("expected 1 generated chain, got %d", len(mission.Spec.GeneratedChains))
	}
	steps := mission.Spec.GeneratedChains[0].Steps
	if got, want := steps[0].KnightRef, "test-mission-haiku-writer"; got != want {
		t.Errorf("ephemeral knightRef = %q, want %q (mission-prefixed)", got, want)
	}
	if got, want := steps[1].KnightRef, "standing-knight"; got != want {
		t.Errorf("recruited knightRef = %q, want %q (unprefixed)", got, want)
	}

	// The created Chain CR must carry the same prefixed refs — this is what
	// the chain controller resolves against real Knight CRs.
	chain := &aiv1alpha1.Chain{}
	chainKey := types.NamespacedName{Name: "mission-test-mission-haiku-creation", Namespace: "default"}
	if err := p.Client.Get(context.Background(), chainKey, chain); err != nil {
		t.Fatalf("expected chain CR to be created: %v", err)
	}
	if got, want := chain.Spec.Steps[0].KnightRef, "test-mission-haiku-writer"; got != want {
		t.Errorf("chain CR ephemeral knightRef = %q, want %q", got, want)
	}
}
