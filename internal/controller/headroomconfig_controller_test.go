package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

// fakeManagedCounts is a managedCountSource stub so the config reconciler can be
// tested without a running node reconciler.
type fakeManagedCounts struct{ pods, nodes int }

func (f fakeManagedCounts) ManagedCounts() (int, int) { return f.pods, f.nodes }

var _ = Describe("HeadroomConfig Controller", func() {
	Context("When reconciling a resource", func() {
		// HeadroomConfig is a cluster-scoped singleton; the only valid name is "cluster".
		const resourceName = kubeheadroomv1alpha1.SingletonName

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		headroomconfig := &kubeheadroomv1alpha1.HeadroomConfig{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind HeadroomConfig")
			err := k8sClient.Get(ctx, typeNamespacedName, headroomconfig)
			if err != nil && errors.IsNotFound(err) {
				resource := &kubeheadroomv1alpha1.HeadroomConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &kubeheadroomv1alpha1.HeadroomConfig{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance HeadroomConfig")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("populates status: ObservedGeneration, counts, and a Ready condition", func() {
			controllerReconciler := &HeadroomConfigReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling with no managed-state source (counts default to zero)")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &kubeheadroomv1alpha1.HeadroomConfig{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
			Expect(updated.Status.ManagedPods).To(Equal(int32(0)))
			Expect(updated.Status.ManagedNodes).To(Equal(int32(0)))
			Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, conditionReady)).To(BeTrue())
			ready := meta.FindStatusCondition(updated.Status.Conditions, conditionReady)
			Expect(ready.ObservedGeneration).To(Equal(updated.Generation))
			Expect(ready.Reason).To(Equal(reasonReconciled))

			By("Reconciling with a managed-state source reporting live counts")
			controllerReconciler.ManagedState = fakeManagedCounts{pods: 3, nodes: 2}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.ManagedPods).To(Equal(int32(3)))
			Expect(updated.Status.ManagedNodes).To(Equal(int32(2)))
		})
	})
})
