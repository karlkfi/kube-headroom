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
  raise silently eats quota too. Audit every managed namespace:

  ```sh
  kubectl get resourcequota -A -o json \
    | jq -r '.items[] | select(.spec.hard["limits.cpu"]) | "\(.metadata.namespace)/\(.metadata.name)"'
  ```

  Any hit must drop its `limits.cpu` hard limit (keep `requests.cpu`) before
  enrollment, or the namespace will see resize 403s.
- **LimitRange `max` caps what Headroom can set.** A namespace `max.limits.cpu`
  (or a per-container `max`) clamps the achievable ceiling. This is safe (raises
  just stop at the cap) but means the "empty-node personality" is bounded;
  document it for tenants who wonder why their limit plateaus.
- **VPA, if present, must be `controlledValues: RequestsOnly`** on managed pods.
  Default-mode VPA (`RequestsAndLimits`) scales limits and conflicts directly —
  exclude those pods. See the [tenant guide](tenant-guide.md#vpa) for the
  coexistence recipe.

---

## Install and roll out

The rollout is deliberately staged: **dry-run → observe → enforce**, one
namespace at a time.

1. **Install the CRD, then the operator, with Helm.** Headroom ships two OCI
   charts on ghcr: `kube-headroom-crds` (the CRD, cluster-wide, installed once)
   and `kube-headroom` (the namespaced operator). The CRD chart carries
   `helm.sh/resource-policy: keep`, so a later `helm uninstall` never drops live
   `HeadroomConfig`s. cert-manager must be present (it issues the webhook
   serving cert).

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
   # give the reconciler a scheduling event or two, then inspect a pod:
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

   Now watch `CPULimitAdjusted` events and `headroom_resizes_total{result="ok"}`.
   A managed pod alone on a node should climb toward allocatable; scheduling a
   neighbor should shrink it within a few seconds.

4. **Add namespaces incrementally.** Repeat step 2 per namespace. There is no
   need to re-toggle dry-run; enrollment is per-namespace via the label.

**Rollback** is always available and always safe (§ [Failure modes](#failure-modes)):
set `dryRun: true` again (stops all patching; limits freeze at their last
values), or remove the namespace label (the namespace's pods stop being managed;
their current limits stay put — Headroom never *removes* a limit).

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
| `ok` | resize applied, cgroup rewritten | record annotation + `CPULimitAdjusted` event |
| `dry-run` | target computed, not applied (`dryRun: true`) | annotation + metric only |
| `Deferred` | kubelet will retry (transient) | track a gauge; a *sustained* deferred count on limit-only raises is an alerting signal |
| `Infeasible` | kubelet cannot satisfy it (should be near-impossible — targets are capped at allocatable) | mark pod ineligible for `backoffPeriod`, warning event |
| `quota-denied` | `403 Forbidden` from a namespace `limits.cpu` ResourceQuota | back off like `Infeasible`; **the real fix is the preflight** — quota on `requests.cpu` only |

A rising `quota-denied` count means a managed namespace still has a `limits.cpu`
quota. Fix the quota (see [Preflight](#preflight)); do not tune around it.

---

## Metrics and dashboards

Prometheus series (design §8.1):

- `headroom_node_factor{node}` — the per-node slack factor `F = 1 + S/M`.
- `headroom_node_slack_cores{node}` — unbooked CPU on the node.
- `headroom_pod_limit_cores{pod}` — the computed/applied ceiling.
- `headroom_resizes_total{result}` — `ok` / `dry-run` / `Deferred` /
  `Infeasible` / `quota-denied`.
- `headroom_reconcile_duration_seconds`, `headroom_pods_managed`.

Ship the Grafana dashboard (`dashboards/headroom.json`, lands with observability
in Q7) that overlays `headroom_pod_limit_cores` on
`container_cpu_cfs_throttled_periods_total`.

**Alerts worth having:** sustained `result="Deferred"` (kubelet not converging),
any `result="quota-denied"` (a namespace violates the quota preflight), and
`headroom_reconcile_duration_seconds` p99 climbing (cache or API pressure).

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
  `clientQPS`/`clientBurst`) bound the write load; the degraded mode is
  staleness, which is safe per above. If you must shed load fast, raise the
  deadband or set `dryRun: true`.
- **Two leaders (split brain):** leader election prevents it; even if violated,
  both replicas compute the same pure function over the same cache and issue
  convergent patches.
- **Thundering herd on restart:** the initial reconcile finds most limits
  already within deadband, so few patches fire; initial sync is jittered.

When in doubt, `dryRun: true` is the safe stop button — it never removes a limit,
it only stops changing them.
