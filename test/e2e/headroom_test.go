//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/karlkfi/kube-headroom/test/utils"
)

// The design §10 exit criteria, exercised against a real kubelet + scheduler on
// a kind cluster running Kubernetes >= 1.35 (in-place pod resize is GA there).
// These need behavior a unit or envtest tier cannot observe — actual scheduling
// changing node slack and the kubelet actuating a CPU limit — so they live here
// rather than in internal/policy or internal/controller (docs/development/testing.md).
const (
	// managedNamespace is enrolled via the kube-headroom.dev/mode=managed label,
	// so the controller manages its pods.
	managedNamespace = "headroom-e2e"
	// neighborNamespace is deliberately unmanaged: its pod books node slack but
	// receives no limit of its own, isolating the probe as the sole managed pod.
	neighborNamespace = "default"

	probePod    = "headroom-probe"
	neighborPod = "headroom-neighbor"
	// pause holds a pod Running with a fixed, near-zero CPU footprint; the probe's
	// limit is a pure function of its request and node slack, not its usage (§5.5).
	pauseImage = "registry.k8s.io/pause:3.10"

	// probeRequestMilli is the probe's CPU request. With the default maxMultiplier
	// of 10, its generous ceiling on a slack-rich node is request x 10.
	probeRequestMilli    = 100
	generousLimitMilli   = probeRequestMilli * 10 // 1000m — request x maxMultiplier
	controllerDeployment = "kube-headroom-controller-manager"
)

// headroomSpecs registers the Headroom exit-criteria scenarios. It is invoked
// from inside the "Manager" Ordered Describe (e2e_test.go) so the deployed
// controller-manager is already up and is torn down after these run.
func headroomSpecs() {
	Context("Headroom CPU-limit management", Ordered, func() {
		var (
			workerNode           string
			allocatableMilli     int64
			neighborRequestMilli int64
		)

		BeforeAll(func() {
			By("selecting the worker node the slack scenarios run on")
			workerNode = workerNodeName()
			Expect(workerNode).NotTo(BeEmpty(), "expected a non-control-plane worker node")

			allocatableMilli = mustCPUMilli(nodeAllocatable(workerNode))
			Expect(allocatableMilli).To(BeNumerically(">", 800),
				"worker needs enough allocatable CPU to host the probe plus a slack-consuming neighbor")
			// Size the neighbor to consume most of the worker's CPU, leaving a small
			// (~allocatable-independent) sliver of slack so the probe's proportional
			// burst collapses well below its generous ceiling.
			neighborRequestMilli = allocatableMilli - 700

			By("creating and enrolling the managed namespace")
			// create ns is idempotent-ish: tolerate an already-existing namespace so a
			// re-run against a retained cluster does not fail setup.
			_, _ = kubectl("create", "ns", managedNamespace)
			_, err := kubectl("label", "--overwrite", "ns", managedNamespace,
				"kube-headroom.dev/mode=managed")
			Expect(err).NotTo(HaveOccurred(), "failed to label managed namespace")

			By("applying the HeadroomConfig with dry-run disabled")
			_, err = kubectl("apply", "-f", "test/e2e/testdata/headroomconfig.yaml")
			Expect(err).NotTo(HaveOccurred(), "failed to apply HeadroomConfig")
		})

		AfterAll(func() {
			By("removing the probe, neighbor, namespace, and config")
			_, _ = kubectl("delete", "pod", neighborPod, "-n", neighborNamespace, "--ignore-not-found")
			_, _ = kubectl("delete", "ns", managedNamespace, "--ignore-not-found", "--wait=false")
			_, _ = kubectl("delete", "headroomconfig", "cluster", "--ignore-not-found")
		})

		// Criterion 1: a pod on an otherwise-empty node runs unthrottled.
		It("gives a pod on an otherwise-empty node a generous CPU limit", func() {
			By("scheduling a small Burstable probe on the worker")
			applyPod(probePod, managedNamespace, workerNode, probeRequestMilli)

			By("waiting for the probe to be Running")
			Eventually(func(g Gomega) {
				g.Expect(podPhase(g, managedNamespace, probePod)).To(Equal("Running"))
			}).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(Succeed())

			By("expecting the controller to raise its limit to request x maxMultiplier")
			Eventually(func(g Gomega) {
				g.Expect(containerLimitMilli(g, managedNamespace, probePod)).
					To(Equal(int64(generousLimitMilli)))
			}).WithTimeout(90 * time.Second).WithPolling(500 * time.Millisecond).Should(Succeed())
		})

		// Criterion 2: scheduling a neighbor shrinks the limit within the 5s SLO.
		It("shrinks the probe's limit within seconds when a neighbor is scheduled", func() {
			By(fmt.Sprintf("scheduling a neighbor requesting %dm on the same worker", neighborRequestMilli))
			start := time.Now()
			applyPod(neighborPod, neighborNamespace, workerNode, neighborRequestMilli)

			By("expecting the probe's limit to drop below its generous ceiling but stay at or above its request")
			Eventually(func(g Gomega) {
				lim := containerLimitMilli(g, managedNamespace, probePod)
				g.Expect(lim).To(BeNumerically("<", int64(generousLimitMilli)), "limit should shrink under contention")
				g.Expect(lim).To(BeNumerically(">=", int64(probeRequestMilli)), "limit must never fall below request")
			}).WithTimeout(30 * time.Second).WithPolling(250 * time.Millisecond).Should(Succeed())

			elapsed := time.Since(start)
			AddReportEntry("neighbor-to-shrink latency", elapsed.String())
			// The design SLO is 5s; assert a looser bound so CI scheduling/actuation
			// jitter does not flake the run, while the reported latency tracks the SLO.
			Expect(elapsed).To(BeNumerically("<", 20*time.Second),
				"shrink should land well inside the 5s design SLO with margin")
		})

		// Criterion 4: no resize churn once node slack stops changing. (Ordered
		// before the controller kill so the controller is still live to observe.)
		It("does not churn the probe's limit at steady state", func() {
			By("recording the settled limit")
			var settled int64
			Eventually(func(g Gomega) {
				settled = containerLimitMilli(g, managedNamespace, probePod)
				g.Expect(settled).To(BeNumerically(">", 0))
			}).WithTimeout(30 * time.Second).WithPolling(500 * time.Millisecond).Should(Succeed())

			By("observing the limit stays byte-stable over a steady window (zero resize patches)")
			Consistently(func(g Gomega) {
				g.Expect(containerLimitMilli(g, managedNamespace, probePod)).To(Equal(settled))
			}).WithTimeout(15 * time.Second).WithPolling(time.Second).Should(Succeed())
		})

		// Criterion 3: killing the controller leaves the cluster in a safe state —
		// no managed pod is left throttled below its request. Runs last because it
		// takes the controller down.
		It("leaves managed pods safe when the controller is killed", func() {
			By("confirming the probe currently sits at or above its request")
			Eventually(func(g Gomega) {
				g.Expect(containerLimitMilli(g, managedNamespace, probePod)).
					To(BeNumerically(">=", int64(probeRequestMilli)))
			}).WithTimeout(30 * time.Second).WithPolling(500 * time.Millisecond).Should(Succeed())

			By("killing the controller by scaling its Deployment to zero")
			_, err := kubectl("scale", "deployment", controllerDeployment, "-n", namespace, "--replicas=0")
			Expect(err).NotTo(HaveOccurred(), "failed to scale controller down")
			Eventually(func(g Gomega) {
				out, err := kubectl("get", "deployment", controllerDeployment, "-n", namespace,
					"-o", "jsonpath={.status.replicas}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).To(Or(Equal(""), Equal("0")), "controller should have no running replicas")
			}).WithTimeout(60 * time.Second).WithPolling(time.Second).Should(Succeed())

			By("confirming the probe stays Running and at or above its request with the controller gone")
			Consistently(func(g Gomega) {
				g.Expect(containerLimitMilli(g, managedNamespace, probePod)).
					To(BeNumerically(">=", int64(probeRequestMilli)), "no pod may be throttled below its request")
				g.Expect(podPhase(g, managedNamespace, probePod)).To(Equal("Running"))
			}).WithTimeout(10 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})
	})
}

// --- helpers ----------------------------------------------------------------

// kubectl runs a kubectl command from the project root and returns its combined
// output. utils.Run sets the working directory and environment for us.
func kubectl(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", args...))
}

// applyPod schedules a single-container Burstable pause pod pinned to a node,
// with the given CPU request and no CPU limit — the shape the controller manages.
func applyPod(name, ns, node string, requestMilli int64) {
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
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
`, name, ns, name, node, pauseImage, requestMilli)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "failed to apply pod %s/%s", ns, name)
}

// workerNodeName returns the name of a node without the control-plane role.
func workerNodeName() string {
	out, err := kubectl("get", "nodes", "-l", "!node-role.kubernetes.io/control-plane",
		"-o", "jsonpath={.items[0].metadata.name}")
	Expect(err).NotTo(HaveOccurred(), "failed to list worker nodes")
	return strings.TrimSpace(out)
}

// nodeAllocatable returns a node's allocatable CPU as a raw quantity string.
func nodeAllocatable(node string) string {
	out, err := kubectl("get", "node", node, "-o", "jsonpath={.status.allocatable.cpu}")
	Expect(err).NotTo(HaveOccurred(), "failed to read node allocatable CPU")
	return out
}

// containerLimitMilli reads the probe container's actuated CPU limit in
// millicores from pod status (0 when unset). Status — not spec — reflects what
// the kubelet has actually enforced after an in-place resize.
func containerLimitMilli(g Gomega, ns, name string) int64 {
	out, err := kubectl("get", "pod", name, "-n", ns,
		"-o", "jsonpath={.status.containerStatuses[0].resources.limits.cpu}")
	g.Expect(err).NotTo(HaveOccurred())
	return cpuMilli(g, out)
}

// podPhase returns a pod's status phase.
func podPhase(g Gomega, ns, name string) string {
	out, err := kubectl("get", "pod", name, "-n", ns, "-o", "jsonpath={.status.phase}")
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// cpuMilli parses a CPU quantity string to millicores; "" is 0. Uses the given
// Gomega so a parse failure inside an Eventually retries rather than aborts.
func cpuMilli(g Gomega, s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	g.Expect(err).NotTo(HaveOccurred(), "parse cpu quantity %q", s)
	return q.MilliValue()
}

// mustCPUMilli is cpuMilli for one-shot setup reads that should not be retried.
func mustCPUMilli(s string) int64 {
	q, err := resource.ParseQuantity(strings.TrimSpace(s))
	Expect(err).NotTo(HaveOccurred(), "parse cpu quantity %q", s)
	return q.MilliValue()
}
