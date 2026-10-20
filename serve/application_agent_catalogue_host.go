package main

import (
	"context"
	"errors"
	"path/filepath"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

var catalogueHostParty = product.InstalledAgentConnectionParty()
var catalogueAgentApplication = nativecli.AgentApplicationForParty(catalogueHostParty)

func (d *applicationDirectory) prepareCatalogueHost(ctx context.Context, image applicationAgentImage) (catalogueHostRecord, func() error, func(), error) {
	config, err := layout.SetupConfig()
	if err != nil {
		return catalogueHostRecord{}, nil, nil, err
	}
	dir := filepath.Join(config, "agent-integrations")
	program := filepath.Join(dir, catalogueAgentApplication+".exe")
	if !identity.SameSubjectProgram(program, image.Program) || privatedir.CheckDir(dir) != nil {
		return catalogueHostRecord{}, nil, nil, errors.New("applications: connector is outside the private Setup destination")
	}
	held, err := openHeldApplicationImage(ctx, fileid.Self, program)
	if err != nil {
		return catalogueHostRecord{}, nil, nil, err
	}
	fail := func(err error) (catalogueHostRecord, func() error, func(), error) {
		return catalogueHostRecord{}, nil, nil, errors.Join(err, held.Close())
	}
	source, err := installedApplicationAgentSource(ctx)
	if err != nil || !d.installedAgentRelease(ctx, source).provenFor(source) {
		return fail(errors.Join(errors.New("applications: installed connector source is unproven"), err))
	}
	copy, err := updateImage(ctx, program)
	if err != nil || copy.Key != image.Key || copy.Key != applicationV3HeldKey(held) || copy.Hash != source.Hash || copy.Bytes != source.Bytes {
		return fail(errors.Join(errors.New("applications: connector differs from installed source"), err))
	}
	data, err := layout.AccountData()
	if err != nil {
		return fail(err)
	}
	hostImage, sourceImage, err := holdCatalogueHost(ctx, fileid.Self, catalogueHostParty, data, openRegistryProgramImage)
	if err != nil {
		return fail(err)
	}
	ref, err := applicationToken()
	if err != nil {
		hostImage.Close()
		return fail(err)
	}
	host := catalogueHostRecord{Reference: ref, Source: catalogueHostSource, Image: sourceImage}
	return host, func() error { return errors.Join(held.Recheck(ctx), hostImage.Recheck(ctx)) },
		func() { _ = held.Close(); _ = hostImage.Close() }, nil
}

// Capture the pending reference under the registration lock, then open the
// official image outside it. The answer commit checks the exact row again.
func (d *applicationDirectory) holdAgentAnswerCatalogueHost(ctx context.Context, name, revision, question string) (catalogueHostRecord, *heldApplicationManifest, error) {
	if name != catalogueAgentApplication {
		return catalogueHostRecord{}, nil, nil
	}
	if !d.lock(ctx) {
		return catalogueHostRecord{}, nil, ctx.Err()
	}
	var host catalogueHostRecord
	var reference string
	err := d.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		m := marks[name]
		if m.Agent == nil || m.Agent.State != "pending" || m.RegistrationRevision != revision || m.Question != question {
			return errors.New("agent enrollment: stale answer")
		}
		value, err := loadCatalogueCompanion(ctx, d.dir, name, m)
		if err != nil {
			return err
		}
		if value.samePending(name, m) {
			host = value.Host
			reference = host.Reference
		}
		return nil
	})
	d.unlock()
	if err != nil || reference == "" {
		return catalogueHostRecord{}, nil, err
	}
	local, err := layout.AccountData()
	if err != nil {
		return catalogueHostRecord{}, nil, err
	}
	held, err := holdKeptCatalogueHost(ctx, fileid.Self, catalogueHostParty, local, reference, host, openRegistryProgramImage)
	return host, held, err
}
