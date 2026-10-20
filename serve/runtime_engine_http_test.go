package main

import (
	"encoding/json"
	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"path/filepath"
	"testing"
	"time"
)

func TestCanonicalProductObservationRequiresCompleteCurrentProducerMatch(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native product observation requires loopback Program proof")
	}
	engine := startGatewayEngine(t)
	useGatewayProductRecord(t, engine.home)
	p, err := openProviders(t.TempDir(), true, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	observations := p.productDeclarations()
	if len(observations) != 1 || observations[0].ProductPresence != "verified" || !identity.SameSubjectProgram(observations[0].ProductProgram, engine.program) {
		t.Fatalf("actual native product observation: %+v", observations)
	}
	for _, mode := range []string{"matching", "owner", "descriptor", "base", "accepted time", "operator", "producer generation", "expired", "unverified", "missing program"} {
		t.Run(mode, func(t *testing.T) {
			observed := observations[0]
			raw, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			var canonical providerFile
			if err := json.Unmarshal(raw, &canonical); err != nil {
				t.Fatal(err)
			}
			canonical.Source, canonical.Generation = declarationSourceProduct, "receiver-epoch"
			at := time.Now()
			switch mode {
			case "owner":
				canonical.DeclaredBy = "other-product"
			case "descriptor":
				canonical.Declaration.Resources = append(canonical.Declaration.Resources, "profile:other")
			case "base":
				canonical.Declaration.Host.Base += "/other"
			case "accepted time":
				canonical.DeclaredAt++
			case "operator":
				canonical.Source = declarationSourceOperator
			case "producer generation":
				observed.Generation = "other-producer-generation"
			case "expired":
				at = at.Add(-productDeclarationAge - time.Second)
			case "unverified":
				observed.ProductPresence = "unverified"
			case "missing program":
				observed.ProductProgram = ""
			}
			p.productsMu.Lock()
			p.product, p.productHas, p.productAt = []providerFile{observed}, true, at
			p.productsMu.Unlock()
			got := p.withCurrentProductObservations([]providerFile{canonical})[0]
			if got.Generation != canonical.Generation || (got.ProductProgram != "") != (mode == "matching") ||
				mode == "matching" && (got.ProductPresence != "verified" || !identity.SameSubjectProgram(got.ProductProgram, engine.program)) {
				t.Fatalf("product proof crossed descriptor/owner boundary: %+v", got)
			}
		})
	}
}

func TestRuntimeEngineHTTPRetainsCurrentProductObservationAcrossCanonicalRead(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native product observation requires loopback Program proof")
	}
	engine := startGatewayEngine(t)
	useGatewayProductRecord(t, engine.home)
	p, err := openProviders(t.TempDir(), true, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	check := func() {
		t.Helper()
		program, err := p.expectedEngineProgram(t.Context(), engine.base+"/api/v0/models")
		if err != nil || !identity.SameSubjectProgram(program, engine.program) {
			t.Fatalf("current observed engine image: %q %v", program, err)
		}
	}
	check()
	d := registration1459Directory(t, t.TempDir())
	p.registrationDirectory = func() *applicationDirectory { return d }
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestRuntimeEngineHTTPRequiresExistingVerifiedProgramFacts(t *testing.T) {
	file := newHostFile(wire.HostEntry{Name: "lmstudio", Kind: "lmstudio", Base: "http://127.0.0.1:12345"}, "fixture")
	file.Source, file.ProductPresence, file.ProductProgram = declarationSourceProduct, "verified", "/fixture/engine"
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), products: func() []providerFile { return []providerFile{file} }}
	if program, err := p.expectedEngineProgram(t.Context(), "http://127.0.0.1:12345/v1/models"); err != nil || program != file.ProductProgram {
		t.Fatal(program, err)
	}
	for _, change := range []func(){func() { file.ProductProgram = "" }, func() { file.ProductPresence = "unverified" }, func() { file.Source = declarationSourceOperator }} {
		file.Source, file.ProductPresence, file.ProductProgram = declarationSourceProduct, "verified", "/fixture/engine"
		change()
		p.forgetProducts()
		if program, err := p.expectedEngineProgram(t.Context(), "http://127.0.0.1:12345/v1/models"); err == nil || program != "" {
			t.Fatal("endpoint became authority", program, err)
		}
	}
}
