/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"testing"

	asocontainerservicev1 "github.com/Azure/azure-service-operator/v2/api/containerservice/v1api20230201"
	asoresourcesv1 "github.com/Azure/azure-service-operator/v2/api/resources/v1api20200601"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/mock_azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/scope"
	"sigs.k8s.io/cluster-api-provider-azure/internal/test"
	"sigs.k8s.io/cluster-api-provider-azure/util/reconciler"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClusterToAzureManagedControlPlane(t *testing.T) {
	tests := []struct {
		name            string
		controlPlaneRef *corev1.ObjectReference
		expected        []ctrl.Request
	}{
		{
			name:            "nil",
			controlPlaneRef: nil,
			expected:        nil,
		},
		{
			name: "bad kind",
			controlPlaneRef: &corev1.ObjectReference{
				Kind: "NotAzureManagedControlPlane",
			},
			expected: nil,
		},
		{
			name: "ok",
			controlPlaneRef: &corev1.ObjectReference{
				Kind:      infrav1.AzureManagedControlPlaneKind,
				Name:      "name",
				Namespace: "namespace",
			},
			expected: []ctrl.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "name",
						Namespace: "namespace",
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			actual := (&AzureManagedControlPlaneReconciler{}).ClusterToAzureManagedControlPlane(context.TODO(), &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ControlPlaneRef: test.controlPlaneRef,
				},
			})
			if test.expected == nil {
				g.Expect(actual).To(BeNil())
			} else {
				g.Expect(actual).To(Equal(test.expected))
			}
		})
	}
}

func TestAzureManagedControlPlaneReconcilePaused(t *testing.T) {
	g := NewWithT(t)

	ctx := context.Background()

	sb := runtime.NewSchemeBuilder(
		clusterv1.AddToScheme,
		infrav1.AddToScheme,
		asoresourcesv1.AddToScheme,
		asocontainerservicev1.AddToScheme,
	)
	s := runtime.NewScheme()
	g.Expect(sb.AddToScheme(s)).To(Succeed())
	c := fake.NewClientBuilder().
		WithScheme(s).
		Build()

	recorder := record.NewFakeRecorder(1)

	reconciler := &AzureManagedControlPlaneReconciler{
		Client:                                   c,
		Recorder:                                 recorder,
		ReconcileTimeout:                         reconciler.DefaultLoopTimeout,
		WatchFilterValue:                         "",
		getNewAzureManagedControlPlaneReconciler: newAzureManagedControlPlaneReconciler,
	}
	name := test.RandomName("paused", 10)
	namespace := "default"

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: clusterv1.ClusterSpec{
			Paused: true,
		},
	}
	g.Expect(c.Create(ctx, cluster)).To(Succeed())

	instance := &infrav1.AzureManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       cluster.Name,
				},
			},
		},
		Spec: infrav1.AzureManagedControlPlaneSpec{
			AzureManagedControlPlaneClassSpec: infrav1.AzureManagedControlPlaneClassSpec{
				SubscriptionID: "something",
			},
			ResourceGroupName: name,
		},
	}
	g.Expect(c.Create(ctx, instance)).To(Succeed())

	rg := &asoresourcesv1.ResourceGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	g.Expect(c.Create(ctx, rg)).To(Succeed())

	mc := &asocontainerservicev1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	g.Expect(c.Create(ctx, mc)).To(Succeed())

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{
			Namespace: instance.Namespace,
			Name:      instance.Name,
		},
	})

	g.Expect(err).To(BeNil())
	g.Expect(result.RequeueAfter).To(BeZero())
}

func TestAzureManagedControlPlaneReconcileNormal(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	cp := &infrav1.AzureManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fake-azmp",
			Namespace: "fake-ns",
		},
		Spec: infrav1.AzureManagedControlPlaneSpec{
			AzureManagedControlPlaneClassSpec: infrav1.AzureManagedControlPlaneClassSpec{
				Version: "0.0.1",
			},
		},
		Status: infrav1.AzureManagedControlPlaneStatus{
			Ready:       false,
			Initialized: false,
		},
	}
	scheme, err := newScheme()
	g.Expect(err).ToNot(HaveOccurred())

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cp).WithStatusSubresource(cp).Build()
	amcpr := &AzureManagedControlPlaneReconciler{
		Client: client,
	}

	helper, err := patch.NewHelper(cp, client)
	g.Expect(err).ToNot(HaveOccurred())

	scopes := &scope.ManagedControlPlaneScope{
		Cluster: &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "fake-cluster",
				Namespace: "fake-ns",
			},
		},
		Client:       client,
		PatchHelper:  helper,
		ControlPlane: cp,
	}
	scopes.SetAdminKubeconfigData(createFakeKubeConfig())
	scopes.SetUserKubeconfigData(createFakeKubeConfig())

	amcpr.getNewAzureManagedControlPlaneReconciler = func(scope *scope.ManagedControlPlaneScope) (*azureManagedControlPlaneService, error) {
		ctrlr := gomock.NewController(t)
		svcr := mock_azure.NewMockServiceReconciler(ctrlr)
		svcr.EXPECT().Reconcile(gomock.Any()).Return(nil)

		return &azureManagedControlPlaneService{
			kubeclient: scope.Client,
			scope:      scope,
			services: []azure.ServiceReconciler{
				svcr,
			},
		}, nil
	}

	_, err = amcpr.reconcileNormal(ctx, scopes)
	g.Expect(err).To(HaveOccurred())
}

func createFakeKubeConfig() []byte {
	return []byte(`
  apiVersion: v1
  kind: Config
  clusters:
  - cluster:
      certificate-authority-data: UEhPTlkK
      server: https://1.1.1.1
    name: production
  contexts:
  - context:
      cluster: production
      user: production
    name: production
  current-context: production`)
}
