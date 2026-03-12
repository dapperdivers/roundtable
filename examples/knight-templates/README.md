# Knight Templates Examples

This directory demonstrates the knight templates feature in RoundTable.

## Overview

Knight templates allow you to define reusable knight configurations at the RoundTable level, eliminating the need to duplicate knight specs across multiple missions.

## Benefits

- **DRY Principle**: Define templates once, use everywhere
- **Centralized Management**: Update templates in one place
- **Consistency**: All missions use the same configuration
- **Reduced YAML**: Missions become 90% smaller
- **Template Libraries**: Share templates across your organization

## Template Priority

When resolving a `templateRef`, the controller checks templates in this order:

1. **Mission-level templates** (`mission.spec.knightTemplates`) - highest priority
2. **RoundTable-level templates** (`roundTable.spec.knightTemplates`) - fallback

This allows missions to override RoundTable templates when needed.

## Files

### `roundtable-with-templates.yaml`

A complete RoundTable with 4 knight templates:
- `auditor` - Security auditor with reconnaissance tools
- `pentester` - Penetration tester with exploitation tools
- `reporter` - Report generator with documentation skills
- `infra-analyst` - Infrastructure analyst with cloud tools

### `mission-using-templates.yaml`

A mission that uses RoundTable templates with `templateRef`:
- References 5 knights using templates
- Shows how to override skills and environment variables
- Demonstrates template reuse (2 auditors use same template with different overrides)

## Usage

```bash
# 1. Create the RoundTable with templates
kubectl apply -f roundtable-with-templates.yaml

# 2. Create a mission that uses the templates
kubectl apply -f mission-using-templates.yaml

# 3. Verify ephemeral knights were created from templates
kubectl get knights -l ai.roundtable.io/mission=web-app-pentest

# 4. Check knight specs
kubectl get knight web-app-pentest-lead -o yaml
```

## Template Reference

### Basic Template

```yaml
spec:
  knightTemplates:
    my-template:
      domain: security
      model: claude-sonnet-4-20250514
      skills:
      - security
      - nmap
      image: ghcr.io/dapperdivers/roundtable-knight:v0.6.0
      concurrency: 2
      taskTimeout: 300
      nats:
        subjects: ["fleet.tasks.security.>"]
```

### Using Template in Mission

```yaml
spec:
  roundTableRef: my-roundtable
  knights:
  - name: knight1
    ephemeral: true
    templateRef: my-template  # ← References roundTable.spec.knightTemplates.my-template
```

### Overriding Template Fields

```yaml
spec:
  knights:
  - name: knight1
    ephemeral: true
    templateRef: my-template
    specOverrides:
      model: claude-opus-4-20250514  # Override model
      skills:  # Replace skills entirely
      - security
      - custom-skill
      env:  # Add environment variables
      - name: MY_VAR
        value: "my-value"
      concurrency: 5  # Override concurrency
```

## Template vs Inline Spec

### Before (Inline Spec)

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
spec:
  knights:
  - name: auditor1
    ephemeral: true
    ephemeralSpec:  # ← Inline spec (100+ lines)
      domain: security
      model: claude-sonnet-4-20250514
      skills: [security, nmap, reconnaissance]
      # ... 50 more lines ...
  - name: auditor2
    ephemeral: true
    ephemeralSpec:  # ← Duplicate spec (100+ lines)
      domain: security
      model: claude-sonnet-4-20250514
      skills: [security, nmap, reconnaissance]
      # ... 50 more lines ...
```

### After (Template Reference)

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: RoundTable
spec:
  knightTemplates:
    auditor:  # ← Define once
      domain: security
      model: claude-sonnet-4-20250514
      skills: [security, nmap, reconnaissance]
      # ... 50 more lines ...
---
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
spec:
  roundTableRef: my-roundtable
  knights:
  - name: auditor1
    ephemeral: true
    templateRef: auditor  # ← 2 lines instead of 100
  - name: auditor2
    ephemeral: true
    templateRef: auditor  # ← 2 lines instead of 100
```

**Result**: 90% reduction in mission YAML size!

## Advanced: Mission-Level Override

Missions can override RoundTable templates for special cases:

```yaml
apiVersion: ai.roundtable.io/v1alpha1
kind: Mission
spec:
  roundTableRef: security-ops
  
  # Mission-specific template (overrides RoundTable template)
  knightTemplates:
  - name: auditor  # ← Same name as RoundTable template
    spec:
      domain: security
      model: claude-opus-4-20250514  # ← More powerful model for emergency
      skills: [security, incident-response, forensics]
      concurrency: 5  # ← Higher concurrency
  
  knights:
  - name: emergency-auditor
    ephemeral: true
    templateRef: auditor  # ← Uses mission-level template (higher priority)
```

## Best Practices

1. **Standard Templates**: Define common templates in RoundTable
2. **Specific Overrides**: Use mission-level templates only for special cases
3. **Minimal Overrides**: Use `specOverrides` instead of redefining entire specs
4. **Template Naming**: Use descriptive names (`auditor`, not `template1`)
5. **Documentation**: Document template purpose in RoundTable description

## Troubleshooting

### Error: "template not found"

```
Error: template "my-template" not found in mission.spec.knightTemplates or roundTable.spec.knightTemplates
```

**Solution**: Check that:
1. Template is defined in RoundTable `spec.knightTemplates`
2. Mission has correct `roundTableRef`
3. Template name matches exactly (case-sensitive)

### Knight Uses Wrong Template

**Solution**: Check template priority:
- Mission-level templates override RoundTable-level templates
- Ensure mission doesn't have duplicate template name

## See Also

- [Knight CRD Documentation](../../docs/knight-crd.md)
- [Mission CRD Documentation](../../docs/mission-crd.md)
- [RoundTable CRD Documentation](../../docs/roundtable-crd.md)
