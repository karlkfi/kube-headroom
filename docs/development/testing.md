# Testing

Three tiers, cheapest first. **Pick the narrowest tier that can observe the
bug** — a policy arithmetic error belongs in a unit test, not an e2e run that
takes minutes to reproduce it.

## The tiers

### 1. Policy unit tests — no cluster

Pure Go against the k8s-free policy core in `internal/policy/`. Table tests plus
property tests cover every §5 invariant of the design. No apiserver, no
scheduler, no I/O.

```
go test ./internal/policy/
```

Runs in well under a second. This is where slack math, caps-win precedence, and
every distribution edge case are pinned. If a behavior can be expressed as
"given these requests and this allocatable, the limit should be X," it is a
unit test.

### 2. Integration tests — envtest

Ginkgo/Gomega suites under `internal/controller/` that run against a real
apiserver + etcd via [envtest](https://book.kubebuilder.io/reference/envtest.html)
(no kubelet, no scheduler, no container runtime). See `suite_test.go` for the
harness.

```
make test
```

This is the default for anything touching Go. It exercises reconciler logic:
watches, requeues, status writes, and the resize subresource against a genuine
API surface — reconciliation behavior a unit test can't reach, without the cost
of a full node. `make test` runs `manifests generate fmt vet` first, so it also
catches stale generated files.

### 3. End-to-end tests — kind

Full-cluster tests on a **dedicated** kind cluster (≥1.35 for GA in-place
resize) with a real kubelet and scheduler.

```
make setup-test-e2e test-e2e
```

Reserved for behavior that only emerges from real scheduling and cgroup
enforcement: a pod on an empty node running unthrottled, a limit shrinking
within seconds when a neighbor schedules, the cluster staying safe when the
controller is killed (Q8 exit criteria, design §10). Never point e2e at a dev or
prod context — see [kind-iteration.md](kind-iteration.md) for the cluster
lifecycle.

## How fast each tier should stay

| Tier | Command | Budget | Runs |
|---|---|---|---|
| Unit | `go test ./internal/policy/` | sub-second | every save |
| Integration | `make test` | seconds–low minutes | before every commit touching Go |
| e2e | `make test-e2e` | minutes | before release-shaped changes, and in CI |

When a tier gets slow, that is a bug in the tests, not a cost of doing business.
A flaky or slow e2e run pushes people to skip it; keeping it fast is what keeps
it trusted. Flake fixes go to the **top** of the queue — see
[technical-debt.md](technical-debt.md).

## Choosing a tier for a new test

1. Can it be reproduced from requests + allocatable alone? → unit test.
2. Does it need the apiserver — watches, status, subresources — but not a real
   node? → envtest integration test.
3. Does it depend on the scheduler or actual cgroup enforcement? → e2e.

Write the test at the lowest tier that still fails before the fix and passes
after. A bug caught by a unit test is a bug you can debug in a second.
