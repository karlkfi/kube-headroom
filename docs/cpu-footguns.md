# CPU footguns: runtimes that size themselves from the wrong CPU count

Almost every language runtime and compute framework decides its parallelism —
thread pools, worker counts, GC threads — from *some* CPU number, read at
*some* moment. Inside Kubernetes both parts go wrong:

- **The wrong number.** APIs like `nproc`, `os.cpu_count()`, and `os.cpus()`
  report the **node's** cores (or the CPU affinity mask) — not your cgroup
  quota. A pod with `limits.cpu: 2` on a 64-core node sees 64.
- **The wrong moment.** Runtimes that *are* quota-aware usually read the quota
  **once at startup**. Under Headroom the limit changes at runtime (in-place
  resize), so a boot-sized pool never grows into a raised ceiling — and a pool
  sized on an idle node may be oversized once the node fills.

The kernel is unaffected either way: CFS enforces the current quota and
`cpu.weight` regardless of what your runtime believes. The damage is
second-order — too many threads timeslicing inside a small quota (latency,
throttling) or too few threads using a large one (wasted headroom).

This page catalogs the known offenders and the workaround for each. The
general-purpose fixes come first; runtime notes below assume them.

## The two universal workarounds

**1. Pin parallelism explicitly from the Downward API.** Expose your CPU
*limit* (or request) as an environment variable and size from it:

```yaml
env:
  - name: CPU_LIMIT
    valueFrom:
      resourceFieldRef:
        resource: limits.cpu
        divisor: "1"        # whole cores, rounded up
```

This is a boot-time value — it will not follow later in-place resizes — but it
is *your* value, derived from your actual allocation instead of the node's
core count. Cap Headroom to match with the
[`kube-headroom.dev/max-cpu` annotation](tenant-guide.md#cap-your-own-ceiling)
if your pool must never be undersized relative to the ceiling.

**2. Accept birth-limit sizing.** Headroom's admission webhook (when enabled)
seeds a generous initial limit at pod CREATE time, so quota-aware runtimes
boot with a sensible number even though they won't track later raises. Good
enough for most services; not for pools you've tuned by hand.

## Go

- `GOMAXPROCS` historically defaults to the **node's logical cores** — Go
  before 1.25 is not cgroup-aware at all.
- [`uber-go/automaxprocs`](https://github.com/uber-go/automaxprocs) reads the
  cgroup quota **once at init** and pins `GOMAXPROCS`; later resizes are
  ignored.
- **Go 1.25+** sets `GOMAXPROCS` from the cgroup CPU limit natively and
  **updates it periodically** as the limit changes — the best-behaved runtime
  on this page. Setting `GOMAXPROCS` explicitly disables the automatic
  behavior.

**Workaround:** upgrade to Go 1.25+, or set `GOMAXPROCS` from the Downward
API: `GOMAXPROCS: $(CPU_LIMIT)`.

## JVM (Java, Kotlin, Scala)

- Container support (`-XX:+UseContainerSupport`, default on since JDK 8u191 /
  JDK 10) makes the JVM read the **cgroup quota** — but the sizings derived
  from it (GC worker threads, JIT compiler threads,
  `ForkJoinPool.commonPool`, and most framework pools built on
  `Runtime.availableProcessors()`) are computed **at JVM startup** and never
  revisited.
- Without container support (ancient JDK 8 builds), the JVM sizes from the
  node's cores — dozens of GC threads inside a 1-core quota.

**Workaround:** `-XX:ActiveProcessorCount=<n>` (drive it from the Downward
API), and size explicit executors from your own configuration, not
`availableProcessors()`.

## Python

- `os.cpu_count()` and `multiprocessing.cpu_count()` return the **node's**
  core count — never the cgroup quota. (`os.process_cpu_count()`, added in
  3.13, honors the *affinity mask* — still not the quota.)
- The classic footgun: **Gunicorn's** documented `workers = cpu_count * 2 + 1`
  recipe, **Celery's** default `--concurrency` (= cpu_count), and
  `ProcessPoolExecutor`'s default all fork node-cores-many workers inside a
  small quota.
- **NumPy/SciPy** pull in OpenMP/BLAS pools sized at **import time** — see the
  OpenMP section.
- `joblib`/`loky` is a partial exception: it estimates effective CPUs from
  cgroup limits (once, at pool creation).

**Workaround:** set worker counts explicitly (`GUNICORN_CMD_ARGS="--workers=N"`,
`celery --concurrency=N`) from the Downward API; set the BLAS env vars below
before interpreter start.

## OpenMP, OpenBLAS, MKL (and everything built on them)

- Default thread count = cores visible **at library initialization** (import
  time for NumPy/SciPy/PyTorch CPU ops). Quota-unaware on most builds.
- One oversubscribed BLAS pool per process, multiplied by forked workers, is
  the classic "why is my 2-CPU pod at 6400% internal contention" incident.

**Workaround:** `OMP_NUM_THREADS`, `OPENBLAS_NUM_THREADS`, `MKL_NUM_THREADS`
(set them in the pod spec from the Downward API — they must exist before
import/init).

## Node.js

- `os.cpus().length` and `os.availableParallelism()` report the **node** (or
  affinity) — not the quota. The standard `cluster`-module pattern
  (`os.cpus().length` workers) forks node-cores-many processes.
- The libuv pool (`UV_THREADPOOL_SIZE`) defaults to a fixed 4 — undersized for
  big quotas rather than oversized, but same class of problem.

**Workaround:** explicit worker count from the Downward API;
`UV_THREADPOOL_SIZE=$(CPU_LIMIT)` where I/O-bound.

## .NET

- `Environment.ProcessorCount` has been cgroup-aware since .NET Core 3.0
  (quota rounded **up**), read **at process start**; `ThreadPool` heuristics
  derive from it at startup.
- `DOTNET_PROCESSOR_COUNT` overrides it explicitly.

**Workaround:** `DOTNET_PROCESSOR_COUNT` from the Downward API when the
default rounding or boot-time snapshot misbehaves.

## Rust

- `std::thread::available_parallelism()` **is** cgroup-quota-aware on Linux —
  but callers cache it: **Tokio** sizes its worker threads from it **once at
  runtime construction**; **Rayon** builds its global pool on first use.

**Workaround:** `TOKIO_WORKER_THREADS` / `RAYON_NUM_THREADS` from the
Downward API when you need a different number than the boot-time quota.

## ML / GPU frameworks

GPU workloads are *CPU* victims here: dataloaders, preprocessing, and
inter-op pools are CPU-bound, and starving them idles the GPU — the exact
failure Headroom exists to relieve (see the
[applicability matrix](applicability.md)).

- **PyTorch** — intra-op threads default from the visible CPU count at import
  (`torch.set_num_threads` / `torch.set_num_interop_threads` to override);
  the ubiquitous `DataLoader(num_workers=os.cpu_count())` pattern forks
  node-cores-many workers.
- **TensorFlow** — inter-op/intra-op pools sized from visible CPUs at session
  creation; override with `tf.config.threading.*`.
- **ONNX Runtime** — `intra_op_num_threads` defaults from visible CPUs at
  session creation; set it in `SessionOptions`.
- **Ray** — advertises `num_cpus` from the detected CPU count at `ray init`;
  pass `num_cpus` explicitly.
- **NVIDIA DALI / Triton** — pipeline `num_threads` and instance-group
  settings are explicit configuration; the footgun is copying
  `cpu_count()`-derived values into them.

**Workaround:** every one of these has an explicit knob — drive it from the
Downward API, not from `cpu_count()`.

## `nproc` and shell scripts

`nproc` (coreutils) honors the **affinity mask**, not the cgroup quota.
`make -j$(nproc)` in a CI pod on a 96-core node is 96 compile jobs inside
whatever quota you were given. Prefer `make -j"${CPU_LIMIT}"` via the
Downward API.

---

**Related:** the [tenant guide](tenant-guide.md#runtimes-that-read-cpu-quota-once-at-startup)
covers how this interacts with Headroom's live resizes; the
[applicability matrix](applicability.md) covers when boot-time-sized runtimes
still benefit.
