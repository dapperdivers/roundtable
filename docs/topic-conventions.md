# NATS Topic Conventions

## Fleet-Scoped Topics

Every fleet (lead agent + its knights) operates under its own topic prefix, ensuring isolation:

```
<fleet-id>.tasks.<domain>.<action>        → Task dispatch within a fleet
<fleet-id>.results.<domain>.<task-id>     → Results back to the fleet's lead agent
<fleet-id>.heartbeat.<agent-id>           → Knight health within a fleet
<fleet-id>.events.<event-type>            → Fleet-scoped system events

roundtable.broadcast.*                    → Cross-fleet announcements
roundtable.peer.<from>.<to>              → Lead agent peer communication
```

This means Fleet A's Galahad and Fleet B's Galahad are completely independent — same knight template, different fleet prefix, different instances.

## Topic Hierarchy

```mermaid
graph TD
    subgraph Fleet["&lt;fleet-id&gt;.*"]
        FID["&lt;fleet-id&gt;"] --> Tasks["tasks"]
        FID --> Results["results"]
        FID --> Events["events"]
        FID --> HB["heartbeat"]

        Tasks --> TSec["security.*"]
        Tasks --> TComms["comms.*"]
        Tasks --> TIntel["intel.*"]
        Tasks --> TObs["observability.*"]
        Tasks --> THome["home.*"]

        Results --> RSec["security.&lt;task-id&gt;"]
        Results --> RComms["comms.&lt;task-id&gt;"]

        Events --> ETClaimed["task.claimed"]
        Events --> ETProgress["task.progress"]
        Events --> ESystem["system.*"]

        HB --> HGal["galahad"]
        HB --> HPer["percival"]
        HB --> HGaw["gawain"]
    end

    subgraph Shared["roundtable.*"]
        RT["roundtable"] --> Broadcast["broadcast.*"]
        RT --> Peer["peer.&lt;from&gt;.&lt;to&gt;"]
    end
```

## Subject Format

```
<fleet-id>.<category>.<domain>.<action|id>
```

### Categories

| Category | Purpose | Pattern |
|----------|---------|---------|
| `tasks` | Task requests from lead agent to knights | `<fleet-id>.tasks.<domain>.<action>` |
| `results` | Task results from knights back to lead agent | `<fleet-id>.results.<domain>.<task-id>` |
| `events` | Fleet system events, lifecycle, progress | `<fleet-id>.events.<event-type>` |
| `heartbeat` | Agent health signals | `<fleet-id>.heartbeat.<agent-id>` |
| `broadcast` | Cross-fleet announcements | `roundtable.broadcast.<topic>` |
| `peer` | Lead agent peer communication | `roundtable.peer.<from>.<to>` |

## Domain Subjects

### Security (Galahad)

| Subject | Direction | Description |
|---------|-----------|-------------|
| `fleet-id.tasks.security.briefing` | Tim → Galahad | Daily security briefing |
| `fleet-id.tasks.security.cve-analysis` | Tim → Galahad | Deep dive on specific CVE |
| `fleet-id.tasks.security.threat-scan` | Tim → Galahad | Scan specific threat feeds |
| `fleet-id.tasks.security.incident` | Tim → Galahad | Analyze a security incident |
| `fleet-id.results.security.*` | Galahad → Tim | All security results |

### Communications (Percival)

| Subject | Direction | Description |
|---------|-----------|-------------|
| `fleet-id.tasks.comms.email-triage` | Tim → Percival | Scan and prioritize emails |
| `fleet-id.tasks.comms.email-draft` | Tim → Percival | Draft an email response |
| `fleet-id.tasks.comms.notifications` | Tim → Percival | Check notification channels |
| `fleet-id.results.comms.*` | Percival → Tim | All comms results |

### Intelligence (Gawain)

| Subject | Direction | Description |
|---------|-----------|-------------|
| `fleet-id.tasks.intel.weather` | Tim → Gawain | Weather report |
| `fleet-id.tasks.intel.news` | Tim → Gawain | News summary |
| `fleet-id.tasks.intel.market` | Tim → Gawain | Market/financial data |
| `fleet-id.tasks.intel.research` | Tim → Gawain | General research task |
| `fleet-id.results.intel.*` | Gawain → Tim | All intel results |

### Observability (Tristan)

| Subject | Direction | Description |
|---------|-----------|-------------|
| `fleet-id.tasks.observability.health` | Tim → Tristan | Cluster health check |
| `fleet-id.tasks.observability.alert` | Tim → Tristan | Investigate an alert |
| `fleet-id.tasks.observability.capacity` | Tim → Tristan | Capacity planning report |
| `fleet-id.results.observability.*` | Tristan → Tim | All observability results |

### Home Automation (Lancelot)

| Subject | Direction | Description |
|---------|-----------|-------------|
| `fleet-id.tasks.home.scene` | Tim → Lancelot | Activate a scene/routine |
| `fleet-id.tasks.home.status` | Tim → Lancelot | Home status report |
| `fleet-id.tasks.home.automation` | Tim → Lancelot | Manage automations |
| `fleet-id.results.home.*` | Lancelot → Tim | All home results |

## System Events

| Subject | Description |
|---------|-------------|
| `fleet-id.events.task.claimed` | A knight claimed a task |
| `fleet-id.events.task.progress` | Task progress update |
| `fleet-id.events.system.knight-online` | A knight pod started |
| `fleet-id.events.system.knight-offline` | A knight pod stopped |
| `fleet-id.events.system.error` | System-level errors |

## Broadcast Subjects

| Subject | Description |
|---------|-------------|
| `roundtable.broadcast.config-update` | Configuration change notification |
| `roundtable.broadcast.maintenance` | Maintenance mode toggle |
| `roundtable.broadcast.alert` | Urgent alert to all agents |

## JetStream Stream Configuration

### ROUNDTABLE_TASKS

```yaml
name: ROUNDTABLE_TASKS
subjects:
  - "*.tasks.>"
retention: workqueue      # Each message consumed once
maxAge: 86400000000000    # 24h TTL (nanoseconds)
storage: file
replicas: 1               # Single node for homelab
maxMsgSize: 1048576       # 1MB max message
discard: old
```

### ROUNDTABLE_RESULTS

```yaml
name: ROUNDTABLE_RESULTS
subjects:
  - "*.results.>"
retention: limits
maxAge: 604800000000000   # 7 day retention
storage: file
replicas: 1
maxMsgSize: 4194304       # 4MB (results can be larger)
discard: old
```

### ROUNDTABLE_EVENTS

```yaml
name: ROUNDTABLE_EVENTS
subjects:
  - "roundtable.events.>"
retention: limits
maxAge: 2592000000000000  # 30 day retention (audit trail)
storage: file
replicas: 1
maxMsgSize: 1048576
discard: old
```

### ROUNDTABLE_HEARTBEAT

```yaml
name: ROUNDTABLE_HEARTBEAT
subjects:
  - "*.heartbeat.>"
retention: limits
maxAge: 3600000000000     # 1 hour (only recent heartbeats matter)
maxMsgsPerSubject: 5      # Keep last 5 per agent
storage: memory           # No need for persistence
replicas: 1
discard: old
```

## Consumer Configuration

Each knight gets a durable pull consumer:

```yaml
# Example: Galahad's consumer
name: galahad
durableName: galahad
filterSubject: "fleet-id.tasks.security.>"
ackPolicy: explicit
ackWait: 300000000000      # 5 minute ack timeout
maxDeliver: 3              # Retry up to 3 times
deliverPolicy: all
```

## Wildcard Subscriptions

A lead agent subscribes broadly within its fleet:
- `<fleet-id>.results.>` — All results from its knights
- `<fleet-id>.heartbeat.>` — All heartbeats from its knights
- `<fleet-id>.events.>` — Fleet system events
- `roundtable.peer.<fleet-id>` — Incoming peer messages

Knights subscribe narrowly to their domain within their fleet:
- `<fleet-id>.tasks.security.>` (Galahad)
- `<fleet-id>.tasks.comms.>` (Percival)
- etc.

## Adding a New Domain

1. Choose a domain name (e.g., `finance`)
2. Define task subjects: `fleet-id.tasks.finance.<action>`
3. Results auto-route to: `fleet-id.results.finance.<task-id>`
4. Add domain to the knight's consumer filter
5. No infrastructure changes needed — NATS subjects are dynamic
