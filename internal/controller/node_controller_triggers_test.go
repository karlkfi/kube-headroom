package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

const (
	refusedPod = "refused-pod"
	maxCPU300m = "300m"
)

// reconcileCount is how many node reconciles this process has finished.
func reconcileCount() uint64 {
	var m dto.Metric
	Expect(reconcileDuration.Write(&m)).To(Succeed())
	return m.GetHistogram().GetSampleCount()
}

var _ = Describe("Node reconcile triggers outside scheduling (§6.2)", func() {
	DescribeTable("podUpdateRelevant",
		func(mutate func(*corev1.Pod), want bool) {
			// A kubelet-reported allocation keeps the booking still through an Infeasible
			// resize, which would otherwise pass on the booking alone.
			allocated := []corev1.ContainerStatus{{
				Name:               cApp,
				AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			}}
			before, after := resizeErrPod("p"), resizeErrPod("p")
			before.Status.ContainerStatuses, after.Status.ContainerStatuses = allocated, allocated
			mutate(after)
			Expect(podUpdateRelevant(event.UpdateEvent{ObjectOld: before, ObjectNew: after})).To(Equal(want))
			Expect(podUpdateRelevant(event.UpdateEvent{ObjectOld: after, ObjectNew: before})).To(Equal(want))
		},
		Entry("opt-out label", func(p *corev1.Pod) {
			p.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
		}, true),
		Entry("max-cpu annotation", func(p *corev1.Pod) {
			p.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationMaxCPU: maxCPU300m}
		}, true),
		Entry("owner reference", func(p *corev1.Pod) {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs"}}
		}, true),
		Entry("Infeasible resize", func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: corev1.PodReasonInfeasible,
			}}
		}, true),
		Entry("the controller's own status annotation", func(p *corev1.Pod) {
			p.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationStatus: `{"factor":"2.00"}`}
		}, false),
		Entry("a max-cpu annotation that does not parse", func(p *corev1.Pod) {
			p.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationMaxCPU: "lots"}
		}, false),
		Entry("a mode label other than unmanaged", func(p *corev1.Pod) {
			p.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeManaged}
		}, false),
		Entry("an unrelated label", func(p *corev1.Pod) {
			p.Labels = map[string]string{"app": "x"}
		}, false),
	)

	DescribeTable("nodeUpdateRelevant",
		func(mutate func(*corev1.Node), want bool) {
			before, after := resizeErrNode(), resizeErrNode()
			mutate(after)
			Expect(nodeUpdateRelevant(event.UpdateEvent{ObjectOld: before, ObjectNew: after})).To(Equal(want))
		},
		Entry("allocatable CPU", func(n *corev1.Node) {
			n.Status.Allocatable[corev1.ResourceCPU] = resource.MustParse("4")
		}, true),
		Entry("a label, which excludedNodeSelector reads", func(n *corev1.Node) {
			n.Labels = map[string]string{"pool": "batch"}
		}, true),
		Entry("a heartbeat", func(n *corev1.Node) {
			n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, LastHeartbeatTime: metav1.Now()}}
		}, false),
	)

	Describe("backoff requeue", func() {
		const backoffPlusDebounce = defaultBackoffPeriod + defaultDebouncePeriod

		// pinTime freezes the clock and centers the jitter, so a requeue reads exactly.
		pinTime := func(r *NodeReconciler) {
			now := time.Now()
			r.now = func() time.Time { return now }
			r.randFloat = func() float64 { return 0.5 }
		}

		DescribeTable("requeues at the window's end for every refusal that backs off",
			func(wording string) {
				refusal := apierrors.NewForbidden(schema.GroupResource{Resource: resourcePods}, refusedPod, errors.New(wording))
				r, _ := newResizeErrReconciler(refusedPod, refusal)
				pinTime(r)

				res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(backoffPlusDebounce))
			},
			Entry("quota-denied", "exceeded quota: cpu-limits"),
			Entry("limitrange-denied", "maximum cpu usage per Container is 50m"),
			Entry("forbidden", "denied by a webhook"),
		)

		It("requeues for a pod the kubelet marked Infeasible", func() {
			r, _ := newResizeErrReconciler("settled", nil)
			pinTime(r)
			infeasible := resizeErrPod("infeasible")
			infeasible.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: corev1.PodReasonInfeasible,
			}}
			Expect(r.Create(ctx, infeasible)).To(Succeed())

			res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(backoffPlusDebounce))
		})

		It("aims at the first window to end", func() {
			r, _ := newResizeErrReconciler("settled", nil)
			pinTime(r)
			r.backoff.Store(nsA+"/settled", r.clock().Add(30*time.Second))
			r.backoff.Store(nsA+"/not-on-this-node", r.clock().Add(5*time.Second))

			res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(30*time.Second + defaultDebouncePeriod))
		})

		// Only an ineligible pod keeps an ended window: inBackoff clears it for the rest.
		It("does not requeue for a window that has already ended", func() {
			r, _ := newResizeErrReconciler("settled", nil)
			pinTime(r)
			optedOut := resizeErrPod("opted-out")
			optedOut.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
			Expect(r.Create(ctx, optedOut)).To(Succeed())
			r.backoff.Store(nsA+"/opted-out", r.clock().Add(-time.Second))

			res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.IsZero()).To(BeTrue())
		})

		It("requeues for a window that ends while the reconcile is running", func() {
			r, _ := newResizeErrReconciler("settled", nil)
			pinTime(r)
			start := r.clock()
			r.backoff.Store(nsA+"/settled", start.Add(time.Second))
			// The namespace read follows the pod's backoff verdict.
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Namespace); ok {
						r.now = func() time.Time { return start.Add(time.Minute) }
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})

			res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Second + defaultDebouncePeriod))
		})

		DescribeTable("which pods hold a share rather than get a target",
			func(prepare func(*NodeReconciler, *corev1.Pod), wantManaged, wantHolds bool) {
				r, _ := newResizeErrReconciler("settled", nil)
				pinTime(r)
				pod := resizeErrPod("p")
				prepare(r, pod)
				cfg, err := r.loadConfig(ctx)
				Expect(err).NotTo(HaveOccurred())

				inputs, _, err := r.buildInputs(ctx, cfg, []corev1.Pod{*pod}, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(inputs[0].Managed).To(Equal(wantManaged))
				Expect(inputs[0].HoldsShare).To(Equal(wantHolds))
			},
			Entry("no refusal", func(*NodeReconciler, *corev1.Pod) {}, true, false),
			Entry("in backoff", func(r *NodeReconciler, p *corev1.Pod) { r.setBackoff(p) }, false, true),
			Entry("newly Infeasible", func(_ *NodeReconciler, p *corev1.Pod) {
				p.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.PodResizePending, Status: corev1.ConditionTrue, Reason: corev1.PodReasonInfeasible,
				}}
			}, false, true),
			Entry("in backoff in a namespace that is not enrolled", func(r *NodeReconciler, p *corev1.Pod) {
				p.Namespace = "not-enrolled"
				r.setBackoff(p)
			}, false, false),
		)

		It("leaves a pod holding a share out of node_managed_pods", func() {
			r, _ := newResizeErrReconciler("settled", nil)
			pinTime(r)
			held := resizeErrPod("held")
			Expect(r.Create(ctx, held)).To(Succeed())
			r.setBackoff(held)

			_, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.ToFloat64(nodeManagedPods.WithLabelValues(errNodeName))).To(Equal(1.0))
		})

		It("leaves the sooner rate-limit requeue in charge", func() {
			refusal := apierrors.NewForbidden(schema.GroupResource{Resource: resourcePods}, refusedPod, errors.New("exceeded quota"))
			conflict := apierrors.NewConflict(schema.GroupResource{Resource: resourcePods}, "stale", errors.New("modified"))
			r, _ := newPerPodResizeReconciler(func(pod string) error {
				if pod == refusedPod {
					return refusal
				}
				return conflict
			}, refusedPod, "stale")

			res, err := r.Reconcile(ctx, reconcile.Request{Name: errNodeName})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Second))
		})
	})

	// These run the watches through a manager against a real apiserver. There is no
	// kubelet, so a resize lands in spec and never changes pod status. The namespace
	// selector keeps earlier specs' leftover pods out of the patch counts, though a
	// config change still reconciles their nodes.
	Context("through a running manager", Ordered, func() {
		const (
			podsPerNode  = 6
			pps          = 2
			enrollLabel  = "trigger-enrolled"
			excludeLabel = "trigger-excluded"
			managedNS    = "trigger-managed"
			lateNS       = "trigger-late"
			refusedNS    = "trigger-refused"
			labelOn      = "yes"
		)
		var (
			nodeA, nodeB string
			stop         context.CancelFunc
			stopped      chan struct{}
		)

		applied := func() float64 { return testutil.ToFloat64(resizesTotal.WithLabelValues(resultApplied)) }
		denied := func() float64 { return testutil.ToFloat64(resizesTotal.WithLabelValues(resultLimitRangeDenied)) }
		nodeCount := func() uint64 {
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			return uint64(len(nodes.Items))
		}
		enrolledNamespace := func(name string) *corev1.Namespace {
			return &corev1.Namespace{Name: name, Labels: map[string]string{enrollLabel: labelOn}}
		}
		getPod := func(name string) *corev1.Pod {
			var pod corev1.Pod
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: managedNS, Name: name}, &pod)).To(Succeed())
			return &pod
		}
		awaitLimit := func(ns, name string, milli int64) {
			Eventually(func() int64 { return podLimitMilli(ns, name) }).
				WithTimeout(15 * time.Second).WithPolling(50 * time.Millisecond).Should(Equal(milli))
		}
		// awaitQuiet waits out a window with no reconcile, which a controller that
		// retriggers itself never reaches. The window is several debounces long.
		awaitQuiet := func() {
			previous := reconcileCount() + 1
			Eventually(func() bool {
				current := reconcileCount()
				stable := current == previous
				previous = current
				return stable
			}).WithTimeout(20 * time.Second).WithPolling(500 * time.Millisecond).Should(BeTrue())
		}
		expectNoReconcile := func() {
			before := reconcileCount()
			Consistently(reconcileCount).WithTimeout(time.Second).WithPolling(50 * time.Millisecond).Should(Equal(before))
		}

		BeforeAll(func() {
			nodeA, nodeB = nextNode(), nextNode()
			makeNode(nodeA, 8)
			makeNode(nodeB, 8)
			Expect(k8sClient.Create(ctx, enrolledNamespace(managedNS))).To(Succeed())
			Expect(k8sClient.Create(ctx, enrolledNamespace(refusedNS))).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Namespace{Name: lateNS})).To(Succeed())
			applyConfigWith(true, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
				spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{enrollLabel: labelOn}}
				spec.ExcludedNodeSelector = &metav1.LabelSelector{MatchLabels: map[string]string{excludeLabel: labelOn}}
				spec.Quantum = resource.MustParse("10m")
				spec.MaxMultiplier = resource.MustParse("10") // every pod here tops out at 1000m
				spec.RateLimits.PerNodePatchesPerSecond = pps
			})
			for i := range podsPerNode {
				makeBurstablePod(managedNS, fmt.Sprintf("a-%d", i), nodeA, 100, 0)
				makeBurstablePod(managedNS, fmt.Sprintf("b-%d", i), nodeB, 100, 0)
			}

			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:                 scheme.Scheme,
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
			Expect(err).NotTo(HaveOccurred())
			r := &NodeReconciler{
				Client:         mgr.GetClient(),
				Scheme:         mgr.GetScheme(),
				DebouncePeriod: 100 * time.Millisecond,
				BackoffPeriod:  2 * time.Second,
			}
			Expect(r.SetupWithManager(mgr)).To(Succeed())

			var mgrCtx context.Context
			mgrCtx, stop = context.WithCancel(ctx)
			stopped = make(chan struct{})
			go func() {
				defer close(stopped)
				defer GinkgoRecover()
				Expect(mgr.Start(mgrCtx)).To(Succeed())
			}()
			Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
			awaitQuiet()
		})

		AfterAll(func() {
			stop()
			Eventually(stopped).WithTimeout(30 * time.Second).Should(BeClosed())
			_ = k8sClient.Delete(ctx, &kubeheadroomv1alpha1.HeadroomConfig{Name: kubeheadroomv1alpha1.SingletonName})
		})

		It("spreads a dryRun flip at the per-node rate rather than patching every pod at once", func() {
			total := float64(2 * podsPerNode)
			before := applied()
			start := time.Now()
			applyConfigWith(false, nil)

			Eventually(func() float64 {
				landed := applied() - before
				// A full bucket of pps tokens per node, then pps more per second.
				budget := 2 * pps * (1 + time.Since(start).Seconds())
				Expect(landed).To(BeNumerically("<=", budget), "patches outran the per-node bucket")
				return landed
			}).WithTimeout(30 * time.Second).WithPolling(20 * time.Millisecond).Should(Equal(total))
			// (podsPerNode-pps)/pps seconds of refill at the least, allowing timer slop.
			Expect(time.Since(start)).To(BeNumerically(">=", 1500*time.Millisecond))
			awaitLimit(managedNS, "a-0", 1000)
			awaitLimit(managedNS, "b-0", 1000)
		})

		It("reconciles each node once and writes nothing for a config change that moves no target", func() {
			awaitQuiet()
			nodes, reconciles, patches := nodeCount(), reconcileCount(), applied()
			version := getPod("a-0").ResourceVersion

			applyConfigWith(false, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
				spec.DebouncePeriod = metav1.Duration{Duration: 3 * time.Second}
			})
			Eventually(func() uint64 { return reconcileCount() - reconciles }).
				WithTimeout(10 * time.Second).WithPolling(20 * time.Millisecond).Should(Equal(nodes))
			awaitQuiet()
			Expect(reconcileCount() - reconciles).To(Equal(nodes))
			Expect(applied()).To(Equal(patches))
			Expect(getPod("a-0").ResourceVersion).To(Equal(version))
		})

		It("does not reconcile on the controller's own writes or on anything it does not read", func() {
			awaitQuiet()

			var hc kubeheadroomv1alpha1.HeadroomConfig
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kubeheadroomv1alpha1.SingletonName}, &hc)).To(Succeed())
			hc.Status.ManagedPods = 99
			Expect(k8sClient.Status().Update(ctx, &hc)).To(Succeed())
			expectNoReconcile()

			pod := getPod("a-0")
			pod.Annotations[kubeheadroomv1alpha1.AnnotationStatus] = `{"factor":"9.99"}`
			pod.Labels = map[string]string{"app": "x"}
			Expect(k8sClient.Update(ctx, pod)).To(Succeed())
			expectNoReconcile()

			var ns corev1.Namespace
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: managedNS}, &ns)).To(Succeed())
			ns.Annotations = map[string]string{"note": "x"}
			Expect(k8sClient.Update(ctx, &ns)).To(Succeed())
			expectNoReconcile()
		})

		It("applies and lifts max-cpu on a running pod", func() {
			awaitQuiet()
			pod := getPod("a-1")
			pod.Annotations[kubeheadroomv1alpha1.AnnotationMaxCPU] = maxCPU300m
			Expect(k8sClient.Update(ctx, pod)).To(Succeed())
			awaitLimit(managedNS, "a-1", 300)

			pod = getPod("a-1")
			delete(pod.Annotations, kubeheadroomv1alpha1.AnnotationMaxCPU)
			Expect(k8sClient.Update(ctx, pod)).To(Succeed())
			awaitLimit(managedNS, "a-1", 1000)
		})

		It("stops managing a running pod that opts out", func() {
			awaitQuiet()
			managed := testutil.ToFloat64(nodeManagedPods.WithLabelValues(nodeA))
			pod := getPod("a-2")
			pod.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
			Expect(k8sClient.Update(ctx, pod)).To(Succeed())

			Eventually(func() float64 { return testutil.ToFloat64(nodeManagedPods.WithLabelValues(nodeA)) }).
				WithTimeout(10 * time.Second).Should(Equal(managed - 1))
		})

		It("enrolls a namespace on only the nodes holding its pods", func() {
			makeBurstablePod(lateNS, "late", nodeA, 100, 0)
			awaitQuiet()
			Expect(podLimitMilli(lateNS, "late")).To(BeZero())
			reconciles := reconcileCount()

			var ns corev1.Namespace
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lateNS}, &ns)).To(Succeed())
			ns.Labels[enrollLabel] = labelOn
			Expect(k8sClient.Update(ctx, &ns)).To(Succeed())

			awaitLimit(lateNS, "late", 1000)
			awaitQuiet()
			Expect(reconcileCount()-reconciles).To(Equal(uint64(1)), "only the late pod's node holds the namespace")
		})

		It("applies and lifts a LimitRange on a running pod", func() {
			awaitQuiet()
			Expect(k8sClient.Create(ctx, &corev1.LimitRange{
				Name: "cap", Namespace: lateNS,
				Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				}}},
			})).To(Succeed())
			awaitLimit(lateNS, "late", 500)

			Expect(k8sClient.Delete(ctx, &corev1.LimitRange{Name: "cap", Namespace: lateNS})).To(Succeed())
			awaitLimit(lateNS, "late", 1000)
		})

		It("applies a node label to excludedNodeSelector", func() {
			awaitQuiet()
			var node corev1.Node
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeB}, &node)).To(Succeed())
			node.Labels = map[string]string{excludeLabel: labelOn}
			Expect(k8sClient.Update(ctx, &node)).To(Succeed())

			Eventually(func() float64 { return testutil.ToFloat64(nodeManagedPods.WithLabelValues(nodeB)) }).
				WithTimeout(10 * time.Second).Should(BeZero())
		})

		It("recomputes but never touches a pod with pod-level resources when its settings change", func() {
			podLevel := "pod-level"
			Expect(k8sClient.Create(ctx, &corev1.Pod{
				Name: podLevel, Namespace: managedNS,
				Spec: corev1.PodSpec{
					NodeName:   nodeA,
					Containers: []corev1.Container{qosContainer(cApp, 100, 1000, 64, 64)},
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					},
				},
			})).To(Succeed())
			awaitQuiet()

			for _, change := range []func(*corev1.Pod){
				func(p *corev1.Pod) {
					p.Annotations = map[string]string{kubeheadroomv1alpha1.AnnotationMaxCPU: maxCPU300m}
				},
				func(p *corev1.Pod) {
					p.Labels = map[string]string{kubeheadroomv1alpha1.LabelMode: kubeheadroomv1alpha1.ModeUnmanaged}
				},
			} {
				reconciles, patches := reconcileCount(), applied()
				pod := getPod(podLevel)
				change(pod)
				Expect(k8sClient.Update(ctx, pod)).To(Succeed())

				Eventually(reconcileCount).WithTimeout(10 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">", reconciles))
				awaitQuiet()
				Expect(applied()).To(Equal(patches))
				Expect(podLimitMilli(managedNS, podLevel)).To(Equal(int64(1000)))
				Expect(getPod(podLevel).Annotations).NotTo(HaveKey(kubeheadroomv1alpha1.AnnotationStatus))
			}
		})

		It("retries a refused pod at each window's end, and no sooner, with no other event", func() {
			awaitQuiet()
			makeBurstablePod(refusedNS, refusedPod, nodeA, 100, 1000)
			Expect(k8sClient.Create(ctx, &corev1.LimitRange{
				Name: "late", Namespace: refusedNS,
				Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
					Type: corev1.LimitTypeContainer,
					Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
				}}},
			})).To(Succeed())

			before := denied()
			Eventually(denied).WithTimeout(15 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">", before))
			first := denied()
			start := time.Now()
			Eventually(denied).WithTimeout(15 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">", first))
			Expect(time.Since(start)).To(BeNumerically(">=", time.Second), "a retry must wait out the 2s window")

			Expect(k8sClient.Delete(ctx, &corev1.Pod{Name: refusedPod, Namespace: refusedNS}, client.GracePeriodSeconds(0))).To(Succeed())
		})

		// Each window's end reconciles with a different pod in backoff. 18 CPUs keeps
		// the burst floor from hiding the difference: 3600m with both in the basis,
		// 4250m or 7500m with one out.
		It("holds a neighbor's limit still while two pods refused out of step keep retrying", func() {
			awaitQuiet()
			applyConfigWith(false, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
				spec.DebouncePeriod = metav1.Duration{Duration: 100 * time.Millisecond}
			})
			awaitQuiet()

			node := nextNode()
			makeNode(node, 18)
			refusals := []struct {
				ns, name string
				req      int64
			}{{"trigger-refused-a", "a", 1000}, {"trigger-refused-b", "b", 3000}}
			for _, p := range refusals {
				Expect(k8sClient.Create(ctx, enrolledNamespace(p.ns))).To(Succeed())
				makeBurstablePod(p.ns, p.name, node, p.req, 0)
			}
			makeBurstablePod(managedNS, "neighbor", node, 1000, 0)
			awaitLimit(managedNS, "neighbor", 3600)
			awaitLimit(refusals[1].ns, refusals[1].name, 10800)

			for _, p := range refusals {
				before := denied()
				Expect(k8sClient.Create(ctx, &corev1.LimitRange{
					Name: "refuses", Namespace: p.ns,
					Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
						Type: corev1.LimitTypeContainer,
						Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
					}}},
				})).To(Succeed())
				Eventually(denied).WithTimeout(15 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">", before))
				time.Sleep(time.Second) // half the 2s window, so the two stay out of step
			}

			patches, retries := applied(), denied()
			Consistently(func() int64 { return podLimitMilli(managedNS, "neighbor") }).
				WithTimeout(7 * time.Second).WithPolling(50 * time.Millisecond).Should(Equal(int64(3600)))
			Expect(applied()).To(Equal(patches))
			Expect(denied()-retries).To(BeNumerically(">=", 3), "both pods must have kept retrying")

			for _, p := range refusals {
				Expect(k8sClient.Delete(ctx, &corev1.Pod{Name: p.name, Namespace: p.ns}, client.GracePeriodSeconds(0))).To(Succeed())
			}
			Expect(k8sClient.Delete(ctx, &corev1.Pod{Name: "neighbor", Namespace: managedNS}, client.GracePeriodSeconds(0))).To(Succeed())
		})

		It("reconciles every node when the config is deleted, since dry-run is then the default", func() {
			awaitQuiet()
			nodes, reconciles := nodeCount(), reconcileCount()
			Expect(k8sClient.Delete(ctx, &kubeheadroomv1alpha1.HeadroomConfig{Name: kubeheadroomv1alpha1.SingletonName})).To(Succeed())
			Eventually(func() uint64 { return reconcileCount() - reconciles }).
				WithTimeout(10 * time.Second).WithPolling(20 * time.Millisecond).Should(BeNumerically(">=", nodes))
		})
	})
})
