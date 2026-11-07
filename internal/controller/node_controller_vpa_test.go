package controller

import (
	"context"
	"strings"
	"time"

	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
	"github.com/karlkfi/kube-headroom/internal/vpa"
)

var vpaGVK = schema.GroupVersionKind{Group: "autoscaling.k8s.io", Version: "v1", Kind: "VerticalPodAutoscaler"}

// vpaCRD is the served shape of the real CRD with its schema left open. The real
// schema is exercised on kind, because these specs only need the API to come and go.
func vpaCRD() *apiextensionsv1.CustomResourceDefinition {
	open := true
	return &apiextensionsv1.CustomResourceDefinition{
		Name:        "verticalpodautoscalers.autoscaling.k8s.io",
		Annotations: map[string]string{"api-approved.kubernetes.io": "https://github.com/kubernetes/kubernetes/pull/63797"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: vpaGVK.Group,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "verticalpodautoscalers", Singular: "verticalpodautoscaler",
				Kind: vpaGVK.Kind, ListKind: vpaGVK.Kind + "List",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: vpaGVK.Version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object", XPreserveUnknownFields: &open,
				}},
			}},
		},
	}
}

// These run a manager and the Poller against a real apiserver, so the VPA API is
// really absent, really appears and is really removed. There is no kubelet, so a
// resize lands in spec. Ordered, because each spec moves the API's state.
var _ = Describe("Pods under a limit-managing VPA (§6.3)", Ordered, func() {
	const (
		enrollLabel = "vpa-enrolled"
		ns          = "vpa-managed"
		deployment  = "web"
		// The replicaSet name is what the Deployment controller would generate.
		replicaSet = deployment + "-6d4cf56db6"
		underVPA   = "under-vpa"
		neighbor   = "neighbor"
		sleeper    = "sleeper"
		vpaObject  = "web-vpa"
		maxTarget  = 1000 // 10 × the 100m requests
	)
	var (
		node    string
		rec     *events.FakeRecorder
		poller  *vpa.Poller
		stop    context.CancelFunc
		stopped chan struct{}
		crd     = envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{vpaCRD()}}
	)

	awaitLimit := func(name string, milli int64) {
		GinkgoHelper()
		Eventually(func() int64 { return podLimitMilli(ns, name) }).
			WithTimeout(15 * time.Second).WithPolling(50 * time.Millisecond).Should(Equal(milli))
	}
	warnings := func() []string {
		var out []string
		for {
			select {
			case e := <-rec.Events:
				if strings.Contains(e, reasonVPAManagesLimit) {
					out = append(out, e)
				}
			default:
				return out
			}
		}
	}
	vpaWith := func(mode string, controlledValues string) *unstructured.Unstructured {
		spec := map[string]any{
			"targetRef":    map[string]any{"kind": "Deployment", "name": deployment},
			"updatePolicy": map[string]any{"updateMode": mode},
		}
		if controlledValues != "" {
			spec["resourcePolicy"] = map[string]any{"containerPolicies": []any{map[string]any{
				"containerName": "*", "controlledValues": controlledValues,
			}}}
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
		u.SetGroupVersionKind(vpaGVK)
		u.SetNamespace(ns)
		u.SetName(vpaObject)
		return u
	}
	setVPA := func(mode string, controlledValues string) {
		GinkgoHelper()
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(vpaGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: vpaObject}, current)).To(Succeed())
		current.Object["spec"] = vpaWith(mode, controlledValues).Object["spec"]
		Expect(k8sClient.Update(ctx, current)).To(Succeed())
	}
	loggedState := func() string {
		var line string
		sink := funcr.New(func(_, args string) { line = args }, funcr.Options{})
		probe := vpa.NewPoller(k8sClient)
		probe.Refresh(logf.IntoContext(ctx, sink))
		return line
	}

	BeforeAll(func() {
		node = nextNode()
		makeNode(node, 8)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{Name: ns, Labels: map[string]string{enrollLabel: "yes"}})).To(Succeed())
		applyConfigWith(false, func(spec *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{enrollLabel: "yes"}}
			spec.Quantum = resource.MustParse("10m")
			spec.MaxMultiplier = resource.MustParse("10")
		})
		makeBurstablePod(ns, underVPA, node, 100, 0)
		makeBurstablePod(ns, neighbor, node, 100, 0)
		var pod corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: underVPA}, &pod)).To(Succeed())
		yes := true
		pod.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet, UID: "rs-uid", Controller: &yes,
		}}
		Expect(k8sClient.Update(ctx, &pod)).To(Succeed())

		skipNameValidation := true // the trigger specs run a node controller in this process too
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Controller:             ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())
		poller = vpa.NewPoller(mgr.GetAPIReader())
		poller.Period = 100 * time.Millisecond
		Expect(mgr.Add(poller)).To(Succeed())
		rec = events.NewFakeRecorder(256)
		r := &NodeReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			Recorder:       rec,
			DebouncePeriod: 100 * time.Millisecond,
			VPAs:           poller,
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
	})

	AfterAll(func() {
		stop()
		Eventually(stopped).WithTimeout(30 * time.Second).Should(BeClosed())
		_ = envtest.UninstallCRDs(cfg, crd)
		_ = k8sClient.Delete(ctx, &kubeheadroomv1alpha1.HeadroomConfig{Name: kubeheadroomv1alpha1.SingletonName})
	})

	It("manages every pod on a cluster without the CRD", func() {
		Expect(loggedState()).To(ContainSubstring(`"state"="absent"`))
		awaitLimit(underVPA, maxTarget)
		awaitLimit(neighbor, maxTarget)
		Expect(warnings()).To(BeEmpty())
	})

	It("picks up a CRD installed later and leaves a RequestsOnly VPA's pods managed", func() {
		_, err := envtest.InstallCRDs(cfg, crd)
		Expect(err).NotTo(HaveOccurred())
		Expect(loggedState()).To(ContainSubstring(`"state"="served"`))

		Expect(k8sClient.Create(ctx, vpaWith("InPlaceOrRecreate", "RequestsOnly"))).To(Succeed())
		Eventually(func() int { return len(poller.ForNamespace(ns)) }).
			WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(Equal(1))
		Consistently(warnings).WithTimeout(time.Second).WithPolling(100 * time.Millisecond).Should(BeEmpty())
		Expect(testutil.ToFloat64(nodeManagedPods.WithLabelValues(node))).To(Equal(2.0))
	})

	It("skips the pod on a quiet node, warning once, when the VPA starts writing limits", func() {
		setVPA("InPlaceOrRecreate", "")

		var seen []string
		Eventually(func() []string {
			seen = append(seen, warnings()...)
			return seen
		}).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(HaveLen(1))
		Expect(seen[0]).To(ContainSubstring(vpaObject))
		Expect(testutil.ToFloat64(nodeManagedPods.WithLabelValues(node))).To(Equal(1.0))
	})

	It("leaves the skipped pod's limit alone, still counts it toward slack, and does not warn again", func() {
		slack := testutil.ToFloat64(nodeSlackCores.WithLabelValues(node))
		var before corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: underVPA}, &before)).To(Succeed())
		Expect(slack).To(BeNumerically("~", 7.8, 0.001), "both 100m requests book the 8-core node")

		// A booking large enough to bring the neighbor's target well down.
		makeBurstablePod(ns, sleeper, node, 7700, 0)
		Eventually(func() int64 { return podLimitMilli(ns, neighbor) }).
			WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(BeNumerically("<", maxTarget))
		Expect(podLimitMilli(ns, underVPA)).To(Equal(int64(maxTarget)))
		var after corev1.Pod
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: underVPA}, &after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion), "a skipped pod gets no write at all")
		Expect(testutil.ToFloat64(nodeSlackCores.WithLabelValues(node))).To(BeNumerically("~", 0.1, 0.001))
		Expect(warnings()).To(BeEmpty())
	})

	It("takes the pod back when the VPA goes to recommendation mode", func() {
		setVPA("Off", "")

		Eventually(func() int64 { return podLimitMilli(ns, underVPA) }).
			WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(BeNumerically("<", maxTarget))
	})

	It("keeps running and manages the pod when the CRD is removed under a limit-managing VPA", func() {
		setVPA("Auto", "")
		Eventually(warnings).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).ShouldNot(BeEmpty())

		Expect(envtest.UninstallCRDs(cfg, crd)).To(Succeed())
		Eventually(func() int { return len(poller.ForNamespace(ns)) }).
			WithTimeout(30 * time.Second).WithPolling(50 * time.Millisecond).Should(BeZero())
		Eventually(loggedState).WithTimeout(30 * time.Second).WithPolling(200 * time.Millisecond).
			Should(ContainSubstring(`"state"="absent"`))
		Expect(stopped).NotTo(BeClosed())
		Eventually(func() float64 { return testutil.ToFloat64(nodeManagedPods.WithLabelValues(node)) }).
			WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(Equal(3.0))
	})
})

var _ = Describe("Reconciling before the first VPA list", func() {
	It("waits, because every pod would look free of VPAs", func() {
		r := &NodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), VPAs: vpa.NewPoller(k8sClient)}
		res, err := r.Reconcile(ctx, reconcile.Request{Name: "any-node"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(time.Second))

		r.VPAs.Refresh(ctx)
		res, err = r.Reconcile(ctx, reconcile.Request{Name: "any-node"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeZero())
	})
})
