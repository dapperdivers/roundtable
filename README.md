# 🏰 Knights of the Round Table

**A Kubernetes Operator for Multi-Agent AI Orchestration**

> *"What... is your domain?" "Security!" "What... is your model?" "Claude!"*
> *"What... is the airspeed velocity of an unladen swallow?"*

## Overview

The Round Table operator manages AI agents ("Knights") as first-class Kubernetes resources. Each Knight is a specialized AI agent with its own domain expertise, tools, skills, and communication channels — all declared in a single YAML manifest.

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
  taskTimeout: 300
```

`kubectl apply` → A fully wired AI agent appears in your cluster.

## What It Does

The operator watches `Knight` custom resources and reconciles them into:

| Resource | Purpose |
|----------|---------|
| **Deployment** | Pi-knight runtime container + skill-filter sidecar |
| **PVC** | Persistent workspace (memory, session state) |
| **ConfigMap** | Tool definitions (`mise.toml`, `apt.txt`), prompt overrides |
| **NATS Consumer** | JetStream durable consumer with configured filter subjects |
| **ServiceAccount** | Per-knight RBAC (optional) |

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Round Table Operator               │
│                                                      │
│  Watches Knight CRs ──► Reconciles K8s Resources    │
│                                                      │
└──────────┬──────────────────────────────┬────────────┘
           │                              │
    ┌──────▼──────┐               ┌───────▼──────┐
    │  Knight Pod  │               │  Knight Pod  │
    │ ┌──────────┐ │               │ ┌──────────┐ │
    │ │ pi-knight │ │    NATS      │ │ pi-knight │ │
    │ │ runtime   │◄├──JetStream──►│ │ runtime   │ │
    │ └──────────┘ │               │ └──────────┘ │
    │ ┌──────────┐ │               │ ┌──────────┐ │
    │ │  skill    │ │               │ │  skill    │ │
    │ │  filter   │ │               │ │  filter   │ │
    │ └──────────┘ │               │ └──────────┘ │
    │      │       │               │      │       │
    │  /vault (RO) │               │  /vault (RO) │
    └──────────────┘               └──────────────┘
```

## Custom Resources

### Knight

A `Knight` represents a specialized AI agent with:

- **Domain** — area of expertise (security, infrastructure, finance, etc.)
- **Model** — which AI model to use
- **Skills** — filtered skill categories injected at deploy time
- **Tools** — system packages (apt) and dev tools (mise) installed on startup
- **NATS** — JetStream consumer configuration for task routing
- **Vault** — shared knowledge base mount with granular write permissions
- **Prompt** — customizable identity and instructions

See [examples/](./examples/) for complete manifests.

### RoundTable (planned)

Cluster-level configuration: NATS connection defaults, shared vault settings, global policies.

### Chain (planned)

Declarative event-driven agent chaining: "When Galahad finds a CVE, Tristan checks exposure."

## Quick Start

```bash
# Install CRDs
make install

# Deploy the operator
make deploy

# Create a knight
kubectl apply -f examples/galahad.yaml

# Check status
kubectl get knights
NAME       DOMAIN     MODEL                    PHASE   READY   TASKS   AGE
galahad    security   claude-sonnet-4-20250514   Ready   true    42      3d
```

## Prerequisites

- Kubernetes 1.28+
- NATS with JetStream enabled
- An AI model API key (Anthropic, etc.)
- [pi-knight](https://github.com/dapperdivers/pi-knight) container image

## Development

```bash
# Generate CRD manifests
make manifests

# Run locally against your cluster
make run

# Build the operator image
make docker-build docker-push IMG=ghcr.io/dapperdivers/roundtable:latest

# Run tests
make test
```

## Background

Born from a homelab experiment in multi-agent AI orchestration. What started as Helm charts and manual wiring evolved into an operator because we kept asking: *"Why am I writing 5 resources for every new agent?"*

The answer: you shouldn't have to. `kubectl apply -f knight.yaml` and go.

## License

Apache 2.0

---

*"We are the Knights of the Round Table. We dance whene'er we're able."* 🏰
