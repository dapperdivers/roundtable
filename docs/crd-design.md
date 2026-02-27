# Round Table Operator — CRD Design Document

## 1. High-Level Architecture

```
┌─────────────────────────────────────────────────────┐
│                    RoundTable                        │
│  (fleet-level config, policies, NATS infrastructure) │
│                                                      │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐            │
│  │ Knight   │ │ Knight   │ │ Knight   │  ...        │
│  │ (gawain) │ │ (mordred)│ │ (lancelot│            │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘            │
│       │             │             │                   │
│  ┌────┴─────────────┴─────────────┴──────┐          │
│  │         NATS JetStream                 │          │
│  │  {prefix}.tasks.{domain}.{knight}      │          │
│  │  {prefix}.results.{domain}.{knight}    │          │
│  └───────────────────────────────────────┘          │
│                                                      │
│  ┌──────────────┐    ┌──────────────────┐           │
│  │    Chain      │    │     Mission       │           │
│  │  (pipelines)  │    │ (ephemeral teams) │           │
│  │  step→step    │    │ knights + chains  │           │
│  └──────────────┘    └──────────────────┘           │
└─────────────────────────────────────────────────────┘
```

### Resource Relationships

- **RoundTable** → owns many **Knights** (via label selector)
- **RoundTable** → provides NATS config and policies inherited by Knights
- **Chain** → references Knights by name, orchestrates task pipelines
- **Mission** → references Knights (existing or ephemeral) and Chains
- **Mission** → creates ephemeral Knights and temporary NATS subjects
- **Chain** can optionally reference a **RoundTable** for NATS config

### Ownership Hierarchy

```
RoundTable (optional, top-level)
├── Knight (core unit, can exist independently)
├── Chain (references knights, publishes to NATS)
└── Mission (ephemeral grouping)
    ├── ephemeral Knights (owned, cleaned up)
    └── Chain executions (scoped to mission)
```

## 2. Go Type Definitions

All types are defined in `api/v1alpha1/` following the patterns established by `knight_types.go`:
- Apache 2.0 license header
- kubebuilder markers for validation, defaults, and print columns
- Phase enums with `+kubebuilder:validation:Enum`
- Standard status pattern: phase, observedGeneration, conditions
- `omitzero` on ObjectMeta and Status, `omitempty` on optional fields

### Files

| CRD | File | Spec Type | Status Type |
|-----|------|-----------|-------------|
| Knight | `knight_types.go` | `KnightSpec` | `KnightStatus` |
| Chain | `chain_types.go` | `ChainSpec` | `ChainStatus` |
| Mission | `mission_types.go` | `MissionSpec` | `MissionStatus` |
| RoundTable | `roundtable_types.go` | `RoundTableSpec` | `RoundTableStatus` |

See the actual Go files for complete type definitions. Key design decisions:

**Chain:** Steps use `dependsOn` for DAG-style dependencies rather than simple ordering. This enables parallel fan-out (multiple steps with no dependencies run concurrently) and fan-in (step depends on multiple prior steps). Step outputs are accessible via Go templates in downstream step tasks.

**Mission:** Knights can be `ephemeral: true` with an inline `ephemeralSpec` (reusing `KnightSpec`), allowing missions to spin up purpose-built agents. The mission lifecycle (Assembling → Briefing → Active → Succeeded/Failed → CleaningUp) maps to real-world round table semantics.

**RoundTable:** Uses a label selector (`knightSelector`) rather than explicit knight lists, following the Kubernetes pattern (like Deployments select Pods). Provides fleet-wide defaults that individual knight specs can override, plus cost budgets and concurrency policies.

## 3. Controller Behavior

### Chain Controller

**Reconciliation Loop:**

1. **Validate** — Ensure all `knightRef` values resolve to existing Knights. Validate DAG has no cycles.
2. **Schedule Check** — If `schedule` is set and it's time, create a new run (reset step statuses, set phase=Running).
3. **Step Execution** — For each step in `Pending` phase:
   - Check if all `dependsOn` steps are `Succeeded` (or `Failed` with `continueOnFailure`)
   - If ready, publish task to NATS: `{prefix}.tasks.{knight-domain}.{knight-name}` with chain context
   - Set step phase to `Running`
4. **Monitor** — Watch for results on `{prefix}.results.chain.{chain-name}.{step-name}`
   - On success: set step `Succeeded`, store output
   - On failure: retry per policy, then set `Failed`
   - On timeout: set `Failed`
5. **Complete** — When all steps are terminal, set chain phase to `Succeeded` or `Failed`

**NATS Subjects:**
- Task publish: `{prefix}.tasks.{domain}.{knight}` (reuses existing knight subjects)
- Result subscribe: `{prefix}.results.chain.{chain-name}.{step-name}`
- Chain events: `{prefix}.chains.{chain-name}.events`

**Created Resources:**
- ConfigMap per run: stores step outputs for template rendering
- No Deployments (uses existing Knights)

### Mission Controller

**Reconciliation Loop:**

1. **Assembling** — Create ephemeral Knight CRs (owned by Mission). Wait for all knights to reach Ready phase.
2. **Briefing** — Publish briefing message to `mission-{name}.briefing` NATS subject. Configure additional NATS consumers on participating knights for mission-scoped subjects.
3. **Active** — Execute setup chains, then active chains. Monitor for objective completion or timeout.
4. **Complete** — Set `Succeeded` or `Failed`. Execute teardown chains.
5. **CleaningUp** — Delete ephemeral Knights, remove mission NATS consumers, clean up ConfigMaps.
6. **TTL Expiry** — After TTL, delete the Mission CR itself (if `cleanupPolicy=Delete`).

**NATS Subjects:**
- Mission briefing: `mission-{name}.briefing`
- Mission tasks: `mission-{name}.tasks.{domain}.{knight}`
- Mission results: `mission-{name}.results.{domain}.{knight}`
- Mission events: `mission-{name}.events`

**Created Resources:**
- Ephemeral Knight CRs (ownerRef → Mission)
- NATS consumers for mission-scoped subjects
- ConfigMap with mission context

### RoundTable Controller

**Reconciliation Loop:**

1. **NATS Setup** — If `createStreams=true`, ensure JetStream streams exist with correct subjects and retention policy.
2. **Knight Discovery** — List Knights matching `knightSelector`. Update status with knight summaries.
3. **Defaults Propagation** — For Knights that don't specify certain fields, the controller does NOT mutate Knight specs. Instead, the Knight controller checks for a parent RoundTable and inherits defaults at reconcile time.
4. **Policy Enforcement:**
   - Count total concurrent tasks across knights. If exceeding `maxConcurrentTasks`, pause NATS consumers on lowest-priority knights.
   - Aggregate costs. If exceeding `costBudgetUSD`, suspend all knights (set annotation `roundtable.ai.roundtable.io/budget-suspended=true`).
   - Check cost reset schedule, reset counter when due.
5. **Health Aggregation** — Compute phase: Ready (all knights ready), Degraded (some not ready), Suspended, OverBudget.
6. **Mission Counting** — Count active Missions referencing this table.

**NATS Subjects:**
- Manages streams: `{subjectPrefix}_tasks` and `{subjectPrefix}_results`
- Fleet events: `{subjectPrefix}.fleet.events`

**Created Resources:**
- JetStream streams (if `createStreams=true`)
- No direct Knight ownership (uses selector, like a Service)

## 4. NATS Subject Mapping

### Current State (Implicit fleet-a)

```
fleet-a.tasks.{domain}.{knight}     — task delivery
fleet-a.results.{domain}.{knight}   — result delivery
```

### With RoundTable

```
{rt.nats.subjectPrefix}.tasks.{domain}.{knight}
{rt.nats.subjectPrefix}.results.{domain}.{knight}
{rt.nats.subjectPrefix}.fleet.events
```

### Chain Subjects

```
{prefix}.tasks.{domain}.{knight}          — reuses knight subjects
{prefix}.results.chain.{chain}.{step}     — step results
{prefix}.chains.{chain}.events            — chain lifecycle events
```

### Mission Subjects

```
mission-{name}.briefing                      — mission briefing
mission-{name}.tasks.{domain}.{knight}       — mission-scoped tasks
mission-{name}.results.{domain}.{knight}     — mission-scoped results
mission-{name}.events                        — mission lifecycle events
```

## 5. Example YAML

### RoundTable

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: RoundTable
metadata:
  name: fleet-a
  namespace: ai
spec:
  description: "Primary AI agent fleet for infrastructure and security operations"
  nats:
    url: "nats://nats.database.svc:4222"
    subjectPrefix: "fleet-a"
    tasksStream: "fleet_a_tasks"
    resultsStream: "fleet_a_results"
    createStreams: true
    streamRetention: "WorkQueue"
  defaults:
    model: "claude-sonnet-4-20250514"
    image: "ghcr.io/dapperdivers/pi-knight:latest"
    taskTimeout: 120
    concurrency: 2
  policies:
    maxConcurrentTasks: 20
    costBudgetUSD: "50.00"
    costResetSchedule: "0 0 1 * *"
    maxKnights: 15
    maxMissions: 5
  knightSelector:
    matchLabels:
      roundtable.ai.roundtable.io/fleet: fleet-a
  secrets:
    - name: anthropic-api-key
    - name: github-token
  vault:
    claimName: obsidian-vault
    readOnly: true
    writablePaths:
      - "Briefings/"
      - "Roundtable/"
```

### Chain

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Chain
metadata:
  name: security-audit
  namespace: ai
spec:
  description: "Run a security audit: scan, analyze, report"
  roundTableRef: fleet-a
  timeout: 900
  retryPolicy:
    maxRetries: 1
    backoffSeconds: 30
  steps:
    - name: scan
      knightRef: mordred
      task: "Run a comprehensive network scan of the internal cluster network 10.43.0.0/16. Output open ports and services as JSON."
      timeout: 300

    - name: vuln-check
      knightRef: mordred
      task: "Analyze these scan results for known vulnerabilities: {{ .Steps.scan.Output }}"
      dependsOn: ["scan"]
      timeout: 180

    - name: compliance-check
      knightRef: percival
      task: "Check these services against our compliance policy: {{ .Steps.scan.Output }}"
      dependsOn: ["scan"]
      timeout: 180

    - name: report
      knightRef: gawain
      task: |
        Compile a security audit report from:
        - Vulnerability findings: {{ .Steps.vuln-check.Output }}
        - Compliance findings: {{ .Steps.compliance-check.Output }}
        Write the report to the vault at Briefings/security-audit-latest.md
      dependsOn: ["vuln-check", "compliance-check"]
      timeout: 120
```

### Chain with Schedule

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Chain
metadata:
  name: daily-briefing
  namespace: ai
spec:
  description: "Generate daily briefing from all knight activity"
  roundTableRef: fleet-a
  schedule: "0 8 * * *"
  timeout: 300
  steps:
    - name: gather
      knightRef: gawain
      task: "Query the last 24h of results from all knights and summarize key events."
      timeout: 120
    - name: publish
      knightRef: gawain
      task: "Format this into a daily briefing and write to Briefings/daily/{{ now | date \"2006-01-02\" }}.md: {{ .Steps.gather.Output }}"
      dependsOn: ["gather"]
      timeout: 60
```

### Mission

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
metadata:
  name: incident-response-2026-02
  namespace: ai
spec:
  objective: "Investigate and remediate the suspicious outbound traffic detected on node talos-3"
  successCriteria: "Root cause identified, affected services contained, and remediation applied or documented"
  roundTableRef: fleet-a
  ttl: 7200
  timeout: 3600
  briefing: |
    Alert: Anomalous outbound traffic detected from node talos-3 to IP 203.0.113.42.
    Traffic pattern suggests potential data exfiltration. Priority: HIGH.
    Each knight should focus on their specialty and report findings to the mission channel.
  knights:
    - name: mordred
      role: lead
    - name: percival
      role: compliance-reviewer
    - name: incident-analyst
      role: forensics
      ephemeral: true
      ephemeralSpec:
        domain: forensics
        model: "claude-sonnet-4-20250514"
        image: "ghcr.io/dapperdivers/pi-knight:latest"
        skills: ["forensics", "networking", "linux"]
        tools:
          nix: ["tcpdump", "wireshark-cli", "volatility3"]
        nats:
          subjects: ["mission-incident-response-2026-02.tasks.forensics.>"]
          stream: "fleet_a_tasks"
        concurrency: 3
        taskTimeout: 300
  chains:
    - name: security-audit
      phase: Setup
      inputOverride: '{"target": "talos-3", "scope": "outbound-traffic"}'
  cleanupPolicy: Retain
```

## 6. Migration Path

### Current State
- 10 Knights deployed in `ai` namespace
- Implicit "fleet-a" NATS prefix hardcoded in knight configs
- Streams `fleet_a_tasks` / `fleet_a_results` exist
- Gawain handles orchestration via prompt engineering

### Migration Steps

#### Phase 1: Add Labels (Non-breaking)
```bash
# Label all existing knights for fleet-a selection
kubectl label knights -n ai --all roundtable.ai.roundtable.io/fleet=fleet-a
```

#### Phase 2: Create RoundTable CR (Non-breaking)
```bash
kubectl apply -f - <<EOF
apiVersion: ai.roundtable.io/v1alpha1
kind: RoundTable
metadata:
  name: fleet-a
  namespace: ai
spec:
  nats:
    subjectPrefix: "fleet-a"
    tasksStream: "fleet_a_tasks"
    resultsStream: "fleet_a_results"
    createStreams: false  # streams already exist
  knightSelector:
    matchLabels:
      roundtable.ai.roundtable.io/fleet: fleet-a
  defaults:
    model: "claude-sonnet-4-20250514"
    taskTimeout: 120
    concurrency: 2
  policies:
    costBudgetUSD: "50.00"
    costResetSchedule: "0 0 1 * *"
EOF
```

The RoundTable controller will discover existing knights and begin aggregating status. No changes to knight behavior.

#### Phase 3: Convert Gawain Orchestrations to Chains (Incremental)
Identify Gawain's recurring orchestration patterns and express them as Chain CRDs. This can happen one chain at a time — Gawain continues handling anything not yet converted.

Example: If Gawain currently coordinates "scan → analyze → report" via prompt engineering, create the `security-audit` Chain above. Gawain's workload decreases as chains take over.

#### Phase 4: Enable Missions for Ad-Hoc Work
Start using Mission CRDs for incident response and exploratory tasks that currently require manual knight coordination.

#### Phase 5: Cleanup
- Set `createStreams: true` on RoundTable to let the operator manage stream lifecycle
- Remove hardcoded NATS config from knights that can inherit from RoundTable defaults
- Consider creating a second RoundTable for dev/test knights

### Backward Compatibility

- Knights continue to work without a RoundTable (standalone mode)
- Chains reference knights by name, no knight changes needed
- NATS subjects remain identical — same prefix, same structure
- All new CRDs are additive; no existing resources are modified
