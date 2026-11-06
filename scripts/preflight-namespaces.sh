#!/usr/bin/env bash
# preflight-namespaces.sh — check namespaces about to be enrolled against the
# quota and LimitRange rules in docs/runbook.md#preflight.
#
#   scripts/preflight-namespaces.sh [--strict] [NAMESPACE...]
#
# With no NAMESPACE, checks every namespace labeled kube-headroom.dev/mode=managed.
#
#   ERROR  a ResourceQuota with a limits.cpu hard limit: Headroom's raises
#          consume it, and an over-budget raise fails admission with a 403.
#   WARN   a LimitRange CPU max, maxLimitRequestRatio or default: each changes
#          what Headroom can set, but none produces 403s on its own.
#
# Exit status: 0 clean (or warnings only), 1 at least one ERROR (or any WARN
# under --strict), 2 the check could not run (bad usage, kubectl failure).
# Uses the current kubectl context; set KUBECONFIG/--context via the environment.
set -euo pipefail

strict=0
namespaces=()
for arg in "$@"; do
  case "${arg}" in
    --strict) strict=1 ;;
    -h|--help) sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown flag: ${arg}" >&2; exit 2 ;;
    *) namespaces+=("${arg}") ;;
  esac
done

if [[ ${#namespaces[@]} -eq 0 ]]; then
  ns_json="$(kubectl get namespaces -l kube-headroom.dev/mode=managed -o json)" || exit 2
  while IFS= read -r ns; do
    namespaces+=("${ns}")
  done < <(jq -r '.items[].metadata.name' <<<"${ns_json}")
  if [[ ${#namespaces[@]} -eq 0 ]]; then
    echo "no namespaces labeled kube-headroom.dev/mode=managed; pass namespaces to check" >&2
    exit 2
  fi
fi

errors=0
warnings=0

for ns in "${namespaces[@]}"; do
  # A list in a missing namespace is empty, which would read as a clean one.
  kubectl get namespace "${ns}" >/dev/null || exit 2
  quotas="$(kubectl get resourcequota -n "${ns}" -o json)" || exit 2
  ranges="$(kubectl get limitrange -n "${ns}" -o json)" || exit 2

  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    echo "ERROR ${ns}: ${line}"
    errors=$((errors + 1))
  done < <(jq -r '.items[] | select(.spec.hard["limits.cpu"]) |
    "ResourceQuota \(.metadata.name) sets limits.cpu=\(.spec.hard["limits.cpu"]); drop it and quota requests.cpu instead (runbook#preflight)"' <<<"${quotas}")

  while IFS= read -r line; do
    [[ -n "${line}" ]] || continue
    echo "WARN  ${ns}: ${line}"
    warnings=$((warnings + 1))
  done < <(jq -r '.items[] | .metadata.name as $n | .spec.limits[]? | .type as $t |
    (if .max.cpu then "LimitRange \($n) (\($t)) caps CPU limits at max=\(.max.cpu); raises stop there (runbook#preflight)" else empty end),
    (if .maxLimitRequestRatio.cpu then "LimitRange \($n) (\($t)) caps limit/request at \(.maxLimitRequestRatio.cpu); raises stop there (runbook#preflight)" else empty end),
    (if .default.cpu then "LimitRange \($n) (\($t)) sets default.cpu=\(.default.cpu); it also fills defaultRequest and replaces the webhook birth limit (runbook#limitrange-default)" else empty end)' <<<"${ranges}")
done

echo "checked ${#namespaces[@]} namespace(s): ${errors} error(s), ${warnings} warning(s)"
if [[ ${errors} -gt 0 ]] || { [[ ${strict} -eq 1 ]] && [[ ${warnings} -gt 0 ]]; }; then
  exit 1
fi
