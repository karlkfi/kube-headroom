# CPU footguns: runtimes that size themselves from the wrong CPU count

Almost every language runtime and compute framework decides its parallelism —
thread pools, worker counts, GC threads — from *some* CPU number, read at
*some* moment. Inside Kubernetes both parts go wrong:

- **The wrong number.** APIs like `nproc`, `os.cpu_count()`, and `os.cpus()`
  report the **node's** cores (or the CPU affinity mask) — not your cgroup
  quota. A pod with `limits.cpu: 2` on a 64-core node sees 64.
- **The wrong moment.** Runtimes that *are* quota-aware usually read the quota
  **once at startup**. Under Headroom the limit changes at runtime (in-place
  resize), so a boot-sized pool never grows into a raised limit — and a pool
  sized on an idle node may be oversized once the node fills.

The kernel is unaffected either way: CFS enforces the current quota and
`cpu.weight` regardless of what your runtime believes. The damage is
second-order — too many threads timeslicing inside a small quota (latency,
throttling) or too few threads using a large one (wasted capacity).

## At a glance

| Runtime / library | Reads | When | Fix |
|---|---|---|---|
| Go ≤ 1.24 | node cores | startup | `GOMAXPROCS`, automaxprocs |
| **Go 1.25+** | **quota** | **startup + periodic** | (behaves correctly) |
| JVM (JDK 10+) | quota | pools/GC/JIT fixed at startup | `-XX:ActiveProcessorCount` |
| Python `cpu_count()` | node cores | per call | `PYTHON_CPU_COUNT` (3.13+) |
| Celery | node cores | worker boot | `--concurrency=N` |
| Gunicorn | (defaults to 1) | master boot | `workers` / `WEB_CONCURRENCY` |
| joblib/loky | quota | pool creation | (quota-aware; `n_jobs` to pin) |
| Node.js `os.cpus()` | node cores | per call | size workers explicitly |
| Node.js `availableParallelism()` | affinity; quota on libuv ≥ 1.49 | per call | verify your version |
| .NET | quota (rounded up) | fixed at startup | `DOTNET_PROCESSOR_COUNT` |
| Rust `available_parallelism` | quota | per call (pools cache it) | `TOKIO_WORKER_THREADS`, `RAYON_NUM_THREADS` |
| OpenMP / OpenBLAS / MKL | node cores | library init | `OMP_NUM_THREADS` etc. |
| PyTorch / TensorFlow / ONNX | node cores | first op / session | explicit thread APIs |
| Ray | quota | node start | `--num-cpus=N` |
| `nproc` | affinity; cgroup v2 quota in coreutils ≥ 9.8 | per call | `make -j"$CPU_LIMIT"` |

This page catalogs each in detail. The general-purpose fixes come first; the
runtime notes assume them.

## The two universal workarounds

**1. Pin parallelism explicitly from the Downward API.** Expose your CPU
*limit* (or request) as an environment variable and size from it:

```yaml
env:
  - name: CPU_LIMIT
    valueFrom:
      resourceFieldRef:
        resource: limits.cpu
        divisor: "1"        # whole cores, fractional values round UP (500m → 1)
```

Use `divisor: "1m"` if you need exact millicores. Two caveats: if the
container has no limit set, the kubelet substitutes **node allocatable**; and
`resourceFieldRef` **env vars are static** — an in-place resize does not
update them (only `downwardAPI` *volumes* update live), so this is a
boot-time value. It is still *your* value, derived from your actual
allocation instead of the node's core count. Cap Headroom to match with the
[`kube-headroom.dev/max-cpu` annotation](tenant-guide.md#cap-your-own-ceiling)
if your pool must never be undersized relative to the limit.

**2. Accept birth-limit sizing.** Headroom's admission webhook (when enabled)
seeds a generous initial limit at pod CREATE time, so quota-aware runtimes
boot with a sensible number even though they won't track later raises. Good
enough for most services; not for pools you've tuned by hand.

## Go

- `GOMAXPROCS` on Go ≤ 1.24 defaults to the **node's logical cores** — not
  cgroup-aware at all.
- [`uber-go/automaxprocs`](https://github.com/uber-go/automaxprocs) reads the
  cgroup quota **once at `init()`** and pins `GOMAXPROCS`; later resizes are
  ignored until restart.
- **Go 1.25+** sets `GOMAXPROCS` from the cgroup CPU limit natively (rounded
  up, never below 2) and **updates it periodically** as the limit changes
  ([release notes](https://go.dev/doc/go1.25)) — the best-behaved runtime on
  this page, and the one case where Headroom's live raises are picked up
  automatically. Setting `GOMAXPROCS` explicitly disables the automatic
  behavior (`GODEBUG=containermaxprocs=0` / `updatemaxprocs=0` opt out).

**Workaround:** upgrade to Go 1.25+, or set `GOMAXPROCS` from the Downward
API: `GOMAXPROCS: $(CPU_LIMIT)`.

## JVM (Java, Kotlin, Scala)

- Container support ([JDK-8146115](https://bugs.openjdk.org/browse/JDK-8146115),
  default on since JDK 10, backported to 8u191) derives the active processor
  count from the **cgroup quota**, rounded up.
- The subtle part: `Runtime.availableProcessors()` is actually re-read
  dynamically (HotSpot caches container metrics for only ~20 ms) — but the
  sizings that matter are computed **once at JVM startup** and never
  revisited: GC worker threads, JIT compiler threads, default heap sizing,
  and `ForkJoinPool.commonPool()` (statically constructed at
  `availableProcessors() - 1`). A live-raised limit is visible to your code
  but not to the pools the JVM already built.

**Workaround:** `-XX:ActiveProcessorCount=<n>` (drive it from the Downward
API); `-Djava.util.concurrent.ForkJoinPool.common.parallelism=N` for the
common pool; size explicit executors from configuration, not
`availableProcessors()`.

## Python

- `os.cpu_count()` and `multiprocessing.cpu_count()` return the **node's**
  core count — never the cgroup quota
  ([cpython#80235](https://github.com/python/cpython/issues/80235), open
  since 2019). `os.process_cpu_count()` (3.13+) honors the *affinity mask* —
  still not the quota.
- **Celery**'s `--concurrency` defaults to the machine's CPU count — node
  cores, read once at worker boot. **Gunicorn**'s `workers` actually defaults
  to `1` (via `WEB_CONCURRENCY`) — the footgun is the documented
  `2-4 × $(NUM_CORES)` recipe that teams copy with node-derived core counts.
  `ProcessPoolExecutor` defaults to `cpu_count()` workers (affinity-based
  `process_cpu_count()` since 3.13).
- **NumPy/SciPy** pull in OpenMP/BLAS pools sized at **import time** — see
  the OpenMP section.
- `joblib`/`loky` is the Python exception: its
  [`cpu_count()`](https://joblib.readthedocs.io/en/latest/generated/joblib.cpu_count.html)
  explicitly accounts for CFS quotas (evaluated at pool creation, not
  import).

**Workaround:** Python 3.13+ honors `PYTHON_CPU_COUNT=N` (or
`python -X cpu_count=N`) for both `cpu_count` APIs; set worker counts
explicitly (`WEB_CONCURRENCY`, `celery --concurrency=N`) from the Downward
API; set the BLAS env vars below before interpreter start.

## OpenMP, OpenBLAS, MKL (and everything built on them)

- Default thread count = cores visible **at library initialization** (first
  parallel region, or import time for NumPy/SciPy/PyTorch CPU ops). Affinity
  is respected; cgroup quota is not. MKL sizes from **physical** cores.
- One oversubscribed BLAS pool per process, multiplied by forked workers, is
  the classic "why is my 2-CPU pod at 6400% internal contention" incident.

**Workaround:** `OMP_NUM_THREADS`, `OPENBLAS_NUM_THREADS`, `MKL_NUM_THREADS`
(set them in the pod spec from the Downward API — they must exist before
import/init).

## Node.js

- `os.cpus().length` reports the **node's** cores; the docs themselves say
  not to size parallelism from it. The standard `cluster`-module pattern
  (`availableParallelism()` workers, forked by your own loop) inherits
  whatever that call returns.
- `os.availableParallelism()` is affinity-aware; cgroup **quota** awareness
  arrived in libuv 1.49 (bundled from Node 23.1) via
  [libuv#4278](https://github.com/libuv/libuv/pull/4278) — but it is
  version-dependent and has had correctness bugs under Kubernetes
  ([libuv#4740](https://github.com/libuv/libuv/issues/4740)). Verify on your
  Node version before trusting it.
- The libuv pool (`UV_THREADPOOL_SIZE`) defaults to a fixed 4 — undersized
  for big quotas rather than oversized, but the same class of problem.

**Workaround:** explicit worker count from the Downward API;
`UV_THREADPOOL_SIZE=$(CPU_LIMIT)` where I/O-bound.

## .NET

- `Environment.ProcessorCount` has been cgroup-quota-aware since .NET Core
  3.0 (quota rounded **up**), and is **fixed at runtime startup for the
  process lifetime** — the
  [docs say so explicitly](https://learn.microsoft.com/en-us/dotnet/api/system.environment.processorcount).
  `ThreadPool` heuristics and GC sizing derive from it at startup.

**Workaround:** `DOTNET_PROCESSOR_COUNT=N` from the Downward API;
`ThreadPool.SetMinThreads`/`SetMaxThreads` for the pool.

## Rust

- [`std::thread::available_parallelism()`](https://doc.rust-lang.org/std/thread/fn.available_parallelism.html)
  **is** cgroup-quota-aware on Linux (v2 since 1.61, v1 since 1.64) and is
  deliberately **not cached** — it recomputes on every call. The footgun is
  downstream: **Tokio** sizes its workers once at runtime construction (via
  the quota-aware `num_cpus` crate) and never resizes; **Rayon** builds its
  global pool once at first use.

**Workaround:** `TOKIO_WORKER_THREADS` / `RAYON_NUM_THREADS` from the
Downward API when you need a different number than the boot-time quota.

## ML / GPU frameworks

GPU workloads are *CPU* victims here: dataloaders, preprocessing, and
inter-op pools are CPU-bound, and starving them idles the GPU — the exact
failure Headroom exists to relieve (see the
[applicability matrix](applicability.md)).

- **PyTorch** — intra-op and inter-op pools default to the visible core
  count (via OpenMP/native backends — node cores, not quota), fixed when the
  parallel backend initializes; the docs warn to call
  [`torch.set_num_threads`](https://docs.pytorch.org/docs/stable/generated/torch.set_num_threads.html)
  *before running eager, JIT or autograd code*. `DataLoader`'s
  `num_workers` defaults to 0; the widespread
  `num_workers=os.cpu_count()` convention imports the node-cores problem.
- **TensorFlow** — inter-op/intra-op pools default to "system-picked"
  (host cores) and are **frozen at context initialization** — the setters
  literally raise `RuntimeError` after the first op. Use
  `tf.config.threading.set_*_parallelism_threads(N)` before any op runs.
- **ONNX Runtime** — thread pools are created **at session creation**, one
  thread per physical core by default; set
  `SessionOptions.intra_op_num_threads` explicitly.
- **Ray** — actually cgroup-quota-aware (reads `cpu.max` / CFS quota at node
  start, rounding sub-core quotas up to 1), but reads it **once** at
  `ray.init()`; bursting (no quota) falls back to node cores. Pass
  `--num-cpus=N` explicitly.
- **NVIDIA DALI / Triton** — pipeline `num_threads` and instance-group
  settings are explicit configuration; the footgun is copying
  `cpu_count()`-derived values into them.

**Workaround:** every one of these has an explicit knob — drive it from the
Downward API, not from `cpu_count()`.

## `nproc` and shell scripts

`nproc` honors the **affinity mask**, and only in coreutils ≥ 9.8 the cgroup
**v2** quota (v1 never). On the typical CI image, `make -j$(nproc)` in a pod
on a 96-core node is 96 compile jobs inside whatever quota you were given.
Prefer `make -j"${CPU_LIMIT}"` via the Downward API.

---

**The pattern across the whole page:** only Go 1.25+ tracks the quota *live*;
the JVM re-reads it but its pools don't; Rust recomputes it but its pools
don't; everything else reads node cores, affinity, or a boot-time quota
snapshot. So a live-raised CPU limit generally does not grow existing thread
pools without a restart — which is why the
[tenant guide](tenant-guide.md#runtimes-that-read-cpu-quota-once-at-startup)
recommends explicit sizing or birth-limit acceptance, and why the
[applicability matrix](applicability.md) lists boot-time-sized runtimes as
"partial benefit."
