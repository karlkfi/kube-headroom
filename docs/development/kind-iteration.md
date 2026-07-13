# The kind inner loop

Fast iteration on the controller against a real cluster. Relevant once the
controller exists and is running in-cluster (Q4/Q8); for host-run development
`make run` against any context is faster still. The goal here is a
seconds-not-minutes edit → deploy → observe loop without recreating the cluster.

## Reuse the cluster

Creating a kind cluster costs tens of seconds; keep one alive across iterations.
`make setup-test-e2e` creates the dedicated e2e cluster only if it doesn't
already exist, so it is safe to re-run. Only delete and recreate when you've
changed the cluster's own shape — Kubernetes version, feature gates, node count.
A stale controller Deployment is cheap to replace; a fresh cluster is not.

> Use the **dedicated** kind cluster, never a dev or prod kubecontext. Confirm
> with `kubectl config current-context` before applying anything.

## Defeat the image cache with unique tags

kind caches images by tag. If you rebuild and reuse `:latest` (or any fixed
tag), the kubelet may run the *old* image because the tag already resolves —
the single most common "my fix didn't take" trap. Give every build a unique
tag:

```
TAG=dev-$(git rev-parse --short HEAD)-$(date +%s)
docker build -t headroom:$TAG .
kind load docker-image headroom:$TAG --name <cluster>
```

A content-addressable tag (commit SHA plus a counter) guarantees the kubelet
pulls what you just built.

## Roll the new image without redeploying

Once the Deployment exists, don't re-apply manifests for a code change — just
point it at the new tag:

```
kubectl set image deployment/headroom-controller-manager manager=headroom:$TAG -n headroom-system
kubectl rollout status deployment/headroom-controller-manager -n headroom-system
```

`kubectl set image` triggers a rollout in place; `rollout status` blocks until
the new pod is serving. This is the hot path of the loop.

## Debug with targeted pods

To observe policy behavior, schedule small, disposable workloads with explicit
requests rather than reasoning in the abstract:

```
kubectl run slack-probe --image=registry.k8s.io/pause:3.9 \
  --overrides='{"spec":{"containers":[{"name":"p","image":"registry.k8s.io/pause:3.9","resources":{"requests":{"cpu":"500m"}}}]}}' \
  -n <managed-namespace>
```

Then watch the controller act:

```
kubectl get pod slack-probe -n <ns> -o jsonpath='{.spec.containers[0].resources.limits.cpu}{"\n"}'
kubectl describe pod slack-probe -n <ns>          # events + status annotation
kubectl logs deploy/headroom-controller-manager -n headroom-system -f
```

Scale a probe up or down to change node slack and confirm limits move the way
the policy predicts. Delete probes when done — they exist to move slack, not to
stay running.

## The loop, condensed

1. Edit Go.
2. `make test` (catch it before the cluster — see [testing.md](testing.md)).
3. Build with a unique tag, `kind load`.
4. `kubectl set image` + `rollout status`.
5. Poke with a probe pod; read limits, events, logs.
6. Repeat. Recreate the cluster only when its shape changes.
