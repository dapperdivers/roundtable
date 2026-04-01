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

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

// ── computePhase ──────────────────────────────────────────────────────────────

func TestComputePhase(t *testing.T) {
	r := &RoundTableReconciler{}

	tests := []struct {
		name       string
		rt         *aiv1alpha1.RoundTable
		readyCount int32
		total      int32
		totalCost  float64
		want       aiv1alpha1.RoundTablePhase
	}{
		{
			name:       "Provisioning when total is 0",
			rt:         &aiv1alpha1.RoundTable{},
			readyCount: 0, total: 0, totalCost: 0,
			want: aiv1alpha1.RoundTablePhaseProvisioning,
		},
		{
			name:       "Ready when readyCount equals total",
			rt:         &aiv1alpha1.RoundTable{},
			readyCount: 3, total: 3, totalCost: 0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
		{
			name:       "Degraded when readyCount < total",
			rt:         &aiv1alpha1.RoundTable{},
			readyCount: 1, total: 3, totalCost: 0,
			want: aiv1alpha1.RoundTablePhaseDegraded,
		},
		{
			name:       "Degraded when readyCount is 0 but total > 0",
			rt:         &aiv1alpha1.RoundTable{},
			readyCount: 0, total: 3, totalCost: 0,
			want: aiv1alpha1.RoundTablePhaseDegraded,
		},
		{
			name: "OverBudget when cost exceeds budget",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "100.0"},
				},
			},
			readyCount: 3, total: 3, totalCost: 150.0,
			want: aiv1alpha1.RoundTablePhaseOverBudget,
		},
		{
			name: "Ready when cost within budget",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "100.0"},
				},
			},
			readyCount: 3, total: 3, totalCost: 50.0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
		{
			name: "OverBudget takes priority over Ready",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "100.0"},
				},
			},
			readyCount: 3, total: 3, totalCost: 100.01,
			want: aiv1alpha1.RoundTablePhaseOverBudget,
		},
		{
			name: "OverBudget takes priority over Degraded",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "50"},
				},
			},
			readyCount: 1, total: 3, totalCost: 999.0,
			want: aiv1alpha1.RoundTablePhaseOverBudget,
		},
		{
			name: "budget '0' is ignored",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "0"},
				},
			},
			readyCount: 3, total: 3, totalCost: 999.0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
		{
			name: "empty budget string is ignored",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: ""},
				},
			},
			readyCount: 3, total: 3, totalCost: 999.0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
		{
			name:       "nil policies is ignored",
			rt:         &aiv1alpha1.RoundTable{},
			readyCount: 3, total: 3, totalCost: 999.0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
		{
			name: "unparseable budget falls through to normal logic",
			rt: &aiv1alpha1.RoundTable{
				Spec: aiv1alpha1.RoundTableSpec{
					Policies: &aiv1alpha1.RoundTablePolicies{CostBudgetUSD: "not-a-number"},
				},
			},
			readyCount: 3, total: 3, totalCost: 999.0,
			want: aiv1alpha1.RoundTablePhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.computePhase(tt.rt, tt.readyCount, tt.total, tt.totalCost)
			if got != tt.want {
				t.Errorf("computePhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── initKnightStatuses ───────────────────────────────────────────────────────

func TestInitKnightStatuses(t *testing.T) {
	r := &MissionReconciler{}

	t.Run("empty knights list", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{
			Spec: aiv1alpha1.MissionSpec{Knights: []aiv1alpha1.MissionKnight{}},
		}
		r.initKnightStatuses(mission)
		if len(mission.Status.KnightStatuses) != 0 {
			t.Errorf("expected 0 statuses, got %d", len(mission.Status.KnightStatuses))
		}
	})

	t.Run("creates one status per knight with correct fields", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{
			Spec: aiv1alpha1.MissionSpec{
				Knights: []aiv1alpha1.MissionKnight{
					{Name: "lancelot"},
					{Name: "gawain", Ephemeral: true},
					{Name: "percival"},
				},
			},
		}
		r.initKnightStatuses(mission)

		if len(mission.Status.KnightStatuses) != 3 {
			t.Fatalf("expected 3 statuses, got %d", len(mission.Status.KnightStatuses))
		}
		if mission.Status.KnightStatuses[0].Name != "lancelot" {
			t.Errorf("status[0].Name = %q, want lancelot", mission.Status.KnightStatuses[0].Name)
		}
		if mission.Status.KnightStatuses[0].Ephemeral {
			t.Error("status[0].Ephemeral should be false")
		}
		if mission.Status.KnightStatuses[1].Name != "gawain" {
			t.Errorf("status[1].Name = %q, want gawain", mission.Status.KnightStatuses[1].Name)
		}
		if !mission.Status.KnightStatuses[1].Ephemeral {
			t.Error("status[1].Ephemeral should be true")
		}
		if mission.Status.KnightStatuses[2].Name != "percival" {
			t.Errorf("status[2].Name = %q, want percival", mission.Status.KnightStatuses[2].Name)
		}
	})

	t.Run("overwrites existing statuses", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{
			Spec: aiv1alpha1.MissionSpec{
				Knights: []aiv1alpha1.MissionKnight{{Name: "new-knight"}},
			},
			Status: aiv1alpha1.MissionStatus{
				KnightStatuses: []aiv1alpha1.MissionKnightStatus{{Name: "old-knight"}},
			},
		}
		r.initKnightStatuses(mission)
		if len(mission.Status.KnightStatuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(mission.Status.KnightStatuses))
		}
		if mission.Status.KnightStatuses[0].Name != "new-knight" {
			t.Errorf("status[0].Name = %q, want new-knight", mission.Status.KnightStatuses[0].Name)
		}
	})
}

// ── natsPrefix ───────────────────────────────────────────────────────────────

func TestNatsPrefix(t *testing.T) {
	tests := []struct {
		name    string
		mission *aiv1alpha1.Mission
		want    string
	}{
		{
			name: "returns custom prefix when set",
			mission: &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "my-mission"},
				Spec:       aiv1alpha1.MissionSpec{NATSPrefix: "custom-prefix"},
			},
			want: "custom-prefix",
		},
		{
			name: "returns mission-<name> when prefix empty",
			mission: &aiv1alpha1.Mission{
				ObjectMeta: metav1.ObjectMeta{Name: "quest-alpha"},
			},
			want: "mission-quest-alpha",
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

// ── nextPhaseAfterProvisioning ───────────────────────────────────────────────

func TestNextPhaseAfterProvisioning(t *testing.T) {
	tests := []struct {
		name    string
		mission *aiv1alpha1.Mission
		want    aiv1alpha1.MissionPhase
	}{
		{
			name:    "returns Planning for meta missions",
			mission: &aiv1alpha1.Mission{Spec: aiv1alpha1.MissionSpec{MetaMission: true}},
			want:    aiv1alpha1.MissionPhasePlanning,
		},
		{
			name:    "returns Assembling for non-meta missions",
			mission: &aiv1alpha1.Mission{Spec: aiv1alpha1.MissionSpec{MetaMission: false}},
			want:    aiv1alpha1.MissionPhaseAssembling,
		},
		{
			name:    "returns Assembling for default (zero-value) mission",
			mission: &aiv1alpha1.Mission{},
			want:    aiv1alpha1.MissionPhaseAssembling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextPhaseAfterProvisioning(tt.mission)
			if got != tt.want {
				t.Errorf("nextPhaseAfterProvisioning() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── shouldDeleteResources ────────────────────────────────────────────────────

func TestShouldDeleteResources(t *testing.T) {
	r := &MissionReconciler{}

	tests := []struct {
		name   string
		policy string
		phase  aiv1alpha1.MissionPhase
		want   bool
	}{
		{"Delete always deletes", "Delete", aiv1alpha1.MissionPhaseSucceeded, true},
		{"Retain never deletes", "Retain", aiv1alpha1.MissionPhaseSucceeded, false},
		{"Retain never deletes on failure", "Retain", aiv1alpha1.MissionPhaseFailed, false},
		{"OnSuccess deletes on success", "OnSuccess", aiv1alpha1.MissionPhaseSucceeded, true},
		{"OnSuccess retains on failure", "OnSuccess", aiv1alpha1.MissionPhaseFailed, false},
		{"OnFailure deletes on failure", "OnFailure", aiv1alpha1.MissionPhaseFailed, true},
		{"OnFailure retains on success", "OnFailure", aiv1alpha1.MissionPhaseSucceeded, false},
		{"empty policy defaults to delete", "", aiv1alpha1.MissionPhaseSucceeded, true},
		{"unknown policy defaults to delete", "SomethingElse", aiv1alpha1.MissionPhaseFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mission := &aiv1alpha1.Mission{
				Spec:   aiv1alpha1.MissionSpec{CleanupPolicy: tt.policy},
				Status: aiv1alpha1.MissionStatus{Phase: tt.phase},
			}
			got := r.shouldDeleteResources(mission)
			if got != tt.want {
				t.Errorf("shouldDeleteResources(policy=%q, phase=%q) = %v, want %v",
					tt.policy, tt.phase, got, tt.want)
			}
		})
	}
}

// ── updateChainStatus ────────────────────────────────────────────────────────

func TestUpdateChainStatus(t *testing.T) {
	r := &MissionReconciler{}

	t.Run("adds new chain status to empty list", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{}
		r.updateChainStatus(mission, "my-chain", "my-chain-cr", aiv1alpha1.ChainPhaseRunning)

		if len(mission.Status.ChainStatuses) != 1 {
			t.Fatalf("expected 1 chain status, got %d", len(mission.Status.ChainStatuses))
		}
		cs := mission.Status.ChainStatuses[0]
		if cs.Name != "my-chain" {
			t.Errorf("Name = %q, want my-chain", cs.Name)
		}
		if cs.ChainCRName != "my-chain-cr" {
			t.Errorf("ChainCRName = %q, want my-chain-cr", cs.ChainCRName)
		}
		if cs.Phase != aiv1alpha1.ChainPhaseRunning {
			t.Errorf("Phase = %q, want Running", cs.Phase)
		}
	})

	t.Run("updates existing chain status by name", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{
			Status: aiv1alpha1.MissionStatus{
				ChainStatuses: []aiv1alpha1.MissionChainStatus{
					{Name: "chain-a", ChainCRName: "old-cr", Phase: aiv1alpha1.ChainPhaseIdle},
				},
			},
		}
		r.updateChainStatus(mission, "chain-a", "new-cr", aiv1alpha1.ChainPhaseSucceeded)

		if len(mission.Status.ChainStatuses) != 1 {
			t.Fatalf("expected 1 chain status, got %d", len(mission.Status.ChainStatuses))
		}
		if mission.Status.ChainStatuses[0].ChainCRName != "new-cr" {
			t.Errorf("ChainCRName = %q, want new-cr", mission.Status.ChainStatuses[0].ChainCRName)
		}
		if mission.Status.ChainStatuses[0].Phase != aiv1alpha1.ChainPhaseSucceeded {
			t.Errorf("Phase = %q, want Succeeded", mission.Status.ChainStatuses[0].Phase)
		}
	})

	t.Run("appends new entry without disturbing existing", func(t *testing.T) {
		mission := &aiv1alpha1.Mission{
			Status: aiv1alpha1.MissionStatus{
				ChainStatuses: []aiv1alpha1.MissionChainStatus{
					{Name: "chain-a", ChainCRName: "cr-a", Phase: aiv1alpha1.ChainPhaseRunning},
				},
			},
		}
		r.updateChainStatus(mission, "chain-b", "cr-b", aiv1alpha1.ChainPhaseFailed)

		if len(mission.Status.ChainStatuses) != 2 {
			t.Fatalf("expected 2 chain statuses, got %d", len(mission.Status.ChainStatuses))
		}
		// first entry unchanged
		if mission.Status.ChainStatuses[0].Name != "chain-a" || mission.Status.ChainStatuses[0].Phase != aiv1alpha1.ChainPhaseRunning {
			t.Error("existing chain-a entry was modified")
		}
		// second entry correct
		if mission.Status.ChainStatuses[1].Name != "chain-b" || mission.Status.ChainStatuses[1].Phase != aiv1alpha1.ChainPhaseFailed {
			t.Error("chain-b entry not appended correctly")
		}
	})
}
