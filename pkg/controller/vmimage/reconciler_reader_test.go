package vmimage

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDirectReaderPrefersAPIReaderForSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	cachedClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	directClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "imagebuilder-system"},
	}).Build()
	r := &VMImageReconciler{Client: cachedClient, APIReader: directClient}

	secret := &corev1.Secret{}
	if err := r.directReader().Get(context.Background(), types.NamespacedName{
		Name: "credentials", Namespace: "imagebuilder-system",
	}, secret); err != nil {
		t.Fatalf("direct reader did not read Secret: %v", err)
	}
}
