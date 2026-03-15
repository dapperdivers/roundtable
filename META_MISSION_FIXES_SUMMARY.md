# Meta-Mission Assembler Bug Fixes

## Overview

Fixed three critical bugs in the meta-mission planner and assembler that prevented mission-generated knights and chains from being properly assembled and executed.

## Bugs Fixed

### Bug 1: GeneratedKnights vs Knights Field Mismatch in Assembler

**Location:** `internal/mission/assembler.go` (ReconcileAssembling)

**Problem:**
- The assembler's `ReconcileAssembling()` only iterated `mission.Spec.Knights`
- The planner writes recruited knights to `mission.Spec.GeneratedKnights` (planner.go line ~854)
- Non-ephemeral recruited knights were invisible to the assembler
- Caused infinite loop with log: `Waiting for knights to become ready total=0 notReady=0`

**Fix:**
- Merge `mission.Spec.GeneratedKnights` into the knight list being iterated:
  ```go
  allKnights := append(mission.Spec.Knights, mission.Spec.GeneratedKnights...)
  for _, mk := range allKnights {
  ```
- Updated knight count references to use `len(allKnights)` instead of `len(mission.Spec.Knights)`
- When all knights (including generated ones) are Ready, assembler now transitions to Briefing immediately

**Files Changed:**
- `internal/mission/assembler.go`: Lines ~75-85, ~195-205, ~215

---

### Bug 2: PlanApplied/completedAt Guard Fires on Failed Validations

**Location:** `internal/mission/planner.go` (ReconcilePlanning)

**Problem:**
- When plan validation failed (line ~200), code set `PlanningResult.CompletedAt` AND the error
- On next reconcile, guard at line ~99 (`if pr.CompletedAt != nil`) fired
- Skipped re-planning and transitioned to Assembling with 0 chains/0 knights
- Failed plans should NOT set CompletedAt — they should allow retry

**Fix:**
- Only set `PlanningResult.CompletedAt` on successful plan application
- On validation failure:
  - Set `PlanningResult.Error` without CompletedAt
  - Return with `RequeueAfter: 10 * time.Second` to allow retry
  - This keeps mission in Planning phase for another attempt

**Files Changed:**
- `internal/mission/planner.go`: Lines ~195-205

**Before:**
```go
if err := p.validatePlan(ctx, mission, plan); err != nil {
    log.Error(err, "Plan validation failed")
    pr.Error = fmt.Sprintf("plan validation failed: %v", err)
    pr.RawOutput = util.Truncate(output, 10000)
    now := metav1.Now()
    pr.CompletedAt = &now  // ❌ WRONG - prevents retry
    return ctrl.Result{}, p.Client.Status().Update(ctx, mission)
}
```

**After:**
```go
if err := p.validatePlan(ctx, mission, plan); err != nil {
    log.Error(err, "Plan validation failed")
    pr.Error = fmt.Sprintf("plan validation failed: %v", err)
    pr.RawOutput = util.Truncate(output, 10000)
    // Do NOT set pr.CompletedAt here - allow retry
    return ctrl.Result{RequeueAfter: 10 * time.Second}, p.Client.Status().Update(ctx, mission)
}
```

---

### Bug 3: Mission-Generated Chains Stuck in Idle

**Location:** `internal/controller/mission_controller.go` (reconcileBriefing)

**Problem:**
- After assembling completes and mission transitions to Running phase, generated Chain CRs remain in Idle
- Chain controller only triggers chains via cron schedule or manual intervention
- Mission-generated chains have no schedule and need to be started manually

**Fix:**
- When mission transitions from Briefing to Active, trigger generated chains to Running phase
- Added new helper method `triggerGeneratedChains()`:
  ```go
  func (r *MissionReconciler) triggerGeneratedChains(ctx context.Context, mission *aiv1alpha1.Mission) error
  ```
- This method:
  1. Lists all Chain CRs owned by the mission (via label selector)
  2. For each chain in Idle phase, sets `status.phase = Running` and `status.startedAt = now`
  3. Updates the chain status via status subresource
- Called in `reconcileBriefing()` just before transitioning to Active phase

**Files Changed:**
- `internal/controller/mission_controller.go`: 
  - Lines ~448-453 (call site in reconcileBriefing)
  - Lines ~820-848 (new triggerGeneratedChains helper method)

**Implementation:**
```go
// triggerGeneratedChains transitions all mission-generated chains from Idle to Running.
// This is necessary because the chain controller only triggers chains via cron schedule,
// and mission-generated chains have no schedule.
func (r *MissionReconciler) triggerGeneratedChains(ctx context.Context, mission *aiv1alpha1.Mission) error {
    log := logf.FromContext(ctx)

    // Iterate all chains owned by this mission
    chainList := &aiv1alpha1.ChainList{}
    if err := r.List(ctx, chainList,
        client.InNamespace(mission.Namespace),
        client.MatchingLabels{aiv1alpha1.LabelMission: mission.Name},
    ); err != nil {
        return fmt.Errorf("failed to list mission chains: %w", err)
    }

    for _, chain := range chainList.Items {
        // Only trigger chains that are in Idle phase
        if chain.Status.Phase != aiv1alpha1.ChainPhaseIdle && chain.Status.Phase != "" {
            continue
        }

        // Transition to Running
        log.Info("Triggering generated chain", "chain", chain.Name)
        now := metav1.Now()
        chain.Status.Phase = aiv1alpha1.ChainPhaseRunning
        chain.Status.StartedAt = &now
        if err := r.Status().Update(ctx, &chain); err != nil {
            log.Error(err, "Failed to trigger chain (will retry)", "chain", chain.Name)
            return fmt.Errorf("failed to trigger chain %s: %w", chain.Name, err)
        }
    }

    return nil
}
```

---

## Testing

### Build Verification
```bash
go build ./...  # ✅ Compiles successfully
go vet ./...    # ✅ No linting errors
```

### Expected Behavior After Fixes

1. **Meta-missions with recruited knights:**
   - Assembler now sees both `mission.Spec.Knights` and `mission.Spec.GeneratedKnights`
   - Correctly waits for all knights to become Ready
   - Log shows accurate count: `Waiting for knights total=N notReady=M`

2. **Plan validation failures:**
   - Mission stays in Planning phase
   - Retries planning after 10 seconds
   - Does NOT transition to Assembling with empty chains/knights

3. **Generated chains:**
   - Automatically transition from Idle → Running when mission reaches Active phase
   - No manual intervention required
   - Chain controller picks up Running chains and executes steps

### Recommended Integration Tests

1. Create a meta-mission that:
   - Recruits existing non-ephemeral knights
   - Generates ephemeral knights
   - Creates chains with multiple steps
   - Verify all knights are assembled and chains execute

2. Create a meta-mission with intentionally invalid plan:
   - Use a planner that returns malformed JSON
   - Verify mission stays in Planning phase
   - Fix the planner and verify retry succeeds

3. Create a meta-mission with chains only:
   - No manual chain triggering
   - Verify chains transition to Running automatically
   - Verify steps execute in correct order

---

## Files Modified

| File | Lines Changed | Description |
|------|---------------|-------------|
| `internal/mission/assembler.go` | +13/-5 | Merge GeneratedKnights into knight iteration |
| `internal/mission/planner.go` | +4/-3 | Don't set CompletedAt on validation failure |
| `internal/controller/mission_controller.go` | +45/-0 | Add chain triggering on mission start |

**Total:** 3 files changed, 61 insertions(+), 9 deletions(-)

---

## Git Branch

- **Branch:** `fix/meta-mission-assembler-bugs`
- **Commit:** e4ce40c
- **Base:** main
- **Author:** Derek (via Round Table 🏰) <derek.mackley@hotmail.com>

---

## Next Steps

1. ✅ Code review
2. ✅ Merge to main
3. 🔄 Create integration test for meta-mission e2e flow
4. 🔄 Deploy to staging environment
5. 🔄 Validate with real meta-mission scenarios

---

## Related Issues

- Fixes issue where meta-missions with recruited knights hang in Assembling phase
- Fixes issue where plan validation failures transition to Assembling prematurely
- Fixes issue where mission-generated chains never execute

---

## Notes

- All fixes are backward compatible
- No API changes required
- No breaking changes to existing missions
- Fixes only affect meta-missions (missions with `spec.metaMission: true`)
