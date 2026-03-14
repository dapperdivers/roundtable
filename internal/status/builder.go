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
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
)

// MissionUpdate provides fluent status updates for Mission resources.
// Automatically sets ObservedGeneration on every update to prevent stale status.
type MissionUpdate struct {
	mission *aiv1alpha1.Mission
}

// ForMission creates a new MissionUpdate builder for the given mission.
func ForMission(m *aiv1alpha1.Mission) *MissionUpdate {
	return &MissionUpdate{mission: m}
}

// Phase sets the mission phase and updates ObservedGeneration.
func (u *MissionUpdate) Phase(p aiv1alpha1.MissionPhase) *MissionUpdate {
	u.mission.Status.Phase = p
	u.mission.Status.ObservedGeneration = u.mission.Generation
	return u
}

// Complete marks the mission as complete with a result and phase.
func (u *MissionUpdate) Complete(result string, phase aiv1alpha1.MissionPhase) *MissionUpdate {
	now := metav1.Now()
	u.mission.Status.CompletedAt = &now
	u.mission.Status.Result = result
	return u.Phase(phase)
}

// Succeeded marks the mission as successfully completed.
func (u *MissionUpdate) Succeeded(result string) *MissionUpdate {
	return u.Complete(result, aiv1alpha1.MissionPhaseSucceeded)
}

// Failed marks the mission as failed.
func (u *MissionUpdate) Failed(result string) *MissionUpdate {
	return u.Complete(result, aiv1alpha1.MissionPhaseFailed)
}

// Started sets the mission start time and phase.
func (u *MissionUpdate) Started(phase aiv1alpha1.MissionPhase) *MissionUpdate {
	now := metav1.Now()
	u.mission.Status.StartedAt = &now
	return u.Phase(phase)
}

// Result sets the result message without changing completion state.
func (u *MissionUpdate) Result(result string) *MissionUpdate {
	u.mission.Status.Result = result
	u.mission.Status.ObservedGeneration = u.mission.Generation
	return u
}

// Condition adds or updates a status condition.
func (u *MissionUpdate) Condition(typ, reason, msg string, status metav1.ConditionStatus) *MissionUpdate {
	meta.SetStatusCondition(&u.mission.Status.Conditions, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: u.mission.Generation,
	})
	return u
}

// Apply commits the status update to the API server.
func (u *MissionUpdate) Apply(ctx context.Context, c client.Client) error {
	return c.Status().Update(ctx, u.mission)
}

// ChainUpdate provides fluent status updates for Chain resources.
type ChainUpdate struct {
	chain *aiv1alpha1.Chain
}

// ForChain creates a new ChainUpdate builder for the given chain.
func ForChain(c *aiv1alpha1.Chain) *ChainUpdate {
	return &ChainUpdate{chain: c}
}

// Phase sets the chain phase and updates ObservedGeneration.
func (u *ChainUpdate) Phase(p aiv1alpha1.ChainPhase) *ChainUpdate {
	u.chain.Status.Phase = p
	u.chain.Status.ObservedGeneration = u.chain.Generation
	return u
}

// Started sets the chain start time and phase.
func (u *ChainUpdate) Started(phase aiv1alpha1.ChainPhase) *ChainUpdate {
	now := metav1.Now()
	u.chain.Status.StartedAt = &now
	return u.Phase(phase)
}

// Completed marks the chain as complete.
func (u *ChainUpdate) Completed(phase aiv1alpha1.ChainPhase) *ChainUpdate {
	now := metav1.Now()
	u.chain.Status.CompletedAt = &now
	return u.Phase(phase)
}

// Failed marks the chain as failed.
func (u *ChainUpdate) Failed() *ChainUpdate {
	return u.Completed(aiv1alpha1.ChainPhaseFailed)
}

// Succeeded marks the chain as succeeded.
func (u *ChainUpdate) Succeeded() *ChainUpdate {
	return u.Completed(aiv1alpha1.ChainPhaseSucceeded)
}

// Condition adds or updates a status condition.
func (u *ChainUpdate) Condition(typ, reason, msg string, status metav1.ConditionStatus) *ChainUpdate {
	meta.SetStatusCondition(&u.chain.Status.Conditions, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: u.chain.Generation,
	})
	return u
}

// Apply commits the status update to the API server.
func (u *ChainUpdate) Apply(ctx context.Context, c client.Client) error {
	return c.Status().Update(ctx, u.chain)
}

// KnightUpdate provides fluent status updates for Knight resources.
type KnightUpdate struct {
	knight *aiv1alpha1.Knight
}

// ForKnight creates a new KnightUpdate builder for the given knight.
func ForKnight(k *aiv1alpha1.Knight) *KnightUpdate {
	return &KnightUpdate{knight: k}
}

// Phase sets the knight phase and updates ObservedGeneration.
func (u *KnightUpdate) Phase(p aiv1alpha1.KnightPhase) *KnightUpdate {
	u.knight.Status.Phase = p
	u.knight.Status.ObservedGeneration = u.knight.Generation
	return u
}

// Ready marks the knight as ready.
func (u *KnightUpdate) Ready(ready bool) *KnightUpdate {
	u.knight.Status.Ready = ready
	u.knight.Status.ObservedGeneration = u.knight.Generation
	return u
}

// Condition adds or updates a status condition.
func (u *KnightUpdate) Condition(typ, reason, msg string, status metav1.ConditionStatus) *KnightUpdate {
	meta.SetStatusCondition(&u.knight.Status.Conditions, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: u.knight.Generation,
	})
	return u
}

// Apply commits the status update to the API server.
func (u *KnightUpdate) Apply(ctx context.Context, c client.Client) error {
	return c.Status().Update(ctx, u.knight)
}

// RoundTableUpdate provides fluent status updates for RoundTable resources.
type RoundTableUpdate struct {
	roundTable *aiv1alpha1.RoundTable
}

// ForRoundTable creates a new RoundTableUpdate builder for the given round table.
func ForRoundTable(rt *aiv1alpha1.RoundTable) *RoundTableUpdate {
	return &RoundTableUpdate{roundTable: rt}
}

// Phase sets the round table phase and updates ObservedGeneration.
func (u *RoundTableUpdate) Phase(p aiv1alpha1.RoundTablePhase) *RoundTableUpdate {
	u.roundTable.Status.Phase = p
	u.roundTable.Status.ObservedGeneration = u.roundTable.Generation
	return u
}

// Condition adds or updates a status condition.
func (u *RoundTableUpdate) Condition(typ, reason, msg string, status metav1.ConditionStatus) *RoundTableUpdate {
	meta.SetStatusCondition(&u.roundTable.Status.Conditions, metav1.Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: u.roundTable.Generation,
	})
	return u
}

// Apply commits the status update to the API server.
func (u *RoundTableUpdate) Apply(ctx context.Context, c client.Client) error {
	return c.Status().Update(ctx, u.roundTable)
}
