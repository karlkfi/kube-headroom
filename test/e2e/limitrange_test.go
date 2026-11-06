//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/karlkfi/kube-headroom/test/utils"
)

const (
	clampNamespace   = "headroom-e2e-clamp"
	refusedNamespace = "headroom-e2e-refused"
	quotaNamespace   = "headroom-e2e-quota"
	// leaderLease is LeaderElectionID in cmd/main.go.
	leaderLease = "605152a6.dev"
)

// limitRangeSpecs covers design §8.3 and §6.4 end to end: a raise stops at a
// LimitRange max instead of failing, and the refusals a clamp cannot prevent name
// who refused them in the event, the log and the metric. It runs inside the "Manager"
// Ordered Describe, which leaves the metrics binding in place, and removes its pods
// before the slack scenarios in headroomSpecs size their neighbor.
func limitRangeSpecs() {
	Context("LimitRange caps and resize refusals", Ordered, func() {
		var workerNode string

		BeforeAll(func() {
			workerNode = workerNodeName()
			Expect(workerNode).NotTo(BeEmpty(), "expected a non-control-plane worker node")

			for _, ns := range []string{clampNamespace, refusedNamespace, quotaNamespace} {
				_, _ = kubectl("create", "ns", ns)
				_, err := kubectl("label", "--overwrite", "ns", ns, "kube-headroom.dev/mode=managed")
				Expect(err).NotTo(HaveOccurred(), "failed to label namespace %s", ns)
			}
			_, err := kubectl("apply", "-f", "test/e2e/testdata/headroomconfig.yaml")
			Expect(err).NotTo(HaveOccurred(), "failed to apply HeadroomConfig")
		})

		AfterAll(func() {
			for _, ns := range []string{clampNamespace, refusedNamespace, quotaNamespace} {
				_, _ = kubectl("delete", "ns", ns, "--ignore-not-found")
			}
			_, _ = kubectl("delete", "headroomconfig", "cluster", "--ignore-not-found")
		})

		It("stops a raise at the LimitRange max", func() {
			applyManifest(limitRangeManifest(clampNamespace, "max", 500))
			awaitAdmissionRefusal(clampNamespace, "maximum cpu usage per Container")

			before := resizeResultCount(resultLabelLimitRange)
			applyLimitedPod("capped", clampNamespace, workerNode, 100, 200)
			Eventually(func(g Gomega) {
				g.Expect(containerLimitMilli(g, clampNamespace, "capped")).To(Equal(int64(500)))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())

			Eventually(func() string {
				return eventNotes(clampNamespace, "CPULimitAdjusted")
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(ContainSubstring("200m → 500m"))
			Expect(resizeResultCount(resultLabelLimitRange)).To(Equal(before), "the clamp must prevent the refusal")
		})

		// LimitRanger defaults a missing limit to the max before the webhook runs, so
		// the birth limit never exceeds it and creation is never refused.
		It("admits a pod with no limit at the LimitRange max, ahead of the birth-limit webhook", func() {
			applyPod("defaulted", clampNamespace, workerNode, 100)
			Eventually(func(g Gomega) {
				g.Expect(specContainerLimitMilli(g, clampNamespace, "defaulted")).To(Equal(int64(500)))
				g.Expect(podPhase(g, clampNamespace, "defaulted")).To(Equal("Running"))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())
		})

		// A max below the request leaves no limit the apiserver admits, which is the
		// refusal a clamp cannot prevent however fresh its read of the LimitRange.
		It("names the LimitRange when no limit satisfies it", func() {
			applyLimitedPod("refused", refusedNamespace, workerNode, 100, 1000)
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, refusedNamespace, "refused")).To(Equal("Running"))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())

			// Read first: a scrape pod is itself a pod event that can reconcile the worker.
			before, quotaBefore := resizeResultCount(resultLabelLimitRange), resizeResultCount(resultLabelQuota)
			applyManifest(limitRangeManifest(refusedNamespace, "late", 50))
			awaitAdmissionRefusal(refusedNamespace, "maximum cpu usage per Container is 50m")

			Eventually(func() string {
				return eventNotes(refusedNamespace, "ResizeLimitRangeDenied")
			}).WithTimeout(90 * time.Second).WithPolling(time.Second).Should(And(
				ContainSubstring("refused by a LimitRange"),
				ContainSubstring("maximum cpu usage per Container is 50m"),
			))
			Expect(controllerLogLine(refusedNamespace + "/refused")).To(ContainSubstring("a LimitRange"))
			Expect(resizeResultCount(resultLabelLimitRange)).To(BeNumerically(">", before))
			Expect(resizeResultCount(resultLabelQuota)).To(Equal(quotaBefore), "must not be blamed on a ResourceQuota")
			Expect(eventNotes(refusedNamespace, "ResizeForbidden")).To(BeEmpty())
			Eventually(func(g Gomega) {
				g.Expect(containerLimitMilli(g, refusedNamespace, "refused")).To(Equal(int64(1000)))
			}).Should(Succeed())
		})

		It("still names the ResourceQuota for a quota refusal", func() {
			applyManifest(fmt.Sprintf(`apiVersion: v1
kind: ResourceQuota
metadata:
  name: cpu-limits
  namespace: %s
spec:
  hard:
    limits.cpu: 500m
`, quotaNamespace))

			before, limitRangeBefore := resizeResultCount(resultLabelQuota), resizeResultCount(resultLabelLimitRange)
			// Admission refuses every pod until the quota controller has written status.
			Eventually(func() error {
				return tryApplyManifest(limitedPodManifest("over-budget", quotaNamespace, workerNode, 100, 200))
			}).WithTimeout(time.Minute).WithPolling(time.Second).Should(Succeed())

			Eventually(func() string {
				return eventNotes(quotaNamespace, "ResizeForbidden")
			}).WithTimeout(90 * time.Second).WithPolling(time.Second).Should(And(
				ContainSubstring("refused by a ResourceQuota"),
				ContainSubstring("exceeded quota"),
			))
			Expect(controllerLogLine(quotaNamespace + "/over-budget")).To(ContainSubstring("a ResourceQuota"))
			Expect(resizeResultCount(resultLabelQuota)).To(BeNumerically(">", before))
			Expect(resizeResultCount(resultLabelLimitRange)).To(Equal(limitRangeBefore))
			Expect(eventNotes(quotaNamespace, "ResizeLimitRangeDenied")).To(BeEmpty())
		})
	})
}

const (
	resultLabelLimitRange = "limitrange-denied"
	resultLabelQuota      = "quota-denied"
)

// limitedPodManifest is a single-container Burstable pause pod with both a CPU
// request and a CPU limit.
func limitedPodManifest(name, ns, node string, requestMilli, limitMilli int64) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  terminationGracePeriodSeconds: 1
  containers:
    - name: pause
      image: %s
      resources:
        requests:
          cpu: "%dm"
        limits:
          cpu: "%dm"
`, name, ns, node, pauseImage, requestMilli, limitMilli)
}

func applyLimitedPod(name, ns, node string, requestMilli, limitMilli int64) {
	applyManifest(limitedPodManifest(name, ns, node, requestMilli, limitMilli))
}

func limitRangeManifest(ns, name string, maxMilli int64) string {
	return fmt.Sprintf(`apiVersion: v1
kind: LimitRange
metadata:
  name: %s
  namespace: %s
spec:
  limits:
    - type: Container
      max:
        cpu: "%dm"
`, name, ns, maxMilli)
}

func tryApplyManifest(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

func applyManifest(manifest string) {
	Expect(tryApplyManifest(manifest)).To(Succeed())
}

// awaitAdmissionRefusal waits until the apiserver refuses a pod far over any max:
// LimitRanger reads through an informer, so a LimitRange is not enforced the moment
// it is created, and a resize racing that window would be admitted.
func awaitAdmissionRefusal(ns, wording string) {
	Eventually(func() string {
		out, _ := kubectl("run", "probe", "-n", ns, "--dry-run=server", "--image", pauseImage, "--restart=Never",
			`--overrides={"spec":{"containers":[{"name":"probe","image":"`+pauseImage+`","resources":{"requests":{"cpu":"1m"},"limits":{"cpu":"100"}}}]}}`)
		return out
	}).WithTimeout(time.Minute).WithPolling(time.Second).Should(ContainSubstring(wording))
}

// eventNotes returns the notes of a namespace's events/v1 events with this reason.
func eventNotes(ns, reason string) string {
	out, err := kubectl("get", "events.events.k8s.io", "-n", ns, "--field-selector", "reason="+reason,
		"-o", "jsonpath={.items[*].note}")
	Expect(err).NotTo(HaveOccurred())
	return out
}

// controllerLogLine returns the manager's resize-refusal log line for a pod key.
func controllerLogLine(podKey string) string {
	out, err := kubectl("logs", leaderPod(), "-n", namespace)
	Expect(err).NotTo(HaveOccurred())
	for line := range strings.Lines(out) {
		if strings.Contains(line, "resize refused") && strings.Contains(line, podKey) {
			return line
		}
	}
	return ""
}

// leaderPod names the manager replica holding the leader lease, the only one that
// reconciles: the other's log is silent on resizes and its counters stay at 0.
func leaderPod() string {
	out, err := kubectl("get", "lease", leaderLease, "-n", namespace, "-o", "jsonpath={.spec.holderIdentity}")
	Expect(err).NotTo(HaveOccurred())
	pod, _, found := strings.Cut(strings.TrimSpace(out), "_")
	Expect(found).To(BeTrue(), "lease holder %q has no pod_uuid form", out)
	return pod
}

var scrapeSeq int

// resizeResultCount scrapes headroom_resizes_total for one result from the leader's
// /metrics, through a one-shot pod like the curl-metrics spec's (the metrics
// NetworkPolicy admits only that namespace). It goes to the pod and not the Service,
// which could answer from the standby's zeros. Every result series is pre-created, so
// a missing one fails rather than reading as 0.
func resizeResultCount(result string) float64 {
	scrapeSeq++
	pod := fmt.Sprintf("curl-resize-results-%d", scrapeSeq)
	leaderIP, err := kubectl("get", "pod", leaderPod(), "-n", namespace, "-o", "jsonpath={.status.podIP}")
	Expect(err).NotTo(HaveOccurred())
	// The pod reads its own token, so the token stays out of the logged command.
	_, err = kubectl("run", pod, "--restart=Never", "--namespace", namespace, "--image=curlimages/curl:latest",
		"--overrides", fmt.Sprintf(`{"spec":{"serviceAccountName":%q,"containers":[{
			"name":"curl","image":"curlimages/curl:latest","command":["/bin/sh","-c"],
			"args":["for i in $(seq 1 30); do curl -sS -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s:8443/metrics && exit 0 || sleep 2; done; exit 1"],
			"securityContext":{"readOnlyRootFilesystem":true,"allowPrivilegeEscalation":false,
				"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"runAsUser":1000,
				"seccompProfile":{"type":"RuntimeDefault"}}}]}}`,
			serviceAccountName, strings.TrimSpace(leaderIP)))
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		_, _ = kubectl("delete", "pod", pod, "-n", namespace, "--ignore-not-found", "--wait=false")
	}()

	Eventually(func(g Gomega) string {
		return podPhase(g, namespace, pod)
	}).WithTimeout(3 * time.Minute).WithPolling(time.Second).Should(Or(Equal("Succeeded"), Equal("Failed")))

	out, err := kubectl("logs", pod, "-n", namespace)
	Expect(err).NotTo(HaveOccurred())
	Expect(podPhase(Default, namespace, pod)).To(Equal("Succeeded"), "scrape failed: %s", out)
	prefix := fmt.Sprintf(`headroom_resizes_total{result=%q} `, result)
	for line := range strings.Lines(out) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			count, err := strconv.ParseFloat(value, 64)
			Expect(err).NotTo(HaveOccurred())
			return count
		}
	}
	Fail(fmt.Sprintf("no %s series in /metrics", prefix))
	return 0
}
