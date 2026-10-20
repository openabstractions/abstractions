package main

import (
	"encoding/json"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

func TestProviderExtensionInventoryValidationAndRoundTrip(t *testing.T) {
	base := providerDeclaration{Name: "image-provider", Program: absoluteFixtureProgram(), Endpoint: "image-provider",
		Transport: transportNative, Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image"},
		Models: []string{"image-model"}, Activation: wire.ActivationOnDemand.String()}
	ext := providerExtension{Profile: "image", Key: "vendor/steps"}
	with := func(entries ...providerExtension) providerDeclaration {
		d := base
		d.SupportedExtensions = entries
		return d
	}
	for _, tc := range []struct {
		name string
		d    providerDeclaration
		want string
	}{
		{"none", base, ""},
		{"declared", with(ext), ""},
		{"wrong profile", with(providerExtension{Profile: "chat", Key: ext.Key}), "supported_extensions"},
		{"missing contract", with(providerExtension{Profile: "embed", Key: ext.Key}), "supported_extensions"},
		{"duplicate", with(ext, ext), "supported_extensions"},
		{"no owner", with(providerExtension{Profile: "image", Key: "/steps"}), "supported_extensions"},
		{"no name", with(providerExtension{Profile: "image", Key: "vendor/"}), "supported_extensions"},
		{"reserved claim", with(providerExtension{Profile: "image", Key: inference.ClaimExtension}), "supported_extensions"},
		{"reserved namespace", with(providerExtension{Profile: "image", Key: inference.ReservedExtensionPrefix + "steps"}), "supported_extensions"},
		{"reserved namespace beside vendor key", with(ext, providerExtension{Profile: "image", Key: "openabstractions/other"}), "supported_extensions"},
		{"vendor key sharing the word", with(providerExtension{Profile: "image", Key: "openabstractions-vendor/steps"}), ""},
		{"unknown profile", with(providerExtension{Profile: "imaginary", Key: ext.Key}), "supported_extensions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validProviderDeclaration(tc.d); got != tc.want {
				t.Fatalf("invalid field = %q, want %q", got, tc.want)
			}
		})
	}
	embed := base
	embed.Contracts = []string{inference.EmbedContract}
	embed.Resources = []string{"profile:embed"}
	embed.SupportedExtensions = []providerExtension{{Profile: "embed", Key: "vendor/shape"}}
	if got := validProviderDeclaration(embed); got != "" {
		t.Fatalf("embed profile and contract refused: %s", got)
	}
	embed.Contracts = []string{inference.Contract}
	if got := validProviderDeclaration(embed); got != "supported_extensions" {
		t.Fatalf("chat contract claimed embed extension: %s", got)
	}
	d := with(ext)
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var stored providerDeclaration
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	got := declarationOf(stored.wire())
	got.SupportedExtensions = extensionsOf(stored.wireExtensions())
	if len(got.SupportedExtensions) != 1 || got.SupportedExtensions[0] != ext {
		t.Fatalf("registration lost extension support: %+v", got.SupportedExtensions)
	}
	if (providerFile{Declaration: d}).bindingID() == (providerFile{Declaration: base}).bindingID() {
		t.Fatal("extension inventory did not change registration binding")
	}
}
