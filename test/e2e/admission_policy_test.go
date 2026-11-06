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

	"github.com/karlkfi/kube-headroom/test/utils"
)

// The optional require-cpu-requests ValidatingAdmissionPolicy (runbook
// #require-cpu-requests) closes the §6.3 escape where a request-less container in
// a managed pod runs uncapped. Its CEL only means anything to a real apiserver,
// so it is exercised here with server-side dry-run creates; nothing schedules.
const (
	admissionPolicyDir = "config/samples/admission-policy"
	policyNamespace    = "headroom-e2e-policy"
	policyName         = "kube-headroom-require-cpu-requests"

	levelMemory     = "{limits: {memory: 128Mi}}"
	levelCPURequest = "{requests: {cpu: 300m}}"
	levelCPULimit   = "{limits: {cpu: \"2\"}}"
	levelEmpty      = "{}"
)

// admissionPolicySpecs registers the admission-policy scenarios. Like
// headroomSpecs it is invoked from inside the "Manager" Ordered Describe.
func admissionPolicySpecs() {
	Context("optional require-cpu-requests admission policy", Ordered, func() {
		BeforeAll(func() {
			By("applying the sample policy and binding")
			_, err := kubectl("apply", "-k", admissionPolicyDir)
			Expect(err).NotTo(HaveOccurred(), "failed to apply the admission policy sample")

			By("creating and enrolling a namespace for the policy checks")
			_, _ = kubectl("create", "ns", policyNamespace)
			_, err = kubectl("label", "--overwrite", "ns", policyNamespace, "kube-headroom.dev/mode=managed")
			Expect(err).NotTo(HaveOccurred(), "failed to label the policy namespace")
		})

		AfterAll(func() {
			By("removing the policy and its namespace")
			_, _ = kubectl("delete", "-k", admissionPolicyDir, "--ignore-not-found")
			_, _ = kubectl("delete", "ns", policyNamespace, "--ignore-not-found", "--wait=false")
		})

		It("rejects a managed pod whose sidecar has no CPU request", func() {
			// The binding takes effect asynchronously, so retry until it denies.
			Eventually(func(g Gomega) {
				out, err := dryRunSidecarPod("dind-no-request", policyNamespace, "", "")
				g.Expect(err).To(HaveOccurred(), "pod should be denied, got: %s", out)
				g.Expect(out).To(ContainSubstring(policyName))
				g.Expect(out).To(ContainSubstring("missing on: dind"))
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})

		It("admits the same pod once the sidecar requests CPU", func() {
			out, err := dryRunSidecarPod("dind-with-request", policyNamespace, "", "100m")
			Expect(err).NotTo(HaveOccurred(), out)
		})

		It("admits a request-less sidecar in an opted-out pod", func() {
			out, err := dryRunSidecarPod("dind-opted-out", policyNamespace, "unmanaged", "")
			Expect(err).NotTo(HaveOccurred(), out)
		})

		It("admits a request-less sidecar outside managed namespaces", func() {
			out, err := dryRunSidecarPod("dind-unmanaged-ns", neighborNamespace, "", "")
			Expect(err).NotTo(HaveOccurred(), out)
		})

		// Headroom never caps a pod with pod-level resources, so it needs a ceiling of its own.
		DescribeTable("pod-level resources without a CPU ceiling",
			func(mode, level string, jobLimit, sidecarLimit, wantDenied string) {
				out, err := dryRunPodLevelPod("pod-level", mode, level, jobLimit, sidecarLimit)
				if wantDenied == "" {
					Expect(err).NotTo(HaveOccurred(), out)
					return
				}
				Expect(err).To(HaveOccurred(), "pod should be denied, got: %s", out)
				Expect(out).To(ContainSubstring(wantDenied))
			},
			Entry("rejects memory only, naming both containers", "", levelMemory, "", "", "a CPU limit on: job, dind"),
			Entry("rejects memory only, naming the unlimited container", "", levelMemory, "1", "", "a CPU limit on: dind"),
			Entry("rejects CPU request only", "", levelCPURequest, "1", "", "a CPU limit on: dind"),
			Entry("admits a limit on every container", "", levelMemory, "1", "200m", ""),
			Entry("admits a pod-level CPU limit", "", levelCPULimit, "", "", ""),
			Entry("admits an empty block, which Headroom still manages", "", levelEmpty, "", "", ""),
			Entry("admits an opted-out pod", "unmanaged", levelMemory, "", "", ""),
		)
	})
}

// dryRunPodLevelPod server-side dry-run creates a pod in the policy namespace with
// pod-level resources level, and a job container and "dind" sidecar that request
// CPU and carry the given CPU limits ("" for none).
func dryRunPodLevelPod(name, mode, level, jobLimit, sidecarLimit string) (string, error) {
	labels := ""
	if mode != "" {
		labels = fmt.Sprintf("\n  labels:\n    kube-headroom.dev/mode: %s", mode)
	}
	resources := func(limit string) string {
		if limit == "" {
			return "{requests: {cpu: 100m}}"
		}
		return fmt.Sprintf("{requests: {cpu: 100m}, limits: {cpu: %q}}", limit)
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s%s
spec:
  resources: %s
  initContainers:
    - name: dind
      image: %s
      restartPolicy: Always
      resources: %s
  containers:
    - name: job
      image: %s
      resources: %s
`, name, policyNamespace, labels, level, pauseImage, resources(sidecarLimit), pauseImage, resources(jobLimit))

	cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	return utils.Run(cmd)
}

// dryRunSidecarPod server-side dry-run creates a pod shaped like the §6.3 escape:
// a job container with a CPU request plus a restartable-init "dind" sidecar whose
// CPU request is sidecarRequest ("" for none). mode, when set, is the pod's
// kube-headroom.dev/mode label. Returns kubectl's combined output and error.
func dryRunSidecarPod(name, ns, mode, sidecarRequest string) (string, error) {
	labels := ""
	if mode != "" {
		labels = fmt.Sprintf("\n  labels:\n    kube-headroom.dev/mode: %s", mode)
	}
	sidecarResources := ""
	if sidecarRequest != "" {
		sidecarResources = fmt.Sprintf("\n      resources:\n        requests:\n          cpu: %q", sidecarRequest)
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s%s
spec:
  initContainers:
    - name: dind
      image: %s
      restartPolicy: Always%s
  containers:
    - name: job
      image: %s
      resources:
        requests:
          cpu: "1"
`, name, ns, labels, pauseImage, sidecarResources, pauseImage)

	cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	return utils.Run(cmd)
}
