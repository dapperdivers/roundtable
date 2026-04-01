# Helm Chart Configuration

## Installation

```bash
# From OCI registry
helm install roundtable oci://ghcr.io/dapperdivers/charts/roundtable-operator \
  --namespace roundtable --create-namespace

# From local chart
helm install roundtable charts/roundtable-operator \
  --namespace roundtable --create-namespace
```

## Values Reference

### Operator

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `replicaCount` | int | `1` | Operator replicas (leader election handles HA) |
| `image.repository` | string | `ghcr.io/dapperdivers/roundtable` | Operator image |
| `image.tag` | string | `latest` | Operator image tag |
| `image.pullPolicy` | string | `Always` | Image pull policy |
| `leaderElect` | bool | `true` | Enable leader election for HA |
| `resources.requests.cpu` | string | `50m` | CPU request |
| `resources.requests.memory` | string | `64Mi` | Memory request |
| `resources.limits.cpu` | string | `200m` | CPU limit |
| `resources.limits.memory` | string | `128Mi` | Memory limit |
| `extraArgs` | list | `[]` | Additional args for the manager binary |

### Service Account

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `serviceAccount.create` | bool | `true` | Create a ServiceAccount |
| `serviceAccount.name` | string | `roundtable-operator` | ServiceAccount name |
| `serviceAccount.annotations` | object | `{}` | ServiceAccount annotations |

### Global Images

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `images.piKnight.repository` | string | `ghcr.io/dapperdivers/pi-knight` | Default pi-knight image for all knights |
| `images.piKnight.tag` | string | `latest` | pi-knight image tag |
| `images.dashboard.repository` | string | `ghcr.io/dapperdivers/roundtable-ui` | Dashboard image |
| `images.dashboard.tag` | string | `latest` | Dashboard image tag |

### Dashboard

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `dashboard.enabled` | bool | `true` | Deploy the Round Table dashboard |
| `dashboard.replicas` | int | `1` | Dashboard replicas |
| `dashboard.ingress.enabled` | bool | `false` | Enable ingress for dashboard |
| `dashboard.ingress.className` | string | `""` | Ingress class name |
| `dashboard.ingress.host` | string | `""` | Dashboard hostname |
| `dashboard.env.NATS_URL` | string | `nats://nats.database.svc:4222` | NATS server URL |
| `dashboard.env.NAMESPACE` | string | `roundtable` | Namespace to watch |
| `dashboard.vault.enabled` | bool | `true` | Mount shared Obsidian vault |
| `dashboard.vault.claimName` | string | `roundtable-vault` | Vault PVC name |

### Knights

Define knights inline in values to deploy them with the operator:

```yaml
knights:
  galahad:
    domain: security
    model: claude-sonnet-4-20250514
    skills: [security, threat-intel]
    tools:
      apt: [nmap, whois]
    nats:
      subjects: ["fleet-a.tasks.security.>"]
    taskTimeout: 300
```

### Planner

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `planner.enabled` | bool | `true` | Deploy the built-in planner knight |
| `planner.domain` | string | `operator` | Planner domain |
| `planner.model` | string | `claude-sonnet-4-20250514` | Planner model |
| `planner.skills` | list | `[operator, shared]` | Planner skills |
| `planner.concurrency` | int | `1` | Max concurrent tasks |
| `planner.taskTimeout` | int | `600` | Task timeout in seconds |
| `planner.arsenal.repo` | string | `https://github.com/dapperdivers/roundtable-arsenal.git` | Skills git repo |

### Scheduling

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `nodeSelector` | object | `{}` | Node selector for operator pod |
| `tolerations` | list | `[]` | Tolerations for operator pod |
| `affinity` | object | `{}` | Affinity rules for operator pod |
