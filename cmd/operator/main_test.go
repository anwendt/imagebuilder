package main

import (
	"fmt"
	"testing"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestUncachedClientObjectsProtectSecuritySensitiveReads(t *testing.T) {
	clusterScoped := objectTypes(uncachedClientObjects(false))
	for _, want := range []string{
		"*v1.Secret",
		"*v1.Namespace",
		"*v1.ValidatingWebhookConfiguration",
	} {
		if !clusterScoped[want] {
			t.Fatalf("cluster-scoped client must bypass the shared cache for %s", want)
		}
	}
	if clusterScoped["*v1alpha1.PlatformProvider"] {
		t.Fatal("cluster-scoped client must keep PlatformProviders in the shared cache")
	}

	namespaceScoped := objectTypes(uncachedClientObjects(true))
	for _, want := range []string{
		"*v1.Secret",
		"*v1.Namespace",
		"*v1.ValidatingWebhookConfiguration",
		"*v1alpha1.PlatformProvider",
		"*unstructured.Unstructured",
	} {
		if !namespaceScoped[want] {
			t.Fatalf("namespace-scoped client must bypass the shared cache for %s", want)
		}
	}
}

func objectTypes(objects []crclient.Object) map[string]bool {
	types := make(map[string]bool, len(objects))
	for _, object := range objects {
		types[fmt.Sprintf("%T", object)] = true
	}
	return types
}
