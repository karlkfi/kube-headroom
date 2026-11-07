# Headroom operator runbook

Operating Headroom on a cluster: preflight, rollout, day-2 triage, and failure
handling. Audience: the platform/cluster operators who install and own the
controller. App teams want the [tenant guide](tenant-guide.md) instead; whether
Headroom fits a given workload is the [applicability matrix](applicability.md).
Background for every "why" here is the [design doc](design.md).

Headroom sets container **`limits.cpu`** as a function of node slack, via the
in-place pod resize subresource. It never touches requests, memory, or any other
field. It is **opt-in per namespace** and ships **`dryRun: true`** by default.

---

## Preflight

<a id="preflight"></a>
Check these before enrolling any namespace. Most are one-time cluster
properties; the ResourceQuota one is the sharp edge.

- **Kubernetes ≥ 1.35 on managed nodes.** In-place pod resize is GA at 1.35
  (the `InPlacePodVerticalScaling` gate is on by default). Older nodes cannot
  actuate a resize.
- **No static CPU/Memory Manager policy on managed nodes.** Resize is prohibited
  under static manager policies; these nodes are excluded structurally (and the
  controller handles a stray `Infeasible` defensively). NUMA-pinned training
  pools are the usual case — leave them unmanaged.
- **No Windows nodes in the managed set.** In-place resize is unsupported there.
- **ResourceQuota: `requests.cpu` only — never `limits.cpu`.** A `limits.cpu`
  quota in a managed namespace makes Headroom's raises consume tenant quota, and
  an **over-budget raise fails admission with a `403 Forbidden`** (verified,
  Phase 0 spike Q2c). Quota is delta-accounted on resize, so a within-budget
  raise silently eats quota too. Run the preflight script on the namespaces you
  are about to enroll (`kubectl` and `jq` on the path, current kube context):

  ```sh
  scripts/preflight-namespaces.sh team-a team-b
  ```

  With no arguments it checks every namespace already labeled
  `kube-headroom.dev/mode=managed`; it does not evaluate a `HeadroomConfig`
  `namespaceSelector`, so name those namespaces yourself. It exits 1 when a namespace has a ResourceQuota
  with `limits.cpu` set, and prints which one. That namespace must drop the
  `limits.cpu` hard limit (keep `requests.cpu`) before enrollment, or it will see
  resize 403s. It exits 2 when it could not check, such as a namespace that does not
  exist or an unreachable cluster, so a script can tell "clean" from "did not run".
  It also prints a `WARN` line for each LimitRange rule below; those exit 0 unless
  you pass `--strict`. It reads the cluster as it is now: re-run it after a quota or
  LimitRange changes, since nothing stops a tenant adding `limits.cpu` later.
- **LimitRange `max` caps what Headroom can set.** A CPU `max` or
  `maxLimitRequestRatio`, per container or per pod, clamps the achievable ceiling.
  Raises just stop at the cap, but that means the "empty-node
  personality" is bounded; document it for tenants who wonder why their limit
  plateaus. A pod's limit is split across its containers by request, so a
  per-container `max` stops the pod when the container with the largest request
  reaches it. A fractional per-container ratio stops a little short of ratio ×
  total, and well short when a container has a very small request: 1000m + 10m
  at 1.05 gets no burst. A whole-number ratio has no such gap. A new or changed
  LimitRange applies within seconds, or when the backoff ends for a pod it refused.
  A `max` below a running pod's request leaves no limit the apiserver will admit,
  which surfaces as [`limitrange-denied`](#resize-outcomes). The script warns on a
  CPU `max`, a `maxLimitRequestRatio` and a `default`. A per-container `max` with no
  `default` also warns about `default`, because the apiserver copies `max` into `default` and
  `defaultRequest` (measured on 1.35: `max: 2` came back as `default: 2` and
  `defaultRequest: 2`), so the [side effects of a CPU default](#limitrange-default)
  apply to it too.
- **VPA, if present, should be `controlledValues: RequestsOnly`** on pods you want
  managed. Default-mode VPA (`RequestsAndLimits`) scales limits, so Headroom leaves
  those pods unmanaged and emits one `VPAManagesLimit` warning per pod. They keep
  the limit they have. `updateMode: Off` writes nothing and stays managed. See the
  [tenant guide](tenant-guide.md#vpa) for the recipe. Headroom notices a VPA change
  within about 30 seconds, and it reads VPAs with `list` alone, so the chart's RBAC
  works whether or not the CRD is installed. The default 128Mi limit held 20,000 VPAs
  and 3,000 small pending pods on kind, but peaked as high as 122MiB, so raise
  `resources.limits.memory` before a cluster reaches those counts.
- **Containers without a CPU request are never capped.** Headroom manages a pod
  whose *total* request is positive, and caps only the containers that carry a
  request. A request-less sidecar in a managed pod runs unbounded. Decide whether
  to [require CPU requests at admission](#require-cpu-requests) before enrolling
  namespaces that run CI.

---

## Install and roll out

The rollout is deliberately staged: **dry-run → observe → enforce**, one
namespace at a time.

1. **Install the CRD, then the operator, with Helm.** Headroom ships two OCI
   charts on ghcr: `kube-headroom-crds` (the CRD, cluster-wide, installed once)
   and `kube-headroom` (the namespaced operator). The CRD chart carries
   `helm.sh/resource-policy: keep`, so a later `helm uninstall` never drops live
   `HeadroomConfig`s. The webhook serving cert comes from cert-manager by
   default; without cert-manager, supply your own (`certmanager.enable=false`,
   `webhook.certSecretName`, `webhook.caBundle`).

   ```sh
   # CRD chart — cluster-wide, on its own lifecycle:
   helm upgrade --install kube-headroom-crds \
     oci://ghcr.io/karlkfi/charts/kube-headroom-crds

   # Operator chart — into its namespace (label it for restricted PSA first if
   # your cluster enforces Pod Security):
   helm upgrade --install kube-headroom \
     oci://ghcr.io/karlkfi/charts/kube-headroom \
     --namespace kube-headroom-system --create-namespace
   ```

   From a checkout, `make install` (CRD chart) then `make deploy IMG=…`
   (operator) do the same against your current kubecontext. Tune the operator
   through `values.yaml` — `image.repository`/`tag`, `replicas`, `resources`,
   the `webhook`/`certmanager`/`prometheus`/`networkPolicy` toggles — instead of
   editing manifests. Render locally to preview: `helm template kube-headroom
   oci://ghcr.io/karlkfi/charts/kube-headroom`. Already running an older
   kustomize-deployed build? See [migrating to Helm](helm-migration.md).

   Then create the `HeadroomConfig` singleton (named `cluster`, defaults to
   `dryRun: true`) — either from the sample, or by setting
   `headroomConfig.create=true` on the operator chart:

   ```sh
   kubectl apply -k config/samples   # creates HeadroomConfig/cluster (dryRun: true)
   kubectl get hcfg cluster
   ```

2. **Enroll one namespace and watch dry-run output.** Dry-run computes targets,
   writes the status annotation, and emits metrics — but issues **no** resize
   patches. This is the validation harness:

   ```sh
   kubectl label ns team-a kube-headroom.dev/mode=managed
   # the namespace's pods recompute within seconds; then inspect one:
   kubectl get pod -n team-a <pod> \
     -o jsonpath='{.metadata.annotations.kube-headroom\.dev/status}' | jq
   ```

   The annotation shows the `factor`, `slack`, `managedRequests`, and the
   `computedAt` timestamp the controller *would* have applied. Sanity-check that
   targets sit between each pod's request and its cap, and that
   `headroom_resizes_total{result="dry-run"}` climbs while real resize counters
   stay flat.

3. **Flip to enforcing.** When the dry-run targets look right, set `dryRun:
   false` — globally, since it is a single cluster config:

   ```sh
   kubectl patch hcfg cluster --type merge -p '{"spec":{"dryRun":false}}'
   ```

   Now watch `CPULimitAdjusted` events and `headroom_resizes_total{result="applied"}`.
   Every node starts recomputing within seconds and patches at its
   `perNodePatchesPerSecond` rate, so a node with more pods than that takes longer
   to finish. A managed pod alone on a node should climb toward allocatable; scheduling a
   neighbor should shrink it within a few seconds.

4. **Add namespaces incrementally.** Repeat step 2 per namespace. There is no
   need to re-toggle dry-run; enrollment is per-namespace via the label.

**Rollback** is always available and always safe (§ [Failure modes](#failure-modes)):
set `dryRun: true` again (stops all patching; limits freeze at their last
values), or remove the namespace label (the namespace's pods stop being managed;
their current limits stay put — Headroom never *removes* a limit).

---

## Requiring CPU requests (optional)

<a id="require-cpu-requests"></a>
Headroom's ceiling covers only containers that request CPU. A container with no
request in an otherwise-managed pod gets no limit at all, and nothing reports it:
the pod shows as managed, its other containers get their limits, and the
request-less one runs unbounded (design §6.3).

The common way to hit this is a sidecar that runs the pod's real work. A CI pod
whose job container requests 16 CPUs and whose Docker-in-Docker sidecar requests
nothing gets a ceiling on the job container, but the build runs in containers
the sidecar starts, which sit under the sidecar's cgroup. The build is uncapped.
Runtimes that size thread pools from the host CPU count make it worse: on a
256-CPU node a numeric library sizes its thread pool from all 256 CPUs it sees,
inside that unbounded cgroup.

The controller does not reject or rewrite such pods; that is policy, and it
belongs to the platform. Headroom ships an **opt-in** `ValidatingAdmissionPolicy`
that rejects, at CREATE, any pod in a managed namespace with an app or sidecar
(restartable-init) container whose CPU request is missing or zero, or a pod with
pod-level resources and no CPU ceiling:

- [`config/samples/admission-policy/require-cpu-requests.yaml`](https://github.com/karlkfi/kube-headroom/blob/main/config/samples/admission-policy/require-cpu-requests.yaml)

It needs Kubernetes ≥ 1.30 (`ValidatingAdmissionPolicy` is GA there), which
Headroom's own 1.35 floor already satisfies. The Helm charts do not install it.

1. **Match your enrollment.** The binding selects namespaces labeled
   `kube-headroom.dev/mode: managed` and exempts pods labeled
   `kube-headroom.dev/mode: unmanaged`. If your `HeadroomConfig` sets
   `spec.namespaceSelector`, copy that selector into the binding's
   `matchResources.namespaceSelector`.
2. **Trial it with `Warn`.** Set the binding's `validationActions` to
   `[Warn, Audit]` and apply it:

   ```sh
   kubectl apply -k config/samples/admission-policy
   ```

   Every violating pod is still admitted, but `kubectl` prints the policy's
   message and the audit log records it, which gives you the list of workloads to
   fix.
3. **Enforce with `Deny`.** Restore `validationActions: [Deny]` once the warnings
   stop. A rejected pod created by a controller shows up as a `FailedCreate`
   event on its ReplicaSet, Job, or StatefulSet rather than as an error to the
   person who applied the Deployment, so tell tenants where to look.

The policy also rejects BestEffort pods in managed namespaces, since they have no
request on any container and Headroom cannot cap them either. Regular
(run-to-completion) init containers are not checked: Headroom never resizes them.

It also rejects a pod that sets pod-level `spec.resources` unless the pod has a
CPU ceiling of its own: a pod-level CPU limit, or a CPU limit on every app and
sidecar container. Headroom never caps these pods (design §6.3), so without the
policy they can run unbounded. An empty `resources: {}` counts too: Kubernetes 1.35
and 1.36.1 class that pod BestEffort whatever its containers request, so Headroom
leaves it uncapped, and deleting the block puts the pod back under Headroom.
On 1.37 this rejects a pod Headroom would have managed, until the block is deleted.

The policy keeps its name, so re-running the `kubectl apply -k` above updates an
existing install in place, and this rule does not apply until you do. Under the
sample's `Deny` it enforces at once, so a Deployment with this shape fails its next
rollout with `FailedCreate`; trial it with `[Warn, Audit]` first, as in step 2.

**No mutating variant ships.** A `MutatingAdmissionPolicy` could inject a default
request instead of rejecting, but it is GA only from Kubernetes 1.36 (beta and
off by default in 1.35, Headroom's floor), and a small default is the wrong fix
anyway: a 100m default request on a dind sidecar makes the sidecar managed with a
ceiling of at most 1 CPU (100m × the default `maxMultiplier` of 10), so the build
that ran there is throttled instead of unbounded. The request has to be sized by
the team that knows where the work runs, which is what rejection forces.

### A LimitRange CPU default: an option, with warnings

<a id="limitrange-default"></a>
A namespace LimitRange with `default.cpu` gives a pod with pod-level resources a
fixed limit where it would otherwise have none, and Headroom never adjusts it. It
applies to every pod in the namespace, not only those, so the
[policy above](#require-cpu-requests) is the narrower way to close the gap.

These were measured with `default.cpu: 2` in a managed namespace, on Kubernetes
1.35 and 1.37 unless noted:

- **`defaultRequest` is filled in from `default`.** A container with no CPU request
  gets a request of 2 and books 2 CPUs. On 1.35, a request-less sidecar did this
  and the app beside it fell from 580m to 145m.
- **A pod requesting more than the default with no limit is refused at create.** A
  request of 4 failed with `must be less than or equal to cpu limit of 2`.
- **The webhook's birth limit is replaced.** On 1.35, a pod requesting 100m was born
  at 2 instead of 200m. The controller still manages it afterwards.
- **Some pods with pod-level resources are refused at create:** a pod-level CPU
  limit below the default, and a pod-level CPU request below the default with no
  container request.
- **A pod that pins memory and sets no CPU books the default.** A single-container
  pod like this, unmanaged before, became Guaranteed at a fixed request and limit of
  2.

It also lets a request-less container pass the policy's request rule, and a pod with
pod-level memory pass its ceiling rule, because the default lands before the policy
runs. That was admitted on both versions.

---

## Day-2: "my pod is throttled"

The design rule is that **any throttle is explainable in two `kubectl`
commands**. The answer is never "an agent decided based on a metric you can't
see."

1. **Is the node full of *requests*?** Throttling under Headroom means the node
   is booked, so the pod's ceiling has collapsed toward its request — working as
   designed.

   ```sh
   kubectl get pod -n team-a <pod> \
     -o jsonpath='{.metadata.annotations.kube-headroom\.dev/status}' | jq
   # low "slack" + factor near 1.0  ->  node is booked; ceiling == request-ish
   ```

2. **Read the last adjustment event:**

   ```sh
   kubectl describe pod -n team-a <pod> | grep -A2 CPULimitAdjusted
   # e.g. "1500m -> 3000m (node factor 2.00, slack 8/16 cores)"
   ```

The fix is self-service and lives in the [tenant guide](tenant-guide.md): raise
the request (buys guaranteed capacity, a bigger CFS weight, *and* a bigger slack
share), or move to a less-booked pool. Deciding *what* to raise it to is VPA's
job, not Headroom's.

**The money graph:** correlate `headroom_pod_limit_cores` with
`container_cpu_cfs_throttled_periods_total`. It answers "you were throttled
because the node was 94% booked; here's who booked it."

---

## Resize outcomes

Per-pod resize results the controller handles (design §6.4), each with a distinct
`headroom_resizes_total{result=...}` label:

| Result | Meaning | Controller action |
|---|---|---|
| `applied` | resize applied, cgroup rewritten | record annotation + `CPULimitAdjusted` event |
| `dry-run` | target computed, not applied (`dryRun: true`) | annotation + metric only |
| `Deferred` | kubelet will retry (transient) | none; `headroom_node_resizes_deferred{node}` counts the managed pods holding one, and a *sustained* nonzero count is an alerting signal |
| `infeasible` | kubelet cannot satisfy it (should be near-impossible — targets are capped at allocatable) | mark pod ineligible for `backoffPeriod`, warning event |
| `quota-denied` | `403 Forbidden` from a namespace `limits.cpu` ResourceQuota | back off like `infeasible`, `ResizeForbidden` event; **the real fix is the preflight** — quota on `requests.cpu` only |
| `limitrange-denied` | `403 Forbidden` from a LimitRange the clamp could not satisfy, such as a `max` below the pod's request or one the controller had not yet read | back off like `infeasible`, `ResizeLimitRangeDenied` event quoting the rule |
| `forbidden` | any other `403 Forbidden`, such as RBAC or an admission webhook | back off like `infeasible`, `ResizeForbidden` event quoting the apiserver |

When a pod's `backoffPeriod` (60s) ends, its node reconciles and retries it without
waiting for a pod event. A refusal nothing has fixed therefore adds about one to
its count per minute, per pod, and one warning event. Its neighbors' limits stay
where they are: a refused pod keeps its share of the node's slack during the window,
so a long refusal holds a share nobody uses until the cause is fixed.

A rising `quota-denied` count means a managed namespace still has a `limits.cpu`
quota. Fix the quota (see [Preflight](#preflight)); do not tune around it. A
sustained `limitrange-denied` count means something Headroom cannot change breaks
a LimitRange: a running pod's request, or the limit of a request-less or init
container. The event quotes the rule. Raise the `max`, or recreate the pod inside it.
The exception is a memory-pinned pod whose limit predates a fractional ratio and
which has a very small request, such as 100m + 10m at 1.05: Headroom's cap leaves it
no room above its request, so use a whole-number ratio.

---

## Metrics and dashboards

Prometheus series (design §8.1):

- `headroom_node_factor{node}` — the per-node slack factor `F = 1 + S/M`.
- `headroom_node_slack_cores{node}` — unbooked CPU on the node.
- `headroom_node_managed_pods{node}` — managed pods given a target in the node's
  last reconcile; design §6.4 says when a refused pod is left out.
- `headroom_pod_limit_cores{namespace,pod}` — the computed/applied ceiling.
- `headroom_resizes_total{result}` — `applied` / `dry-run` / `infeasible` /
  `quota-denied` / `limitrange-denied` / `forbidden` / `conflict` / `error`.
- `headroom_node_resizes_deferred{node}` — managed pods whose kubelet reports the
  resize as `Deferred` (`PodResizePending` condition).
- `headroom_reconcile_duration_seconds`, `headroom_pods_managed`.

The Grafana dashboard (`dashboards/headroom.json`) overlays
`headroom_pod_limit_cores` on `container_cpu_cfs_throttled_periods_total`.

**Alerts worth having:** `min_over_time(headroom_node_resizes_deferred[15m]) > 0`
(a node's kubelet is not converging), any `result="quota-denied"` (a namespace
violates the quota preflight), and `headroom_reconcile_duration_seconds` p99
climbing (cache or API pressure).

---

## Metrics TLS and scraper trust

<a id="metrics-tls"></a>
The manager serves `/metrics` over **HTTPS on `:8443`** by default
(`--metrics-secure=true`), behind Kubernetes authn/authz (a scraper needs a
token with `get` on the `/metrics` nonResourceURL). The **serving certificate**
is what a scraper has to trust — get this wrong and Prometheus is stuck on a
self-signed cert.

Two ways the serving cert gets provisioned:

- **Self-signed, in-process (default).** If no `--metrics-cert-path` is set,
  controller-runtime generates a self-signed cert in memory at startup. It is
  never written to a Secret and rotates on every restart, so scrapers **cannot
  pin a CA** — they can only `insecureSkipVerify: true`. Fine for a quick look;
  **not for production**, and it is exactly the "stuck on self-signed" trap.
- **cert-manager-issued (recommended for production).** The operator chart
  templates a cert-manager `Certificate` that issues into the
  `metrics-server-cert` Secret; the manager mounts it and is pointed at it with
  `--metrics-cert-path=/tmp/k8s-metrics-server/metrics-certs`. Now the cert is
  stable, has the service DNS SANs, and its `ca.crt` is a real trust anchor a
  scraper can verify against.

### Enabling the cert-manager wiring

Requires cert-manager installed in the cluster (`certmanager.enable=true`, the
default). Set **`metrics.certManagerTLS=true`** on the operator chart — one flag
turns on all three coupled pieces:

```sh
helm upgrade --install kube-headroom \
  oci://ghcr.io/karlkfi/charts/kube-headroom \
  --namespace kube-headroom-system \
  --set metrics.certManagerTLS=true \
  --set prometheus.enable=true
```

This mounts `metrics-server-cert` into the manager Deployment and adds the
`--metrics-cert-path` arg, issues the metrics `Certificate` with the real
service DNS SANs, and — when `prometheus.enable=true` — renders the
ServiceMonitor verifying against the Secret's `ca.crt` (and presenting the
client cert) instead of `insecureSkipVerify: true`.

Confirm the manager logs `Initializing metrics certificate watcher using
provided certificates` — that line means it picked up the mounted cert instead
of self-signing.

### Scrapers outside the ServiceMonitor path

If you scrape with something other than the shipped `ServiceMonitor` (a raw
Prometheus scrape config, the agent's own config, a different operator), point
its `tls_config` at the same CA:

- **CA to trust:** `ca.crt` from the `metrics-server-cert` Secret (or the
  cert-manager Issuer's CA).
- **`server_name`:** `<metrics-service>.<namespace>.svc` — must match a SAN on
  the cert (the `Certificate` lists `.svc` and `.svc.cluster.local`), or
  verification fails even with the right CA.
- **Bearer token:** a ServiceAccount token with access to the metrics
  nonResourceURL (the authn/authz filter rejects unauthenticated scrapes).

Do **not** paper over a verification failure with `insecureSkipVerify: true` in
production — that reintroduces the MITM exposure the cert wiring exists to close.
A "x509: certificate signed by unknown authority" scrape error almost always
means the manager is still self-signing (cert not mounted) or the scraper's CA /
`server_name` doesn't match the issued cert.

---

## Failure modes

<a id="failure-modes"></a>
The core safety property (design §8.6): **no failure mode is worse than not
running Headroom.** Because targets are a pure function of API-server state
(requests, never usage), a stalled controller is just stale — and stale limits
are safe.

- **Controller down / paused (`dryRun`):** limits freeze at their last values.
  Frozen-generous limits on a node that then fills degrade to "generous static
  limits," and `cpu.weight` still enforces request-proportional sharing under
  contention. Frozen-tight limits on a node that then empties give unnecessary
  throttling — i.e., plain Kubernetes. Neither is worse than not running it.
- **API-server pressure:** rate limits (`perNodePatchesPerSecond`,
  `clientQPS`/`clientBurst`) bound the write load (`clientQPS` and `clientBurst`
  are read once at startup, so changing them needs a manager restart); the degraded mode is
  staleness, which is safe per above. If you must shed load fast, raise the
  deadband or set `dryRun: true`.
- **Two leaders (split brain):** leader election prevents it; even if violated,
  both replicas compute the same pure function over the same cache and issue
  convergent patches.
- **Thundering herd on restart:** the initial reconcile finds most limits
  already within deadband, so few patches fire; initial sync is jittered.
- **Backoff after a restart/failover:** the per-pod backoff window is in-memory,
  so a restart, rollout, or leader change starts with it empty. A pod still
  showing an `Infeasible` resize re-enters backoff from its own status on its
  first reconcile — no retry patch is issued, though its one-time `Infeasible`
  warning event and `result="infeasible"` increment reappear. The `403` cases are
  *not* re-derivable from pod status, so a quota-blocked pod re-issues its
  resize once (one `403`, one `quota-denied` increment, one `ResizeForbidden`
  event) before backing off again, and `limitrange-denied` and `forbidden` do the
  same with their own labels. A brief `quota-denied` blip right after a
  manager restart is therefore expected; a *sustained* count still means a
  namespace violates the `requests.cpu`-only preflight (fix the quota, above).

When in doubt, `dryRun: true` is the safe stop button — it never removes a limit,
it only stops changing them.
