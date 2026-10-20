//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type installedConnectionHost struct {
	claudePanelImage
	manifest   *os.File
	path       string
	descriptor fwire.ApplicationDescriptor
	title      string
}

func (h *installedConnectionHost) Check(ctx context.Context) error {
	if err := h.claudePanelImage.Check(ctx); err != nil {
		return err
	}
	descriptor, err := readApplicationManifest(h.path)
	if err != nil {
		return err
	}
	if applicationDigest(&descriptor) != applicationDigest(&h.descriptor) {
		return fmt.Errorf("installed %s manifest changed", h.title)
	}
	return ctx.Err()
}
func (h *installedConnectionHost) Close() error {
	return errors.Join(h.manifest.Close(), h.claudePanelImage.Close())
}

func holdInstalledConnectionHost(ctx context.Context, connection installedAgentConnection) (fwire.ApplicationDescriptor, claudePanelImage, error) {
	var zero fwire.ApplicationDescriptor
	home, err := setup.CurrentHome()
	if err != nil {
		return zero, nil, err
	}
	if home.Isolated {
		return zero, nil, fmt.Errorf("%s connection requires the installed runtime", connection.title)
	}
	local, _, err := manifestAccountFolders()
	if err != nil {
		return zero, nil, err
	}
	// This is the existing MSI payload location, never a plugin-supplied path.
	path := filepath.Join(local, "Programs", "OpenAbstractions", "apps", string(connection.party), "openabstractions-application.json")
	manifest, err := nativecli.OpenSource(ctx, path)
	if err != nil {
		return zero, nil, err
	}
	descriptor, err := readApplicationManifest(path)
	official, found := catalogOfficialProgram(setup.AppID(connection.party))
	if err != nil || !found || descriptor.Name != string(connection.party) || descriptor.Activation != nil ||
		descriptor.Program != filepath.Join(local, filepath.FromSlash(official.LocalAppData)) {
		return zero, nil, errors.Join(err, manifest.Close(), fmt.Errorf("installed %s manifest differs from the canonical host", connection.title))
	}
	directory, err := programfiles.HoldDirectory(filepath.Dir(descriptor.Program))
	if err != nil {
		return zero, nil, errors.Join(err, manifest.Close())
	}
	input, err := nativecli.OpenSource(ctx, descriptor.Program)
	if err != nil {
		return zero, nil, errors.Join(err, manifest.Close(), directory.Release())
	}
	key, err := fileid.FileKeyOf(input)
	if err != nil {
		return zero, nil, errors.Join(err, input.Close(), manifest.Close(), directory.Release())
	}
	image := &installedConnectionHost{claudePanelImage: nativecli.AdoptImage(input, directory, descriptor.Program, key),
		manifest: manifest, path: path, descriptor: descriptor, title: connection.title}
	if err := image.Check(ctx); err != nil {
		return zero, nil, errors.Join(err, image.Close())
	}
	return descriptor, image, nil
}
