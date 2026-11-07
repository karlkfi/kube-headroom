//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	quietManagedNamespace  = "headroom-e2e-quiet"
	quietEnrolledNamespace = "headroom-e2e-enroll"
	quietBackoffNamespace  = "headroom-e2e-backoff"

	quietPod         = "quiet"
	enrolledPod      = "enrolled"
	scraperPod       = "metrics-scraper"
	metricReconciles = "headroom_reconcile_duration_seconds_count"

	// quietSettleWindow is longer than the e2e config's largest jittered debounce
	// (1.5s) plus an actuation, and shorter than any backoff window.
	quietSettleWindow = 8 * time.Second
	// defaultBackoff mirrors defaultBackoffPeriod in the node reconciler.
	defaultBackoff = 60 * time.Second
)

// quietNodeSpecs covers design §6.2's non-scheduling triggers: each spec changes a
// setting while its pods are already running, with no pod created or deleted until
// the effect has landed. Metrics come through kubectl exec into a long-lived pod,
// because the one-shot scrape in resizeResultCount is itself a pod event. It runs
// inside the "Manager" Ordered Describe and removes its pods before headroomSpecs.
func quietNodeSpecs() {
	Context("settings changed on quiet nodes", Ordered, func() {
		var workerNode string

		BeforeAll(func() {
			workerNode = workerNodeName()
			Expect(workerNode).NotTo(BeEmpty(), "expected a non-control-plane worker node")

			for _, ns := range []string{quietManagedNamespace, quietEnrolledNamespace, quietBackoffNamespace} {
				_, _ = kubectl("create", "ns", ns)
			}
			for _, ns := range []string{quietManagedNamespace, quietBackoffNamespace} {
				_, err := kubectl("label", "--overwrite", "ns", ns, "kube-headroom.dev/mode=managed")
				Expect(err).NotTo(HaveOccurred(), "failed to label namespace %s", ns)
			}

			_, err := kubectl("apply", "-f", "test/e2e/testdata/headroomconfig.yaml")
			Expect(err).NotTo(HaveOccurred(), "failed to apply HeadroomConfig")
			_, err = kubectl("patch", "hcfg", "cluster", "--type", "merge", "-p", `{"spec":{"dryRun":true}}`)
			Expect(err).NotTo(HaveOccurred(), "failed to set dryRun")

			startScraper()
		})

		AfterAll(func() {
			for _, ns := range []string{quietManagedNamespace, quietEnrolledNamespace, quietBackoffNamespace} {
				_, _ = kubectl("delete", "ns", ns, "--ignore-not-found")
			}
			_, _ = kubectl("delete", "pod", scraperPod, "-n", namespace, "--ignore-not-found")
			_, _ = kubectl("delete", "headroomconfig", "cluster", "--ignore-not-found")
		})

		It("applies dryRun: false to a running pod", func() {
			applyPod(quietPod, quietManagedNamespace, workerNode, probeRequestMilli)
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, quietManagedNamespace, quietPod)).To(Equal("Running"))
				g.Expect(podStatusAnnotation(g, quietManagedNamespace, quietPod)).To(ContainSubstring(`"dryRun":true`))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())
			awaitQuiet()
			Expect(containerLimitMilli(Default, quietManagedNamespace, quietPod)).NotTo(Equal(int64(generousLimitMilli)))
			reconciles := leaderMetric(metricReconciles)

			_, err := kubectl("patch", "hcfg", "cluster", "--type", "merge", "-p", `{"spec":{"dryRun":false}}`)
			Expect(err).NotTo(HaveOccurred())
			awaitLimit(quietManagedNamespace, quietPod, generousLimitMilli)
			// The final spec's unchanged count is only evidence if this one can move.
			Expect(leaderMetric(metricReconciles)).To(BeNumerically(">", reconciles))
		})

		It("manages a namespace's running pods once it is enrolled", func() {
			applyPod(enrolledPod, quietEnrolledNamespace, workerNode, probeRequestMilli)
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, quietEnrolledNamespace, enrolledPod)).To(Equal("Running"))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())
			awaitQuiet()
			Expect(containerLimitMilli(Default, quietEnrolledNamespace, enrolledPod)).To(BeZero())

			_, err := kubectl("label", "ns", quietEnrolledNamespace, "kube-headroom.dev/mode=managed")
			Expect(err).NotTo(HaveOccurred())
			awaitLimit(quietEnrolledNamespace, enrolledPod, generousLimitMilli)
		})

		It("applies max-cpu to a running pod and lifts it again", func() {
			awaitQuiet()
			_, err := kubectl("annotate", "pod", quietPod, "-n", quietManagedNamespace, "kube-headroom.dev/max-cpu=300m")
			Expect(err).NotTo(HaveOccurred())
			awaitLimit(quietManagedNamespace, quietPod, 300)

			awaitQuiet()
			_, err = kubectl("annotate", "pod", quietPod, "-n", quietManagedNamespace, "kube-headroom.dev/max-cpu-")
			Expect(err).NotTo(HaveOccurred())
			awaitLimit(quietManagedNamespace, quietPod, generousLimitMilli)
		})

		// An opted-out pod keeps its limit, so the sign is its neighbor's share basis.
		It("stops counting a running pod that opts out", func() {
			awaitQuiet()
			Expect(podStatusAnnotation(Default, quietManagedNamespace, quietPod)).To(ContainSubstring(`"managedRequests":"200m"`))

			_, err := kubectl("label", "pod", enrolledPod, "-n", quietEnrolledNamespace, "kube-headroom.dev/mode=unmanaged")
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				g.Expect(podStatusAnnotation(g, quietManagedNamespace, quietPod)).To(ContainSubstring(`"managedRequests":"100m"`))
			}).WithTimeout(30 * time.Second).WithPolling(time.Second).Should(Succeed())
			Expect(containerLimitMilli(Default, quietEnrolledNamespace, enrolledPod)).To(Equal(int64(generousLimitMilli)))
		})

		It("applies a LimitRange to a running pod and lifts it again", func() {
			awaitQuiet()
			applyManifest(limitRangeManifest(quietManagedNamespace, "quiet-cap", 500))
			awaitLimit(quietManagedNamespace, quietPod, 500)

			awaitQuiet()
			_, err := kubectl("delete", "limitrange", "quiet-cap", "-n", quietManagedNamespace)
			Expect(err).NotTo(HaveOccurred())
			awaitLimit(quietManagedNamespace, quietPod, generousLimitMilli)
		})

		It("retries a refused pod when its backoff expires", func() {
			refused := "refused"
			applyLimitedPod(refused, quietBackoffNamespace, workerNode, probeRequestMilli, generousLimitMilli)
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, quietBackoffNamespace, refused)).To(Equal("Running"))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())
			DeferCleanup(func() {
				_, _ = kubectl("delete", "pod", refused, "-n", quietBackoffNamespace, "--ignore-not-found")
			})

			before := leaderMetric(`headroom_resizes_total{result="limitrange-denied"}`)
			applyManifest(limitRangeManifest(quietBackoffNamespace, "late", 50))
			awaitAdmissionRefusal(quietBackoffNamespace, "maximum cpu usage per Container is 50m")

			var firstRefusal time.Time
			var refusals float64
			Eventually(func(g Gomega) {
				refusals = leaderMetric(`headroom_resizes_total{result="limitrange-denied"}`)
				g.Expect(refusals).To(BeNumerically(">", before))
				firstRefusal = time.Now()
			}).WithTimeout(90 * time.Second).WithPolling(time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(leaderMetric(`headroom_resizes_total{result="limitrange-denied"}`)).To(BeNumerically(">", refusals))
			}).WithTimeout(defaultBackoff + 30*time.Second).WithPolling(time.Second).Should(Succeed())
			Expect(time.Since(firstRefusal)).To(BeNumerically(">", defaultBackoff/2), "a retry must wait out the backoff")
		})

		// A write the controller makes must not bring it back: reconciles and pod writes
		// both stop once the triggers above have settled.
		It("stays quiet once settled", func() {
			awaitQuiet()
			reconciles := leaderMetric(metricReconciles)
			versions := func() string {
				quiet, err := kubectl("get", "pod", quietPod, "-n", quietManagedNamespace, "-o", "jsonpath={.metadata.resourceVersion}")
				Expect(err).NotTo(HaveOccurred())
				enrolled, err := kubectl("get", "pod", enrolledPod, "-n", quietEnrolledNamespace, "-o", "jsonpath={.metadata.resourceVersion}")
				Expect(err).NotTo(HaveOccurred())
				return quiet + "/" + enrolled
			}
			settled := versions()

			time.Sleep(30 * time.Second)
			Expect(versions()).To(Equal(settled), "nothing may write to a settled pod")
			Expect(leaderMetric(metricReconciles)).To(Equal(reconciles), "nothing may reconcile a settled node")
		})
	})
}

// startScraper runs the pod quiet specs read /metrics through. Its namespace and
// service account are the ones the metrics NetworkPolicy and RBAC admit.
func startScraper() {
	_, err := kubectl("run", scraperPod, "--restart=Never", "--namespace", namespace, "--image=curlimages/curl:latest",
		"--overrides", fmt.Sprintf(`{"spec":{"serviceAccountName":%q,"terminationGracePeriodSeconds":1,"containers":[{
			"name":"curl","image":"curlimages/curl:latest","command":["sleep","3600"],
			"securityContext":{"readOnlyRootFilesystem":true,"allowPrivilegeEscalation":false,
				"capabilities":{"drop":["ALL"]},"runAsNonRoot":true,"runAsUser":1000,
				"seccompProfile":{"type":"RuntimeDefault"}}}]}}`, serviceAccountName))
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(g Gomega) {
		g.Expect(podPhase(g, namespace, scraperPod)).To(Equal("Running"))
	}).WithTimeout(3 * time.Minute).WithPolling(time.Second).Should(Succeed())
}

// leaderMetric reads one unlabeled-or-fully-labeled series from the leader's /metrics
// through the scraper pod. A missing series fails rather than reading as 0.
func leaderMetric(series string) float64 {
	leaderIP, err := kubectl("get", "pod", leaderPod(), "-n", namespace, "-o", "jsonpath={.status.podIP}")
	Expect(err).NotTo(HaveOccurred())
	out, err := kubectl("exec", scraperPod, "-n", namespace, "--", "sh", "-c",
		fmt.Sprintf(`curl -sS -k -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" https://%s:8443/metrics`,
			strings.TrimSpace(leaderIP)))
	Expect(err).NotTo(HaveOccurred())
	for line := range strings.Lines(out) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), series+" "); ok {
			count, err := strconv.ParseFloat(value, 64)
			Expect(err).NotTo(HaveOccurred())
			return count
		}
	}
	Fail(fmt.Sprintf("no %s series in /metrics", series))
	return 0
}

// awaitQuiet waits until the leader has gone a settle window without reconciling,
// which a controller retriggering itself never does.
func awaitQuiet() {
	previous := -1.0
	Eventually(func(g Gomega) {
		current := leaderMetric(metricReconciles)
		stable := current == previous
		previous = current
		g.Expect(stable).To(BeTrue(), "reconciles still moving: %v", current)
	}).WithTimeout(2 * time.Minute).WithPolling(quietSettleWindow).Should(Succeed())
}

func awaitLimit(ns, pod string, limitMilli int64) {
	Eventually(func(g Gomega) {
		g.Expect(containerLimitMilli(g, ns, pod)).To(Equal(limitMilli))
	}).WithTimeout(90 * time.Second).WithPolling(time.Second).Should(Succeed())
}

func podStatusAnnotation(g Gomega, ns, pod string) string {
	out, err := kubectl("get", "pod", pod, "-n", ns, "-o", `jsonpath={.metadata.annotations.kube-headroom\.dev/status}`)
	g.Expect(err).NotTo(HaveOccurred())
	return out
}
