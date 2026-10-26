# Example: giving a package a secret

NodeWright does not fetch or inject secrets. It hands a package's `env` straight
to the package pod, so you use the Kubernetes mechanism you already have: put the
secret in a `Secret` and point an environment variable at it with `secretKeyRef`.
The package's scripts then read the variable.

Never put a secret in a package's `configMap`. That map is copied verbatim into a
ConfigMap, which is readable by anyone with ConfigMap access, and it lands in the
NodeWright resource itself.

## Run it

Package pods run in the **operator's namespace**, so the Secret has to be there.
That is `nodewright` for a new install (older installs use `skyhook`):

```bash
kubectl create secret generic nodewright-example \
  --namespace nodewright \
  --from-literal=token=REPLACE_ME
```

Replace `REPLACE_ME`, though any value works for the demo. Then:

```bash
NODE=<node-name>
kubectl label node "$NODE" nodewright.nvidia.com/example=secrets
kubectl apply -f examples/secrets/nodewright.yaml
kubectl get nodewright secrets-demo -w
```

Read what the script saw:

```bash
kubectl nodewright package logs use-secret --nodewright secrets-demo --stage apply
```

```text
received a token of 11 characters
```

The script reports the length, not the value. Package logs are readable by anyone
who can read pods in the operator's namespace.

## What to know

- **The Secret and the package pod share a namespace**, so create it in the
  operator's namespace, not `default`.
- **If the Secret or key is missing**, Kubernetes cannot start the package's
  containers, so the package never finishes. The reason is in the events from
  `kubectl describe pod -n nodewright <pod>`.
- **The value is read when the pod starts.** Rotating the Secret does not change a
  package that already completed. Use `kubectl nodewright package rerun` to run it
  again.
- `env` takes standard `EnvVar` entries, so to pass several keys, add one entry per
  key.

## Clean up

```bash
kubectl delete -f examples/secrets/nodewright.yaml
kubectl delete secret nodewright-example --namespace nodewright
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

## Related

- [Providing Secrets to Packages](../../docs/user-guide/providing-secrets.md)
