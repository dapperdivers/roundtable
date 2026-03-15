# Fix Meta-Mission Assembler Bugs

## Summary

Fixes three critical bugs in the meta-mission planner and assembler that prevented mission-generated knights and chains from being properly assembled and executed.

## Bugs Fixed

### 🐛 Bug 1: GeneratedKnights vs Knights Field Mismatch in Assembler

**Problem:**
The assembler's `ReconcileAssembling()` only iterated `mission.Spec.Knights` but the planner writes recruited knights to `mission.Spec.GeneratedKnights`. This caused non-ephemeral recruited knights to be invisible to the assembler, resulting in an infinite loop:
```
Waiting for knights to become ready total=0 notReady=0
```

**Solution:**
Merge `mission.Spec.GeneratedKnights` into the knight list being iterated:
```go
allKnights := append(mission.Spec.Knights, mission.Spec.GeneratedKnights...)
for _, mk := range allKnights {
```

---

### 🐛 Bug 2: PlanApplied/CompletedAt Guard Fires on Failed Validations

**Problem:**
When plan validation failed, the code set both `PlanningResult.CompletedAt` and the error. On the next reconcile, the guard at line 99 (`if pr.CompletedAt != nil`) would fire and skip re-planning, transitioning to Assembling with 0 chains/0 knights.

**Solution:**
Only set `PlanningResult.CompletedAt` on successful plan application. On validation failure:
- Set `PlanningResult.Error` without CompletedAt
- Return with `RequeueAfter: 10 * time.Second` to allow retry
- Keep mission in Planning phase for another attempt

---

### 🐛 Bug 3: Mission-Generated Chains Stuck in Idle

**Problem:**
After assembling completes and mission transitions to Running phase, generated Chain CRs remain in Idle. The chain controller only triggers chains via cron schedule or manual intervention. Mission-generated chains have no schedule.

**Solution:**
Added `triggerGeneratedChains()` helper method called during transition from Briefing to Active phase:
1. Lists all Chain CRs owned by the mission
2. For each chain in Idle phase, sets `status.phase = Running` and `status.startedAt = now`
3. Updates the chain status via status subresource

---

## Changes

| File | Lines | Description |
|------|-------|-------------|
| `internal/mission/assembler.go` | +13/-5 | Merge GeneratedKnights into knight iteration |
| `internal/mission/planner.go` | +4/-3 | Don't set CompletedAt on validation failure |
| `internal/controller/mission_controller.go` | +45/-0 | Add chain triggering on mission start |

**Total:** 3 files changed, 61 insertions(+), 9 deletions(-)

## Testing

### Build Verification
- ✅ `go build ./...` - Compiles successfully
- ✅ `go vet ./...` - No linting errors

### Expected Behavior After Fixes

1. **Meta-missions with recruited knights:**
   - Assembler sees both `mission.Spec.Knights` and `mission.Spec.GeneratedKnights`
   - Correctly waits for all knights to become Ready
   - Log shows accurate count: `Waiting for knights total=N notReady=M`

2. **Plan validation failures:**
   - Mission stays in Planning phase
   - Retries planning after 10 seconds
   - Does NOT transition to Assembling with empty chains/knights

3. **Generated chains:**
   - Automatically transition from Idle → Running when mission reaches Active phase
   - Chain controller picks up Running chains and executes steps

## Backward Compatibility

- ✅ No API changes
- ✅ No breaking changes to existing missions
- ✅ Fixes only affect meta-missions (`spec.metaMission: true`)
- ✅ All changes are backward compatible

## Related Issues

- Fixes meta-missions with recruited knights hanging in Assembling phase
- Fixes plan validation failures transitioning to Assembling prematurely  
- Fixes mission-generated chains never executing

## Checklist

- [x] Code compiles without errors
- [x] No linting errors from `go vet`
- [x] Fixes address root cause of reported bugs
- [x] Changes are backward compatible
- [x] Documentation added (META_MISSION_FIXES_SUMMARY.md)
- [ ] Integration tests added (recommended follow-up)
- [ ] Tested with real meta-mission scenarios (recommended follow-up)

## How to Apply

### Option 1: Using the patch file
```bash
git apply meta-mission-assembler-fixes.patch
```

### Option 2: Merge the branch
```bash
git fetch origin fix/meta-mission-assembler-bugs
git merge origin/fix/meta-mission-assembler-bugs
```

---

## Additional Documentation

See [META_MISSION_FIXES_SUMMARY.md](./META_MISSION_FIXES_SUMMARY.md) for detailed analysis of each bug and implementation notes.
