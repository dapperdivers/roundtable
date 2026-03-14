# Round Table Operator — Architecture Deep Dive

**Date:** 2026-03-14  
**Codebase:** 13,328 LOC across 26 Go files  
**Assessment:** Moderate coupling, several extraction opportunities

---

## Executive Summary

The operator works but is showing strain. The core problem: **everything lives in one flat package** (`internal/controller/`) with shared state patterns that make changes cascade unpredictably. Adding meta-missions required `plan.go` to reach into `mission_controller.go`'s internals, and the NATS client is independently initialized by 3 different controllers.

**Key metrics:**
| File | Lines | Responsibility |
|------|-------|---------------|
| `mission_controller.go` | 1,883 | Mission lifecycle, ephemeral resources, NATS, cost, cleanup |
| `plan.go` | 1,116 | Meta-mission planning (tightly coupled to mission_controller) |
| `chain_controller.go` | 1,075 | Chain DAG execution, NATS polling, cron, KV storage |
| `knight_controller.go` | 1,032 | Knight → Deployment/PVC/ConfigMap reconciliation |
| `roundtable_controller.go` | 336 | Health aggregation, stream management |
| `pkg/nats/client.go` | 504 | NATS JetStream wrapper |

---

## Problem 1: NATS Client Duplication

**Three controllers independently manage NATS connections:**

```go
// chain_controller.go
func (r *ChainReconciler) ensureNATS(ctx context.Context) error {
    r.mu.Lock(); defer r.mu.Unlock()
    if r.natsClient != nil && r.natsClient.IsConnected() { return nil }
    config := natspkg.DefaultConfig()
    r.natsClient = natspkg.NewClient(config, log)
    return r.natsClient.Connect()
}

// mission_controller.go — IDENTICAL pattern
func (r *MissionReconciler) ensureNATS(ctx context.Context) error { ... }

// roundtable_controller.go — SLIGHTLY DIFFERENT (uses RT's URL)
func (r *RoundTableReconciler) ensureStreams(ctx context.Context, rt *aiv1alpha1.RoundTable) error {
    if r.natsClient == nil {
        url := rt.Spec.NATS.URL; ...
        r.natsClient = natspkg.NewClient(config, log)
    }
}
```

**Impact:** 3 independent NATS connections instead of 1. Each has its own mutex, connection lifecycle, and reconnect behavior. If NATS goes down, each controller recovers independently with different timing.

### Fix: Shared NATS Provider

```go
// pkg/nats/provider.go
type Provider struct {
    client Client
    mu     sync.Mutex
    config Config
    log    logr.Logger
}

func NewProvider(config Config, log logr.Logger) *Provider { ... }
func (p *Provider) Client() (Client, error) { ... } // lazy connect, single instance
```

Inject `*Provider` into all controllers via `cmd/main.go`:

```go
natsProvider := nats.NewProvider(nats.DefaultConfig(), setupLog)

(&controller.ChainReconciler{Client: mgr.GetClient(), NATS: natsProvider}).SetupWithManager(mgr)
(&controller.MissionReconciler{Client: mgr.GetClient(), NATS: natsProvider}).SetupWithManager(mgr)
(&controller.RoundTableReconciler{Client: mgr.GetClient(), NATS: natsProvider}).SetupWithManager(mgr)
```

**Effort:** Small. **Impact:** Eliminates 2 redundant connections + 3 duplicate `ensureNATS` methods.

---

## Problem 2: God Object — MissionReconciler

`mission_controller.go` is 1,883 lines doing **everything**:

- Phase state machine (8 phases)
- Ephemeral Knight creation
- Ephemeral RoundTable creation
- ServiceAccount creation
- NetworkPolicy creation
- NATS briefing publishing
- Chain lifecycle management
- Cost aggregation & budget enforcement
- Results storage to NATS KV
- NATS consumer/stream cleanup
- Knight spec resolution (template + override merging)

Plus `plan.go` adds another 1,116 lines for meta-mission planning, tightly coupled via methods on `MissionReconciler`.

### Fix: Extract Domain Services

```
internal/
  controller/
    mission_controller.go      ← slim phase state machine (~400 lines)
    chain_controller.go        ← DAG executor
    knight_controller.go       ← deployment builder
    roundtable_controller.go   ← health aggregator
  mission/
    assembler.go               ← ephemeral knight/RT creation, SA, NetworkPolicy
    briefing.go                ← NATS briefing publication
    planner.go                 ← meta-mission planning (current plan.go)
    cost.go                    ← cost aggregation & budget checks
    cleanup.go                 ← resource deletion, KV storage
    chain_manager.go           ← mission chain creation & monitoring
    template_resolver.go       ← knight spec resolution from templates
```

The controller becomes a **coordinator** that delegates:

```go
func (r *MissionReconciler) reconcileActive(ctx context.Context, mission *aiv1alpha1.Mission) (ctrl.Result, error) {
    if err := r.costService.CheckBudget(ctx, mission); err != nil {
        return r.costService.HandleOverBudget(ctx, mission)
    }
    return r.chainManager.ReconcileChains(ctx, mission)
}
```

**Effort:** Medium (refactor, not rewrite). **Impact:** Each concern becomes independently testable. Adding features doesn't require understanding 3,000 lines.

---

## Problem 3: Status Update Patterns (Cascading Bug Source)

Every controller has ad-hoc status update patterns that cause conflicts:

```go
// mission_controller.go — reconcileCleaningUp
mission.Status.Phase = aiv1alpha1.MissionPhaseCleaningUp
now := metav1.Now()
mission.Status.CompletedAt = &now
mission.Status.Result = "Mission expired (TTL exceeded)"
meta.SetStatusCondition(&mission.Status.Conditions, ...)
mission.Status.ObservedGeneration = mission.Generation
return ctrl.Result{...}, r.Status().Update(ctx, mission)
```

This pattern is repeated ~20 times across mission_controller.go alone, each with slightly different fields being set. Missing `ObservedGeneration` in one path? Silent bug. Forgetting to update a condition? Status drift.

### Fix: Status Builder Pattern

```go
// internal/status/builder.go
type MissionStatusUpdate struct {
    mission *aiv1alpha1.Mission
    dirty   bool
}

func ForMission(m *aiv1alpha1.Mission) *MissionStatusUpdate { ... }

func (u *MissionStatusUpdate) Phase(p aiv1alpha1.MissionPhase) *MissionStatusUpdate {
    u.mission.Status.Phase = p
    u.mission.Status.ObservedGeneration = u.mission.Generation // always set
    u.dirty = true
    return u
}

func (u *MissionStatusUpdate) Complete(result string) *MissionStatusUpdate {
    now := metav1.Now()
    u.mission.Status.CompletedAt = &now
    u.mission.Status.Result = result
    return u.Phase(u.mission.Status.Phase) // ensures ObservedGeneration
}

func (u *MissionStatusUpdate) Condition(typ, reason, msg string, status metav1.ConditionStatus) *MissionStatusUpdate {
    meta.SetStatusCondition(&u.mission.Status.Conditions, metav1.Condition{
        Type: typ, Status: status, Reason: reason, Message: msg,
        ObservedGeneration: u.mission.Generation,
    })
    return u
}

func (u *MissionStatusUpdate) Apply(ctx context.Context, client client.StatusClient) error {
    if !u.dirty { return nil }
    return client.Status().Update(ctx, u.mission)
}
```

**Effort:** Small. **Impact:** Eliminates an entire class of "forgot to set ObservedGeneration" bugs.

---

## Problem 4: Duplicate Utility Functions

```go
// chain_controller.go
func containsString(slice []string, s string) bool { ... }
func removeString(slice []string, s string) []string { ... }

// These are used by mission_controller.go too (via same package).
// But also:
// - plan.go has its own isValidK8sName(), isValidSkillName(), truncate()
// - knight_controller.go has capitalizeFirst(), boolPtr(), intstrPort()
// - chain_controller.go has its own validateDAG()
// - plan.go has ANOTHER validateDAG() (standalone function, not method)
```

**Two `validateDAG` implementations exist:**
1. `ChainReconciler.validateDAG()` — Kahn's algorithm (method on reconciler)
2. `validateDAG()` in plan.go — DFS-based cycle detection (standalone function)

Both do the same thing. Different algorithms. Neither calls the other.

### Fix: `internal/util/` package

```go
// internal/util/strings.go
func Contains(slice []string, s string) bool { ... }
func Remove(slice []string, s string) []string { ... }
func Capitalize(s string) string { ... }
func Truncate(s string, max int) string { ... }

// internal/util/k8s.go
func IsValidName(name string) bool { ... }
func BoolPtr(b bool) *bool { ... }

// internal/util/dag.go
func ValidateDAG(steps []Step) error { ... } // one implementation
```

**Effort:** Tiny. **Impact:** No more duplicate logic diverging silently.

---

## Problem 5: `plan.go` — File Comment Says It All

```go
// Meta-missions planning phase implementation
// This code should be integrated into internal/controller/mission_controller.go
```

The file literally tells you it's a bolt-on. It declares standalone types (`PlannerOutput`, `PlannerChain`, etc.) that duplicate CRD types, and methods on `MissionReconciler` that reach deep into mission internals. But it's a separate file because cramming more into 1,883 lines was untenable.

**The real issue:** `plan.go` has its own `validateDAG`, its own `resolveRoundTable`, and builds planning prompts by string concatenation. It's a separate concern forced into the same struct.

### Fix: `internal/mission/planner.go`

Extract `Planner` as its own type:

```go
type Planner struct {
    client     client.Client
    nats       *nats.Provider
    scheme     *runtime.Scheme
}

func (p *Planner) Plan(ctx context.Context, mission *aiv1alpha1.Mission) (*PlannerOutput, error) { ... }
func (p *Planner) Dispatch(ctx context.Context, mission *aiv1alpha1.Mission, knight *aiv1alpha1.Knight) (string, error) { ... }
func (p *Planner) Poll(ctx context.Context, mission *aiv1alpha1.Mission, taskID string) (*PlannerOutput, error) { ... }
func (p *Planner) Apply(ctx context.Context, mission *aiv1alpha1.Mission, plan *PlannerOutput) error { ... }
```

---

## Problem 6: Knight PodSpec Builder (400+ lines, no separation)

`buildPodSpec()` is a single 200+ line method that constructs the entire pod spec inline — containers, volumes, mounts, probes, sidecars, security context. Adding any new sidecar or volume mount means modifying this monolith.

### Fix: Builder Pattern

```go
// internal/knight/pod_builder.go
type PodBuilder struct {
    knight  *aiv1alpha1.Knight
    volumes []corev1.Volume
    mounts  []corev1.VolumeMount
}

func NewPodBuilder(knight *aiv1alpha1.Knight) *PodBuilder { ... }
func (b *PodBuilder) WithWorkspace() *PodBuilder { ... }
func (b *PodBuilder) WithNixStore() *PodBuilder { ... }
func (b *PodBuilder) WithVault() *PodBuilder { ... }
func (b *PodBuilder) WithSharedWorkspace(rt *aiv1alpha1.RoundTable) *PodBuilder { ... }
func (b *PodBuilder) WithArsenal() *PodBuilder { ... }
func (b *PodBuilder) WithSkillFilter(skills []string) *PodBuilder { ... }
func (b *PodBuilder) WithGitSync(arsenal *aiv1alpha1.ArsenalSpec) *PodBuilder { ... }
func (b *PodBuilder) Build() corev1.PodSpec { ... }
```

**Effort:** Medium. **Impact:** New sidecars/volumes become composable. Each `With*` method is independently testable.

---

## Problem 7: Test Coupling

Tests mock at the wrong boundary. `mission_controller_test.go` (1,336 lines) and `mission_integration_test.go` (832 lines) test the entire reconcile loop, which means:

- Any internal refactor breaks tests
- Tests are slow (envtest = real API server + etcd)
- Hard to test edge cases in cost calculation or template resolution without going through the full reconcile path

### Fix: Test at Service Boundaries

With the domain service extraction (Problem 2), each service gets unit tests:

```go
// internal/mission/cost_test.go
func TestCheckBudget_ExceedsBudget(t *testing.T) {
    svc := &CostService{client: fake.NewClientBuilder()...}
    exceeded, err := svc.CheckBudget(ctx, mission)
    assert.True(t, exceeded)
}
```

Keep integration tests for the controller, but make them thinner — they verify phase transitions and delegation, not business logic.

---

## Problem 8: Hardcoded `fleet-a` Fallbacks

Three known hardcoded `fleet-a` references (from MEMORY.md):
- `mission_controller.go:1053` (or nearby)
- `knight_controller.go:996/1002`
- `chain_controller.go:55` (`defaultNATSConfig`)

```go
// chain_controller.go:55
var defaultNATSConfig = natsConfig{
    SubjectPrefix: "fleet-a",       // ← hardcoded
    TasksStream:   "fleet_a_tasks", // ← hardcoded
    ResultsStream: "fleet_a_results",
}
```

These should be eliminated entirely. A chain without a `roundTableRef` should be an error, not silently fall back to `fleet-a`.

---

## Recommended Refactoring Order

**Phase 1 — Quick Wins (1-2 days)**
1. ✅ Extract `internal/util/` — deduplicate strings, DAG validation, k8s helpers
2. ✅ Create `pkg/nats/provider.go` — shared NATS connection
3. ✅ Remove hardcoded `fleet-a` fallbacks — require `roundTableRef`
4. ✅ Status builder pattern — eliminate ObservedGeneration bugs

**Phase 2 — Mission Decomposition (2-3 days)**
5. Extract `internal/mission/planner.go` — decouple planning from reconciler
6. Extract `internal/mission/assembler.go` — ephemeral resource creation
7. Extract `internal/mission/cleanup.go` — resource deletion + KV storage
8. Extract `internal/mission/cost.go` — budget tracking
9. Extract `internal/mission/chain_manager.go` — chain lifecycle

**Phase 3 — Knight Builder (1 day)**
10. Extract `internal/knight/pod_builder.go` — composable pod construction

**Phase 4 — Test Restructure (1-2 days)**
11. Unit tests for extracted services
12. Slim down integration tests to phase-transition verification

---

## Dependency Graph (Current vs. Proposed)

### Current
```
cmd/main.go
  └── internal/controller/  (one flat package, everything sees everything)
        ├── knight_controller.go   → pkg/nats (direct)
        ├── chain_controller.go    → pkg/nats (direct)
        ├── mission_controller.go  → pkg/nats (direct)
        ├── plan.go                → (methods on MissionReconciler)
        └── roundtable_controller.go → pkg/nats (direct)
```

### Proposed
```
cmd/main.go
  ├── pkg/nats/provider.go  (shared, injected)
  ├── internal/util/         (strings, dag, k8s helpers)
  ├── internal/status/       (builder pattern)
  ├── internal/knight/
  │     └── pod_builder.go   (composable pod construction)
  ├── internal/mission/
  │     ├── planner.go       (meta-mission planning)
  │     ├── assembler.go     (ephemeral resource creation)
  │     ├── cleanup.go       (teardown + KV storage)
  │     ├── cost.go          (budget tracking)
  │     ├── chain_manager.go (chain lifecycle)
  │     └── template_resolver.go (knight spec resolution)
  └── internal/controller/   (slim reconcilers — coordination only)
        ├── knight_controller.go
        ├── chain_controller.go
        ├── mission_controller.go
        └── roundtable_controller.go
```

---

## What NOT to Change

- **CRD types** (`api/v1alpha1/`) — stable, well-structured, leave them
- **pkg/nats/client.go interface** — clean, just needs the Provider wrapper
- **Helm chart structure** — separate concern, works fine
- **Controller-runtime patterns** — `SetupWithManager`, `Reconcile`, `Owns` are idiomatic

---

*The operator isn't broken — it's outgrowing its architecture. These changes make each controller a thin coordinator over domain services, which is the standard pattern for production Kubernetes operators.*
