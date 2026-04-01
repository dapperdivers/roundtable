# Architecture

## Overview

**Knights of the Round Table** is a Kubernetes operator that orchestrates fleets of AI agents as first-class cluster resources. It provides declarative management, workflow orchestration, and cost control through four Custom Resource Definitions.

### Core Metaphor

| Concept | Kubernetes Resource | Purpose |
|---------|-------------------|---------|
| **Knight** | Pod (Deployment/Sandbox) | A single AI agent with a domain, model, and skills |
| **RoundTable** | Fleet config | A group of knights sharing NATS infrastructure and policies |
| **Chain** | DAG workflow | Multi-step task pipeline with output chaining between knights |
| **Mission** | Ephemeral team | Auto-assembles knights for a time-bounded objective |

## System Architecture

```
┌────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                       │
│                                                            │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              Operator Pod                            │  │
│  │  ┌───────────────┐  ┌───────────────────────────┐  │  │
│  │  │  Controllers   │  │  Runtime Backends          │  │  │
│  │  │  • Knight      │  │  • DeploymentBackend      │  │  │
│  │  │  • RoundTable  │  │  • SandboxBackend         │  │  │
│  │  │  • Chain       │  │    (scale-to-zero)        │  │  │
│  │  │  • Mission     │  └───────────────────────────┘  │  │
│  │  └───────────────┘                                   │  │
│  └─────────────────────────────────────────────────────┘  │
│           │ watches/reconciles                             │
│           ▼                                                │
│  ┌─────────────────────────────────────────────────────┐  │
│  │              CRDs (etcd)                             │  │
│  │  RoundTable ──owns──► Knight[]                      │  │
│  │  Mission ──creates──► ephemeral RoundTable + Chain[]│  │
│  │  Chain ──references──► Knight (by name/domain)      │  │
│  └─────────────────────────────────────────────────────┘  │
│           │ creates pods                                   │
│           ▼                                                │
│  ┌─────────────────────────────────────────────────────┐  │
│  │         Knight Pods (pi-knight runtime)              │  │
│  │  ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐      │  │
│  │  │Galahad │ │Tristan │ │ Warm-1 │ │ Warm-2 │      │  │
│  │  │security│ │ infra  │ │(idle)  │ │(idle)  │      │  │
│  │  └───┬────┘ └───┬────┘ └────────┘ └────────┘      │  │
│  │      │          │                                    │  │
│  │      ▼          ▼                                    │  │
│  │  ┌─────────────────────────────────────────────┐    │  │
│  │  │           NATS JetStream                     │    │  │
│  │  │  Tasks:   {prefix}.tasks.{domain}.>          │    │  │
│  │  │  Results: {prefix}.results.{task_id}         │    │  │
│  │  │  KV:     chain-outputs (step result cache)   │    │  │
│  │  └─────────────────────────────────────────────┘    │  │
│  └─────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────┘
```

## Controller Reconciliation

Each CRD has a dedicated reconciler following the standard Kubernetes controller pattern:

**KnightReconciler**: ConfigMap → PVC → Runtime (Deployment or Sandbox) → Status
**RoundTableReconciler**: Knight discovery → Health aggregation → NATS streams → Warm pool → Cost check
**ChainReconciler**: Step ordering → Task dispatch → Output collection → Template rendering → Next step
**MissionReconciler**: State machine (Pending → Provisioning → Planning → Assembling → Briefing → Active → Cleanup)

## Runtime Backends

The operator uses a pluggable `RuntimeBackend` interface:

```go
type RuntimeBackend interface {
    Reconcile(ctx context.Context, knight *Knight) error
    Cleanup(ctx context.Context, knight *Knight) error
    IsReady(ctx context.Context, knight *Knight) (bool, error)
    Suspend(ctx context.Context, knight *Knight) error
    Resume(ctx context.Context, knight *Knight) error
}
```

- **DeploymentBackend** — Standard Kubernetes Deployment. Always running. Good for persistent knights.
- **SandboxBackend** — Uses `agents.x-k8s.io/Sandbox` CRD. Scale-to-zero capable. Good for cost optimization.

Selected per-knight via `spec.runtime: deployment|sandbox`.

## NATS Communication

### Subject Routing

```
{prefix}.tasks.{domain}.>        — All tasks for a domain
{prefix}.tasks.{domain}.{knight} — Tasks for a specific knight
{prefix}.results.{task_id}       — Result for a specific task
```

### Consumer Model

Each knight creates a **durable JetStream consumer** with:
- Filter subject matching its domain
- Explicit ack policy (exactly-once delivery)
- MaxDeliver for retry handling

### Message Flow

1. Tim/Chain/Mission publishes task → `fleet-a.tasks.security.galahad`
2. Galahad's consumer pulls the message
3. pi-knight runtime processes it (LLM call + tool use)
4. Result published → `fleet-a.results.{task_id}`
5. Chain controller or caller picks up the result

## Mission Lifecycle

```
Pending → Provisioning → Planning → Assembling → Briefing → Active → Succeeded/Failed → CleaningUp
```

| Phase | What Happens |
|-------|-------------|
| **Pending** | Validate spec, set initial status |
| **Provisioning** | Create ephemeral RoundTable + NATS streams |
| **Planning** | (meta-missions) Dispatch to planner knight, receive Chain definitions |
| **Assembling** | Create/claim knights, wait for Ready. **Warm pool claiming happens here.** |
| **Briefing** | Publish mission context to all knights via NATS |
| **Active** | Execute chains, track costs, monitor timeout |
| **CleaningUp** | Delete ephemeral resources, preserve results if configured |

## Warm Pool

RoundTable maintains pre-warmed knight pods for instant mission startup:

1. **RoundTable controller** creates N warm knights (labeled `warm-pool=true`, `claimed=false`)
2. **Mission assembler** tries to claim warm knights before cold-starting
3. **Optimistic locking** prevents race conditions — conflict → try next candidate
4. **Pool replenishes** automatically (burst-limited to 5 per reconcile)
5. **Idle recycling** deletes warm knights older than `maxIdleTime`

Result: Mission spin-up goes from ~30s to <1s.

## Chain Execution

Chains are multi-step DAG workflows with Go template output chaining:

```yaml
steps:
  - name: scan
    knightRef: galahad
    prompt: "Scan for CVEs"
  - name: report
    knightRef: kay
    prompt: "Summarize: {{ .Steps.scan.Output }}"
```

Steps execute in dependency order. Independent steps run in parallel. Each step's output is available to subsequent steps via `{{ .Steps.<name>.Output }}`.

## Cost Tracking

Costs tracked at three levels:
- **Per-knight**: `status.totalCost` (lifetime)
- **Per-mission**: `status.totalCost` (mission duration)
- **Per-RoundTable**: aggregated across all knights

Budget enforcement: `spec.policies.costBudgetUSD` triggers OverBudget phase when exceeded.

## Directory Structure

```
api/v1alpha1/           — CRD type definitions
internal/controller/    — Reconcilers (one per CRD)
internal/mission/       — KnightAssembler, mission lifecycle helpers
pkg/runtime/            — RuntimeBackend interface + implementations
pkg/nats/               — JetStream client wrapper
charts/roundtable-operator/ — Helm chart
examples/               — Real-world manifest examples
docs/                   — Documentation
```
