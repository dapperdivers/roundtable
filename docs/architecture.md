# Architecture Overview

## System Design

The Round Table follows a hub-and-spoke pattern with NATS JetStream as the central message bus.

```
┌─────────────────────────────────────────────────┐
│                  Kubernetes Cluster               │
│                                                   │
│  ┌──────────┐    ┌──────────────┐                │
│  │   Tim     │───▶│  NATS        │                │
│  │ (Enchanter│◀───│  JetStream   │                │
│  │  Gateway) │    │              │                │
│  └──────────┘    └──────┬───────┘                │
│                    ┌────┼────┐                    │
│                    ▼    ▼    ▼                    │
│              ┌─────────────────────┐             │
│              │   Knight Pods        │             │
│              │ ┌─────────────────┐ │             │
│              │ │ OpenClaw Gateway │ │             │
│              │ │ (Agent Runtime)  │ │             │
│              │ ├─────────────────┤ │             │
│              │ │ nats-bridge     │ │             │
│              │ │ (Sidecar)       │ │             │
│              │ └─────────────────┘ │             │
│              └─────────────────────┘             │
│                                                   │
│  ┌──────────┐    ┌──────────────┐                │
│  │  Redis    │    │  Shared       │                │
│  │ (State)   │    │  Volumes      │                │
│  └──────────┘    └──────────────┘                │
└─────────────────────────────────────────────────┘
```

## Components

### Tim the Enchanter
- Lead AI agent, OpenClaw gateway
- Receives commands from Derek (human)
- Dispatches tasks to Knights via NATS
- Aggregates results, makes decisions

### NATS JetStream
- Central message bus
- Durable streams for task reliability
- Topic-based routing per knight and function

### Knight Pods
Each knight is a Kubernetes pod with two containers:
1. **OpenClaw Gateway** — The AI agent runtime with personality, skills, and tools
2. **nats-bridge Sidecar** — Bridges NATS messages to/from the OpenClaw webhook API

### Redis / Valkey
- Shared state store for cross-knight memory
- Task deduplication
- Rate limiting

## Message Flow

1. Tim publishes task → `knight.<name>.task`
2. nats-bridge receives → POSTs to OpenClaw webhook (`localhost:18789`)
3. OpenClaw processes → Returns response
4. nats-bridge publishes result → `knight.<name>.result`
5. Tim's subscription picks up the result

## Deployment

All infrastructure is deployed via **Flux CD** (GitOps):
- HelmReleases for NATS, Redis
- Kustomize overlays for each Knight
- Workspace configs mounted as ConfigMaps/Secrets
