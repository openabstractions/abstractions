package main

import (
	"slices"
	"testing"

	hostdeclaration "github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// serve names the engine providers and asks the picture engines in
// product's names and order, which Setup's Engines list shares; the model
// host's own module names the same provider.
func TestServeUsesTheProductEngineNamesAndOrder(t *testing.T) {
	if modelHostName != product.ModelHost || hostdeclaration.ProviderName != product.ModelHost {
		t.Fatalf("model host names %q and %q, product %q", modelHostName, hostdeclaration.ProviderName, product.ModelHost)
	}
	if pytorchName != product.DiffusersModelsHost {
		t.Fatalf("pytorchName = %q, product %q", pytorchName, product.DiffusersModelsHost)
	}
	if !slices.Equal(pictureProviderPreference, product.PictureEngines()) {
		t.Fatalf("pictureProviderPreference = %q, product %q", pictureProviderPreference, product.PictureEngines())
	}
}
