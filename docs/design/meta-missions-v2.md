# Meta-Missions v2 — Capability-First Planning

**Status:** Approved Design
**Date:** March 12, 2026
**Authors:** Derek (vision), Tim (architecture), coder-1 (initial research)

## One-Line Summary

A 5-line Mission YAML becomes a fully orchestrated multi-agent workflow — the operator's built-in planner knight reasons about what tools, skills, and chains are needed, then creates everything from scratch.

## Example

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
metadata:
  name: security-audit
spec:
  objective: "Conduct security audit of https://prod.example.com"
  metaMission: true
  roundTableRef: personal
  ttl: 28800
```

That's it. The operator does the rest.

## Architecture

### Built-in Planner Knight
- Permanent knight deployed by the operator chart (like the dashboard)
- Skilled via `roundtable-arsenal/operator/planner.md`
- Communicates over NATS like every other knight
- Operator dispatches planning tasks when `metaMission: true`

### Capability-First Reasoning
The planner doesn't pick from a menu — it **designs from scratch**:
- **Nix packages**: "I need nmap" → knight bootstraps with `nix profile install nixpkgs#nmap`
- **Skills**: "I need a recon methodology" → planner writes the skill markdown inline
- **Chains**: "Research first, then exploit, then report" → planner designs the DAG

No predefined catalogs. No template shopping. Pure reasoning about capabilities.

### Flow
```
1. User creates Mission with metaMission: true
2. Operator dispatches objective to planner knight via NATS
3. Planner reasons: "I need 4 knights with these tools and skills..."
4. Planner outputs structured JSON plan
5. Operator validates (DAG, limits, schema)
6. Operator creates ephemeral knights (nix packages + generated skills)
7. Operator creates Chain CRs from plan
8. Normal mission lifecycle: Assembling → Briefing → Active → Succeeded
9. Teardown: ephemeral knights + chains cleaned up
```

### What's Baked In
Only three things:
1. The planner knight — ships with the operator chart, always running
2. The planner skill — `roundtable-arsenal/operator/planner.md`
3. The output JSON schema — so the operator can parse and validate

Everything else is generated per-mission.

## Planner Output Schema

```json
{
  "planVersion": "v1alpha1",
  "metadata": {
    "reasoning": "Explain planning strategy",
    "estimatedDuration": "2h"
  },
  "chains": [
    {
      "name": "reconnaissance",
      "description": "Map the attack surface",
      "steps": [
        {
          "name": "port_scan",
          "knightRef": "scanner",
          "task": "Scan all ports on target...",
          "timeout": 300,
          "dependsOn": []
        }
      ],
      "timeout": 600
    }
  ],
  "knights": [
    {
      "name": "scanner",
      "domain": "security",
      "role": "reconnaissance",
      "model": "claude-sonnet-4-20250514",
      "nixPackages": ["nmap", "masscan", "curl", "jq"],
      "skills": [
        {
          "name": "recon-methodology",
          "content": "# Reconnaissance Knight\n\nYou scan targets..."
        }
      ],
      "concurrency": 2,
      "taskTimeout": 300
    }
  ]
}
```

## CRD Changes Needed

### MissionSpec
- `metaMission bool` — triggers built-in planner

### KnightSpec
- `nixPackages []string` — nix packages installed at bootstrap
- `generatedSkills []GeneratedSkill` — inline skill markdown

### New Phase
- `MissionPhasePlanning` — between Provisioning and Assembling

## Validation Strategy
1. Schema — JSON parses, required fields present
2. DAG — no circular dependencies
3. References — all knightRefs resolve
4. Limits — chain/knight/step counts within bounds
5. Safety — no security-sensitive field overrides

## Implementation Checklist

### Operator
- [ ] `metaMission` on MissionSpec
- [ ] `nixPackages` + `generatedSkills` on KnightSpec
- [ ] `Planning` phase + `reconcilePlanning()`
- [ ] Plan parser + validator
- [ ] Dynamic Chain CR creation
- [ ] Planner knight Deployment in chart
- [ ] Planner Knight CR in chart

### Arsenal
- [x] `operator/planner.md` — planner skill

### Pi-Knight
- [ ] Support `nixPackages` in bootstrap
- [ ] Support `generatedSkills` mounting

### Dashboard
- [ ] Planning phase in timeline
- [ ] Planner reasoning display

## What Makes This Novel

No other K8s operator reasons about its own agent topology from scratch.
The planner is an architect, not a shopper.
