package main

import (
	"context"
	"errors"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

const catalogueHostSource = "operator_catalogue"

type catalogueHostRecord struct {
	Reference string              `json:"reference"`
	Source    string              `json:"source"`
	Image     applicationV3Source `json:"image"`
}

func validCatalogueHosts(hosts map[string]catalogueHostRecord) bool {
	if len(hosts) > 16 {
		return false
	}
	for name, record := range hosts {
		official, ok := product.OfficialAgentProgram(name)
		if !ok || record.Source != catalogueHostSource || !agentHex(record.Reference, 24) ||
			!record.Image.valid() || !record.Image.signed() || record.Image.Code.Subject != official.Signer {
			return false
		}
	}
	return true
}

// holdCatalogueHost resolves the program through the compiled catalogue in
// the native account folder. Only the image owner supplies the held proof.
func holdCatalogueHost(ctx context.Context, peer fileid.Peer, name, localData string, open func(context.Context, fileid.Peer, string) (*heldApplicationManifest, error)) (*heldApplicationManifest, applicationV3Source, error) {
	official, ok := product.OfficialAgentProgram(name)
	if !ok || !filepath.IsAbs(localData) {
		return nil, applicationV3Source{}, errors.New("applications: unknown catalogue host")
	}
	program := filepath.Join(localData, filepath.FromSlash(official.LocalAppData))
	held, err := open(ctx, peer, program)
	if err != nil {
		return nil, applicationV3Source{}, err
	}
	image := applicationV3Source{Program: identity.NormalizeSubjectProgram(applicationV3HeldProgram(held)), FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
	if !image.valid() || !image.signed() || image.Code.Subject != official.Signer || !identity.SameSubjectProgram(image.Program, program) || held.Recheck(ctx) != nil {
		return nil, applicationV3Source{}, errors.Join(errors.New("applications: catalogue host image mismatch"), held.Close())
	}
	return held, image, nil
}

// holdKeptCatalogueHost reopens the exact approved host image without
// publishing or repairing registration. The caller supplies the reference
// from a current native enrollment capture and owns Close.
func holdKeptCatalogueHost(ctx context.Context, peer fileid.Peer, name, localData, reference string, record catalogueHostRecord, open func(context.Context, fileid.Peer, string) (*heldApplicationManifest, error)) (*heldApplicationManifest, error) {
	if reference == "" || reference != record.Reference || record.Source != catalogueHostSource {
		return nil, errors.New("applications: kept catalogue host reference changed")
	}
	held, image, err := holdCatalogueHost(ctx, peer, name, localData, open)
	if err != nil {
		return nil, err
	}
	if image != record.Image {
		return nil, errors.Join(errors.New("applications: kept catalogue host image changed"), held.Close())
	}
	return held, nil
}

// withKeptCatalogueEffect retains the original host handle until the exact
// effect returns. snapshot must reread native enrollment and target identity
// without taking a Book lock across dispatch.
func withKeptCatalogueEffect(ctx context.Context, held *heldApplicationManifest, expected keptCatalogueEffectIdentity, snapshot func(context.Context) (keptCatalogueEffectIdentity, error), effect func() error) error {
	if held == nil || snapshot == nil || effect == nil || expected.host == "" || expected.enrollment == "" || expected.target == "" {
		return errors.New("applications: kept effect proof unavailable")
	}
	current, err := snapshot(ctx)
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("applications: kept effect identity changed")
	}
	if err := held.Recheck(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return effect()
}

type keptCatalogueEffectIdentity struct {
	host, enrollment, target string
}
