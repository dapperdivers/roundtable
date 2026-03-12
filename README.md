# 🏰 Knights of the Round Table

**Kubernetes-Native Multi-Agent AI Orchestration**

[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![CI](https://github.com/dapperdivers/roundtable/actions/workflows/ci.yml/badge.svg)](https://github.com/dapperdivers/roundtable/actions)

> *"We are the knights who say... `kubectl apply`!"*

Round Table is a Kubernetes operator that manages fleets of AI agents as native cluster resources. Define agents, orchestrate workflows, and launch ephemeral missions — all through CRDs and GitOps.

```bash
kubectl apply -f knight.yaml   # A fully wired AI agent appears in your cluster
kubectl apply -f mission.yaml  # An ephemeral team spins up, executes, and cleans up
```

**Not a framework. Not a chatbot. An orchestration layer that treats AI agents like infrastructure.**

---

## Why Round Table?

Most multi-agent systems are either app-layer task managers or single-agent wrappers. Round Table takes a different approach: **agents ARE infrastructure**.

| | Round Table | Agent Frameworks | Workflow Engines | Task Managers |
|---|---|---|---|---|
| **Agent lifecycle** | K8s pods — ephemeral, scalable, isolated | Library objects in a process | Static containers | External processes |
| **Communication** | NATS JetStream — durable, replay, fan-out | In-memory function calls | HTTP/gRPC | Polling / webhooks |
| **State** | CRDs + NATS KV (etcd-backed, declarative) | In-memory / DB | ConfigMaps / DB | Postgres |
| **Orchestration** | Chain DAGs with templated output chaining | Code-defined graphs | YAML DAGs | Org chart delegation |
| **Isolation** | Pod-level (NetworkPolicy, ServiceAccount) | None (shared process) | Container-level | App-level scoping |
| **Ephemerality** | Missions auto-provision and teardown teams | Manual | Manual | N/A |
| **GitOps** | Native (CRDs + Flux/ArgoCD) | N/A | Partial | N/A |

## Features

🗡️ **4 Custom Resources** — Knight, RoundTable, Chain, Mission — declare your entire agent fleet in YAML

⚔️ **Ephemeral Missions** — spin up a purpose-built team of agents, execute an objective, tear everything down

🔗 **Chain DAGs** — multi-step workflows with parallel execution, templated output injection between steps

📡 **NATS JetStream** — durable task routing with WorkQueue retention, fan-out, and KV store for results

🛡️ **Pod-Level Isolation** — each knight gets its own pod, ServiceAccount, NetworkPolicy, and workspace PVC

📦 **Arsenal** — git-synced skill repository, auto-distributed to knights via sidecar

🖥️ **Dashboard** — real-time fleet monitoring, chain visualization, mission tracking ([roundtable-ui](https://github.com/dapperdivers/roundtable-ui))

🔄 **GitOps-Native** — CRDs reconcile through Flux or ArgoCD, same as any other cluster workload

---

## Custom Resources

### Knight — An AI Agent

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Knight
metadata:
  name: galahad
spec:
  domain: security
  model: claude-sonnet-4-20250514
  skills: [security, opencti, shared]
  tools:
    apt: [nmap, whois, dnsutils]
  nats:
    subjects: ["fleet-a.tasks.security.>"]
  concurrency: 2
  workspace:
    size: 2Gi
  resources:
    requests:
      memory: 256Mi
      cpu: 100m
```

The operator reconciles this into a Deployment, PVC, ConfigMaps, NATS consumer, and optional ServiceAccount. One YAML, fully wired agent.

### RoundTable — Fleet Configuration

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: RoundTable
metadata:
  name: personal
spec:
  nats:
    url: nats://nats.database.svc:4222
    subjectPrefix: fleet-a
    tasksStream: fleet-a-tasks
    resultsStream: fleet-a-results
    createStreams: true
  knightSelector:
    matchLabels:
      ai.roundtable.io/roundtable: personal
```

Manages NATS streams and selects knights by label into logical fleets. Multiple RoundTables can coexist for multi-tenant isolation.

### Chain — Workflow DAG

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Chain
metadata:
  name: security-scan
spec:
  steps:
    - name: scan
      knightRef: galahad
      task: "Scan example.com for CVEs. Report findings."
      timeout: 300

    - name: verify
      knightRef: tristan
      dependsOn: [scan]
      task: |
        Galahad found: {{ .Steps.scan.Output }}
        Check if any of these CVEs affect our cluster.
      timeout: 300

    - name: report
      knightRef: kay
      dependsOn: [verify]
      task: |
        Write a threat report:
        - Scan results: {{ .Steps.scan.Output }}
        - Exposure analysis: {{ .Steps.verify.Output }}
      timeout: 600
```

Steps run in dependency order. Parallel steps execute concurrently. Outputs are injected into downstream tasks via Go templates.

### Mission — Ephemeral Objective

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
metadata:
  name: pentest-sprint
spec:
  objective: "Penetration test the staging environment and produce a findings report."
  ttl: 3600
  timeout: 1800
  cleanupPolicy: Delete
  knights:
    - name: lead
      role: coordinator
      ephemeral: true
      ephemeralSpec:
        domain: security
        model: claude-sonnet-4-20250514
        skills: [security, shared]
        nats:
          subjects: ["mission-pentest-sprint.tasks.security.>"]
        concurrency: 1
        workspace:
          size: 2Gi
    - name: galahad
      role: scanner
```

Missions auto-provision ephemeral knights, create isolated NATS streams, execute the objective, and clean up all resources on completion. Mix ephemeral and existing knights in the same mission.

**Lifecycle:** `Pending` → `Provisioning` → `Assembling` → `Briefing` → `Active` → `Succeeded/Failed` → `CleaningUp`

---

## Architecture

```
                        ┌──────────────────────┐
                        │   roundtable-ui      │
                        │   (Dashboard)        │
                        └──────────┬───────────┘
                                   │ K8s API
┌──────────────────────────────────┼──────────────────────────────────┐
│                     Round Table Operator                            │
│                                  │                                  │
│  ┌──────────────┐  ┌────────────┴───┐  ┌────────────────────────┐  │
│  │   Knight      │  │   Mission      │  │   Chain                │  │
│  │   Controller  │  │   Controller   │  │   Controller           │  │
│  └──────┬───────┘  └───────┬────────┘  └──────────┬─────────────┘  │
│         │                  │                       │                │
└─────────┼──────────────────┼───────────────────────┼────────────────┘
          │                  │                       │
    ┌─────▼──────────────────▼───────────────────────▼─────┐
    │                    NATS JetStream                      │
    │  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐ │
    │  │ Tasks Stream │  │Results Stream│  │  KV Store    │ │
    │  └──────┬──────┘  └──────────────┘  └──────────────┘ │
    └─────────┼────────────────────────────────────────────┘
              │
    ┌─────────┼──────────────────────────────────┐
    │         │           Knight Pods             │
    │  ┌──────▼──────┐  ┌─────────────┐          │
    │  │  pi-knight   │  │  pi-knight  │  ...     │
    │  │  (runtime)   │  │  (runtime)  │          │
    │  ├─────────────┤  ├─────────────┤          │
    │  │ skill-filter │  │ skill-filter│          │
    │  ├─────────────┤  ├─────────────┤          │
    │  │ git-sync     │  │ git-sync    │          │
    │  │ (arsenal)    │  │ (arsenal)   │          │
    │  └─────────────┘  └─────────────┘          │
    └────────────────────────────────────────────┘
```

## Ecosystem

| Component | Description | Repo |
|-----------|-------------|------|
| **roundtable** | Operator + CRDs + controllers | [dapperdivers/roundtable](https://github.com/dapperdivers/roundtable) |
| **pi-knight** | Knight runtime (Node.js agent that consumes NATS tasks) | [dapperdivers/pi-knight](https://github.com/dapperdivers/pi-knight) |
| **roundtable-ui** | Dashboard for fleet monitoring and chain visualization | [dapperdivers/roundtable-ui](https://github.com/dapperdivers/roundtable-ui) |
| **roundtable-arsenal** | Skill repository (git-synced to knights via sidecar) | [dapperdivers/roundtable-arsenal](https://github.com/dapperdivers/roundtable-arsenal) |

## Quick Start

### Prerequisites

- Kubernetes 1.28+
- NATS with JetStream enabled
- An AI model API key (Anthropic, OpenAI, etc.)

### Install via Helm (recommended)

```bash
# Add the OCI chart
helm install roundtable-operator \
  oci://ghcr.io/dapperdivers/charts/roundtable-operator \
  --namespace roundtable --create-namespace \
  --set image.tag=latest \
  --set images.piKnight.tag=latest

# Create your first knight
kubectl apply -f examples/galahad.yaml

# Check status
kubectl get knights -n roundtable
NAME       DOMAIN     MODEL                       READY   AGE
galahad    security   claude-sonnet-4-20250514    true    30s
```

The chart installs CRDs, the operator, and the [dashboard](#ecosystem) in one shot.

### Install from source

```bash
git clone https://github.com/dapperdivers/roundtable.git
cd roundtable

make install   # CRDs
make deploy IMG=ghcr.io/dapperdivers/roundtable:latest
```

### What Happens

1. The operator sees the `Knight` CR and creates a Deployment with the pi-knight runtime
2. A skill-filter sidecar mounts only the skills matching `spec.skills`
3. A git-sync sidecar pulls the latest arsenal (skill definitions)
4. A NATS JetStream consumer is configured with the knight's filter subjects
5. The knight starts consuming tasks from the stream

### Send a Task

```bash
# Publish a task via NATS
nats pub fleet-a.tasks.security.scan '{"task": "Scan example.com for open ports"}'
```

## Development

```bash
# Generate CRD manifests + deep copy
make manifests generate

# Run locally against your cluster
make run

# Run tests
make test

# Build the operator image
make docker-build docker-push IMG=ghcr.io/dapperdivers/roundtable:latest
```

## Roadmap

- [x] **Phase 1** — Knight operator (CRD → Pod + NATS consumer)
- [x] **Phase 2** — Chains (DAG workflows with output chaining)
- [x] **Phase 3a** — Missions with existing knights
- [x] **Phase 3b** — Ephemeral missions (dynamic knight provisioning + teardown)
- [ ] **Phase 4** — Observability (OpenTelemetry tracing, cost tracking)
- [ ] **Phase 5** — Multi-cluster federation

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

Areas where help is needed:
- Multi-cluster support
- OpenTelemetry integration
- Additional knight runtime implementations
- Dashboard improvements
- Documentation

## License

Apache 2.0 — See [LICENSE](LICENSE)

---

*"On second thought, let's not go to Lambda. 'Tis a silly place."* 🏰
