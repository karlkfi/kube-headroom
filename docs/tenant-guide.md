# Headroom tenant guide

For application teams whose namespaces are managed by Headroom. If you operate
the controller, see the [runbook](runbook.md); to decide whether your workload
should be managed at all, see the [applicability matrix](applicability.md).

## The contract

> **Your CPU request is guaranteed.** Your CPU *limit* floats between your
> request and your request plus your proportional share of the node's unbooked
> CPU. If you need more sustained CPU, request more. Your limit shrinking is not
> an incident — it means the node got busier, and you still have everything you
> requested.

That is the whole model. On an empty node your ceiling approaches the node's
capacity, so nothing throttles pointlessly. As the node fills with other pods'
*requests*, your ceiling shrinks toward your request — which is exactly the share
the kernel's `cpu.weight` fair-sharing would give you under contention anyway, so
behavior is coherent at both extremes.

Headroom only ever changes `limits.cpu`. It never touches your requests, your
memory, or anything else, and it never changes where your pods are scheduled.

## What you'll see

- Your pod's `spec...resources.limits.cpu` moves over time — `kubectl get pod`
  shows the actually-enforced ceiling (the spec is the source of truth; there is
  no hidden node-agent state).
- A status annotation explaining the current ceiling:

  ```sh
  kubectl get pod <pod> \
    -o jsonpath='{.metadata.annotations.kube-headroom\.dev/status}' | jq
  # {"factor":"2.00","slack":"8000m","managedRequests":"8000m","nodePods":14,...}
  ```

- A `CPULimitAdjusted` event on each change, e.g.
  `1500m → 3000m (node factor 2.00, slack 8/16 cores)`.

**A shrinking limit is normal.** It is the node getting busier, not an outage.

## Opting in and out

Enrollment is per **namespace** and is normally done by your platform team:

```sh
kubectl label ns <your-namespace> kube-headroom.dev/mode=managed
```

To exclude a single pod inside a managed namespace, set the label on the pod
template (fail-closed enum keyword — `unmanaged`, not a boolean):

```yaml
metadata:
  labels:
    kube-headroom.dev/mode: unmanaged
```

Pods that are Guaranteed QoS, BestEffort, or have no CPU request are never
managed regardless of labels (see [applicability](applicability.md)).

## "I'm being throttled / I need more CPU"

If you are throttled, the node is booked and your ceiling has collapsed toward
your request. The fix is **self-service: raise your CPU request.** Raising your
request buys three things at once — more guaranteed schedulable capacity, a
larger CFS weight under contention, *and* a larger share of node slack (a higher
dynamic ceiling). There is no way to get sustained large CPU by requesting little.

Deciding *what* to raise the request to is a usage-histogram question that
belongs to **VPA's recommender**, not Headroom. Run VPA in recommendation mode
(`updateMode: "Off"`) and read `status.recommendation`, or let it auto-apply with
the recipe below.

## Cap your own ceiling

Some workloads genuinely shouldn't exceed a set parallelism (e.g. a
`GOMAXPROCS`-pinned service). Cap the ceiling per pod with an annotation:

```yaml
metadata:
  annotations:
    kube-headroom.dev/max-cpu: "4"   # never raise this pod's limit above 4 cores
```

Headroom will never set the limit above `min(nodeAllocatable, your max-cpu)`.

## Runtimes that read CPU quota once at startup

**This is the most common surprise.** Some runtimes size their thread pools from
the CPU limit *at container start* and never re-read it:

- **JVM ergonomics** (`-XX:ActiveProcessorCount` derived from the cgroup quota).
- **Go** binaries that pin `GOMAXPROCS` from the limit at boot (e.g. via
  `automaxprocs`).

Headroom raises the cgroup ceiling live, but a boot-time-sized thread pool won't
grow to use it — so the extra headroom is available to the kernel but not to your
runtime's parallelism. Options:

- Set `GOMAXPROCS` / thread counts **explicitly** to your intended steady-state
  parallelism rather than deriving them from the (now-floating) limit.
- Use a runtime knob that **re-reads** the quota periodically, if available.
- Accept birth-limit sizing: the admission webhook (when enabled) seeds a
  generous limit at CREATE time, which is a good value for boot-time-sized
  runtimes even though later live raises won't be picked up.

Either way, note that a raised *limit* does not change `GOMAXPROCS` for you —
runtime tuning is yours to own.

## Using Headroom together with VPA

<a id="vpa"></a>
Headroom (owns `limits.cpu`) and VPA (owns **requests**) compose cleanly —
**verified in the Phase 0 spike** (Q2d): the two controllers patch disjoint
resource leaves via the resize subresource with distinct field managers, ownership
is granular per leaf, and steady state is conflict-free (no field flapping, zero
restarts). The one requirement is that VPA must manage **requests only**:

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: my-app
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-app
  updatePolicy:
    updateMode: "InPlaceOrRecreate"     # in-place, no restart when possible
  resourcePolicy:
    containerPolicies:
      - containerName: "*"
        controlledValues: RequestsOnly  # <-- REQUIRED: VPA sets requests, not limits
```

- `controlledValues: RequestsOnly` — VPA moves `requests.cpu`; Headroom owns
  `limits.cpu`. VPA's request changes are just inputs to Headroom (a
  request-change event re-triggers the node recompute).
- The **first** write by each controller performs a one-time ownership transfer
  of its leaf (from `kube-controller-manager`); this is expected and does not
  cause churn afterward.

> **Do not** run VPA in its default `controlledValues: RequestsAndLimits` mode on
> managed pods — it scales limits proportionally with requests and fights
> Headroom directly. Those pods are refused by the eligibility check; either
> switch the VPA to `RequestsOnly` or opt the pods out with
> `kube-headroom.dev/mode: unmanaged`.

## A note on HPA

CPU-utilization HPA computes against **requests**, so it is structurally
unaffected. One behavioral change to expect: throttling used to silently cap the
HPA signal (a pod pinned at its limit reports bounded usage). With Headroom,
unthrottled pods reveal true demand, so utilization can exceed 100% of request
and HPA may scale out earlier. This is correct — demand is no longer masked — but
if you tuned your HPA thresholds around the old capped signal, review your
stabilization windows when you adopt Headroom.
