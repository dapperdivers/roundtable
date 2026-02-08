# Knight Template

Base template for creating new Knights of the Round Table.

## Creating a New Knight

1. Copy the `template/` directory:
   ```bash
   cp -r knights/template knights/<knight-name>
   ```

2. Create a `kustomization.yaml` that patches the template:
   ```yaml
   apiVersion: kustomize.config.k8s.io/v1beta1
   kind: Kustomization
   resources:
     - ../template
   patches:
     - target:
         kind: Deployment
       patch: |
         - op: replace
           path: /metadata/name
           value: knight-<name>
   namePrefix: ""
   ```

3. Customize `workspace/SOUL.md` with the knight's personality

4. Update `SUBSCRIBE_TOPICS` env var to match the knight's name

5. Add the knight to the Flux kustomization
