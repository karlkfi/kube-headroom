package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// These exercise the CRD's CEL ceilings on the multiplier fields (Q18): the
// apiserver must reject a config that would seed/allow an unbounded CPU limit,
// and accept one at the ceiling or using the "0" disable sentinel.
var _ = Describe("HeadroomConfig multiplier validation", func() {
	ctx := context.Background()
	name := types.NamespacedName{Name: kubeheadroomv1alpha1.SingletonName}

	newConfig := func(mutate func(*kubeheadroomv1alpha1.HeadroomConfigSpec)) *kubeheadroomv1alpha1.HeadroomConfig {
		hc := &kubeheadroomv1alpha1.HeadroomConfig{
			ObjectMeta: metav1.ObjectMeta{Name: kubeheadroomv1alpha1.SingletonName},
		}
		if mutate != nil {
			mutate(&hc.Spec)
		}
		return hc
	}

	AfterEach(func() {
		// Only the accepted case persists; delete it if present.
		hc := &kubeheadroomv1alpha1.HeadroomConfig{}
		if err := k8sClient.Get(ctx, name, hc); err == nil {
			Expect(k8sClient.Delete(ctx, hc)).To(Succeed())
		}
	})

	It("rejects an initialMultiplier above 100", func() {
		hc := newConfig(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			s.Webhook.InitialMultiplier = resource.MustParse("1000")
		})
		Expect(k8sClient.Create(ctx, hc)).NotTo(Succeed())
	})

	It("rejects a maxMultiplier above 100", func() {
		hc := newConfig(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			s.MaxMultiplier = resource.MustParse("1000")
		})
		Expect(k8sClient.Create(ctx, hc)).NotTo(Succeed())
	})

	It("accepts multipliers at the ceiling and the '0' disable sentinel", func() {
		hc := newConfig(func(s *kubeheadroomv1alpha1.HeadroomConfigSpec) {
			s.Webhook.InitialMultiplier = resource.MustParse("100")
			s.MaxMultiplier = resource.MustParse("0")
		})
		Expect(k8sClient.Create(ctx, hc)).To(Succeed())
	})
})
