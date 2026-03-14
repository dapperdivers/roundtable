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

package status

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

func TestMissionUpdate_Phase(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 5,
		},
	}

	ForMission(mission).Phase(aiv1alpha1.MissionPhaseActive)

	if mission.Status.Phase != aiv1alpha1.MissionPhaseActive {
		t.Errorf("Expected phase Active, got %s", mission.Status.Phase)
	}

	if mission.Status.ObservedGeneration != 5 {
		t.Errorf("Expected ObservedGeneration 5, got %d", mission.Status.ObservedGeneration)
	}
}

func TestMissionUpdate_Succeeded(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 3,
		},
	}

	ForMission(mission).Succeeded("All tasks completed successfully")

	if mission.Status.Phase != aiv1alpha1.MissionPhaseSucceeded {
		t.Errorf("Expected phase Succeeded, got %s", mission.Status.Phase)
	}

	if mission.Status.Result != "All tasks completed successfully" {
		t.Errorf("Expected result message, got %s", mission.Status.Result)
	}

	if mission.Status.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}

	if mission.Status.ObservedGeneration != 3 {
		t.Errorf("Expected ObservedGeneration 3, got %d", mission.Status.ObservedGeneration)
	}
}

func TestMissionUpdate_Failed(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 7,
		},
	}

	ForMission(mission).Failed("Task execution failed")

	if mission.Status.Phase != aiv1alpha1.MissionPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", mission.Status.Phase)
	}

	if mission.Status.Result != "Task execution failed" {
		t.Errorf("Expected result message, got %s", mission.Status.Result)
	}

	if mission.Status.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}

	if mission.Status.ObservedGeneration != 7 {
		t.Errorf("Expected ObservedGeneration 7, got %d", mission.Status.ObservedGeneration)
	}
}

func TestMissionUpdate_Started(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 2,
		},
	}

	ForMission(mission).Started(aiv1alpha1.MissionPhaseActive)

	if mission.Status.Phase != aiv1alpha1.MissionPhaseActive {
		t.Errorf("Expected phase Active, got %s", mission.Status.Phase)
	}

	if mission.Status.StartedAt == nil {
		t.Error("Expected StartedAt to be set")
	}

	if mission.Status.ObservedGeneration != 2 {
		t.Errorf("Expected ObservedGeneration 2, got %d", mission.Status.ObservedGeneration)
	}
}

func TestMissionUpdate_Condition(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 4,
		},
	}

	ForMission(mission).Condition("Ready", "ResourcesProvisioned", "All resources are ready", metav1.ConditionTrue)

	if len(mission.Status.Conditions) != 1 {
		t.Fatalf("Expected 1 condition, got %d", len(mission.Status.Conditions))
	}

	cond := mission.Status.Conditions[0]
	if cond.Type != "Ready" {
		t.Errorf("Expected condition type Ready, got %s", cond.Type)
	}

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Expected condition status True, got %s", cond.Status)
	}

	if cond.Reason != "ResourcesProvisioned" {
		t.Errorf("Expected condition reason ResourcesProvisioned, got %s", cond.Reason)
	}

	if cond.ObservedGeneration != 4 {
		t.Errorf("Expected condition ObservedGeneration 4, got %d", cond.ObservedGeneration)
	}
}

func TestMissionUpdate_Chaining(t *testing.T) {
	mission := &aiv1alpha1.Mission{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 10,
		},
	}

	ForMission(mission).
		Started(aiv1alpha1.MissionPhaseActive).
		Condition("Progressing", "KnightsDeployed", "Knights are being deployed", metav1.ConditionTrue).
		Result("In progress")

	if mission.Status.Phase != aiv1alpha1.MissionPhaseActive {
		t.Errorf("Expected phase Active, got %s", mission.Status.Phase)
	}

	if mission.Status.StartedAt == nil {
		t.Error("Expected StartedAt to be set")
	}

	if mission.Status.Result != "In progress" {
		t.Errorf("Expected result 'In progress', got %s", mission.Status.Result)
	}

	if len(mission.Status.Conditions) != 1 {
		t.Errorf("Expected 1 condition, got %d", len(mission.Status.Conditions))
	}

	if mission.Status.ObservedGeneration != 10 {
		t.Errorf("Expected ObservedGeneration 10, got %d", mission.Status.ObservedGeneration)
	}
}

func TestChainUpdate_Phase(t *testing.T) {
	chain := &aiv1alpha1.Chain{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 3,
		},
	}

	ForChain(chain).Phase(aiv1alpha1.ChainPhaseRunning)

	if chain.Status.Phase != aiv1alpha1.ChainPhaseRunning {
		t.Errorf("Expected phase Running, got %s", chain.Status.Phase)
	}

	if chain.Status.ObservedGeneration != 3 {
		t.Errorf("Expected ObservedGeneration 3, got %d", chain.Status.ObservedGeneration)
	}
}

func TestChainUpdate_Succeeded(t *testing.T) {
	chain := &aiv1alpha1.Chain{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 5,
		},
	}

	ForChain(chain).Succeeded()

	if chain.Status.Phase != aiv1alpha1.ChainPhaseSucceeded {
		t.Errorf("Expected phase Succeeded, got %s", chain.Status.Phase)
	}

	if chain.Status.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}

	if chain.Status.ObservedGeneration != 5 {
		t.Errorf("Expected ObservedGeneration 5, got %d", chain.Status.ObservedGeneration)
	}
}

func TestChainUpdate_Failed(t *testing.T) {
	chain := &aiv1alpha1.Chain{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 2,
		},
	}

	ForChain(chain).Failed()

	if chain.Status.Phase != aiv1alpha1.ChainPhaseFailed {
		t.Errorf("Expected phase Failed, got %s", chain.Status.Phase)
	}

	if chain.Status.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}

	if chain.Status.ObservedGeneration != 2 {
		t.Errorf("Expected ObservedGeneration 2, got %d", chain.Status.ObservedGeneration)
	}
}

func TestKnightUpdate_Phase(t *testing.T) {
	knight := &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 1,
		},
	}

	ForKnight(knight).Phase(aiv1alpha1.KnightPhaseReady)

	if knight.Status.Phase != aiv1alpha1.KnightPhaseReady {
		t.Errorf("Expected phase Ready, got %s", knight.Status.Phase)
	}

	if knight.Status.ObservedGeneration != 1 {
		t.Errorf("Expected ObservedGeneration 1, got %d", knight.Status.ObservedGeneration)
	}
}

func TestKnightUpdate_Ready(t *testing.T) {
	knight := &aiv1alpha1.Knight{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 4,
		},
	}

	ForKnight(knight).Ready(true)

	if !knight.Status.Ready {
		t.Error("Expected Ready to be true")
	}

	if knight.Status.ObservedGeneration != 4 {
		t.Errorf("Expected ObservedGeneration 4, got %d", knight.Status.ObservedGeneration)
	}
}

func TestRoundTableUpdate_Phase(t *testing.T) {
	rt := &aiv1alpha1.RoundTable{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 6,
		},
	}

	ForRoundTable(rt).Phase(aiv1alpha1.RoundTablePhaseReady)

	if rt.Status.Phase != aiv1alpha1.RoundTablePhaseReady {
		t.Errorf("Expected phase Ready, got %s", rt.Status.Phase)
	}

	if rt.Status.ObservedGeneration != 6 {
		t.Errorf("Expected ObservedGeneration 6, got %d", rt.Status.ObservedGeneration)
	}
}

func TestRoundTableUpdate_Condition(t *testing.T) {
	rt := &aiv1alpha1.RoundTable{
		ObjectMeta: metav1.ObjectMeta{
			Generation: 3,
		},
	}

	ForRoundTable(rt).Condition("StreamsReady", "StreamsCreated", "All NATS streams created", metav1.ConditionTrue)

	if len(rt.Status.Conditions) != 1 {
		t.Fatalf("Expected 1 condition, got %d", len(rt.Status.Conditions))
	}

	cond := rt.Status.Conditions[0]
	if cond.Type != "StreamsReady" {
		t.Errorf("Expected condition type StreamsReady, got %s", cond.Type)
	}

	if cond.ObservedGeneration != 3 {
		t.Errorf("Expected condition ObservedGeneration 3, got %d", cond.ObservedGeneration)
	}
}

func TestObservedGenerationNeverForgotten(t *testing.T) {
	// This test ensures that ObservedGeneration is ALWAYS set
	// This is the main value proposition of the builder pattern

	t.Run("Mission Phase", func(t *testing.T) {
		m := &aiv1alpha1.Mission{ObjectMeta: metav1.ObjectMeta{Generation: 99}}
		ForMission(m).Phase(aiv1alpha1.MissionPhaseActive)
		if m.Status.ObservedGeneration != 99 {
			t.Error("ObservedGeneration not set by Phase()")
		}
	})

	t.Run("Mission Result", func(t *testing.T) {
		m := &aiv1alpha1.Mission{ObjectMeta: metav1.ObjectMeta{Generation: 88}}
		ForMission(m).Result("test")
		if m.Status.ObservedGeneration != 88 {
			t.Error("ObservedGeneration not set by Result()")
		}
	})

	t.Run("Chain Phase", func(t *testing.T) {
		c := &aiv1alpha1.Chain{ObjectMeta: metav1.ObjectMeta{Generation: 77}}
		ForChain(c).Phase(aiv1alpha1.ChainPhaseRunning)
		if c.Status.ObservedGeneration != 77 {
			t.Error("ObservedGeneration not set by Phase()")
		}
	})

	t.Run("Knight Ready", func(t *testing.T) {
		k := &aiv1alpha1.Knight{ObjectMeta: metav1.ObjectMeta{Generation: 66}}
		ForKnight(k).Ready(true)
		if k.Status.ObservedGeneration != 66 {
			t.Error("ObservedGeneration not set by Ready()")
		}
	})

	t.Run("RoundTable Phase", func(t *testing.T) {
		rt := &aiv1alpha1.RoundTable{ObjectMeta: metav1.ObjectMeta{Generation: 55}}
		ForRoundTable(rt).Phase(aiv1alpha1.RoundTablePhaseReady)
		if rt.Status.ObservedGeneration != 55 {
			t.Error("ObservedGeneration not set by Phase()")
		}
	})
}
