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
Set it to wherever your operator is installed: `nodewright` for a new install,
`skyhook` for one that predates the rename.

```bash
NS=nodewright
```

Create the Secret without putting the value in the command, so it stays out of your
shell history and the process list. Type or paste a value (any value works for the
demo) when prompted; nothing is echoed:

```bash
printf 'Token: '; read -rs TOKEN; echo
kubectl create secret generic nodewright-example \
  --namespace "$NS" \
  --from-file=token=<(printf %s "$TOKEN")
unset TOKEN
```

Then:

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
EXAMPLE_TOKEN is set
```

The script reports that the variable is set and nothing about its value, not even
its length. Package logs are readable by anyone who can read pods in the operator's
namespace.

## What to know

- **The Secret and the package pod share a namespace**, so create it in the
  operator's namespace (`$NS`), not `default`.
- **If the Secret or key is missing**, Kubernetes cannot start the package's
  containers, so the package never finishes. The reason is in the events from
  `kubectl describe pod -n "$NS" <pod>`.
- **The value is read when the pod starts.** Rotating the Secret does not change a
  package that already completed. Use `kubectl nodewright package rerun` to run it
  again.
- `env` takes standard `EnvVar` entries, so to pass several keys, add one entry per
  key.

## Clean up

```bash
kubectl delete -f examples/secrets/nodewright.yaml
kubectl delete secret nodewright-example --namespace "$NS"
kubectl label node "$NODE" nodewright.nvidia.com/example-
```

## Related

- [Providing Secrets to Packages](../../docs/user-guide/providing-secrets.md)
