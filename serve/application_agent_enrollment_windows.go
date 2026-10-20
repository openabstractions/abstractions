//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	asks "github.com/openabstractions/abstraction-asks/go"
	corebootstrap "github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// This is the installer-owned McpGatewayFiles component location, relative to
// InstallLocation. The layout consistency test binds it to the packaging owner.
const applicationSharedGatewayComponent = "tools/openabstractions-mcp.exe"

func readApplicationAgentImage(ctx context.Context, program, binding string) (applicationAgentImage, error) {
	admitted, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return applicationAgentImage{}, err
	}
	defer admitted.Close()
	if admitted.FileID().IsDir() || admitted.FileID().Links() != 1 {
		return applicationAgentImage{}, errors.New("agent enrollment: ordinary native image required")
	}
	program = admitted.FileID().Path()
	name, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return applicationAgentImage{}, err
	}
	fd, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return applicationAgentImage{}, err
	}
	f := os.NewFile(uintptr(fd), program)
	defer f.Close()
	key, err := fileid.FileKeyOf(f)
	if err != nil || key != admitted.FileID().Key() {
		return applicationAgentImage{}, errors.Join(errors.New("agent enrollment: image changed"), err)
	}
	// Agent subjects follow RIGHTS-A3/A5 program/file identity. Signing is
	// publisher provenance and existing shared-interpreter evidence here;
	// it creates no content-version subject or new signature requirement.
	stat, err := f.Stat()
	if err != nil {
		return applicationAgentImage{}, err
	}
	before, beforeErr := readApplicationNamedResource(f, stat.Size(), agentReleaseResourceName)
	var verification *identity.Options
	if beforeErr == nil {
		// Standing maintenance needs a current accepted revocation verdict.
		// Legacy images retain the ordinary enrollment verification policy.
		verification = &identity.Options{CheckRevocation: true}
	}
	code, proof, publisherKey, verifyErr := identity.VerifyFilePublisher(program, verification)
	if verifyErr != nil {
		return applicationAgentImage{}, verifyErr
	}
	if identity.SharedInterpreterProgram(program, code) {
		return applicationAgentImage{}, errors.New("agent enrollment: shared interpreter is not an agent subject")
	}
	named, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return applicationAgentImage{}, err
	}
	defer named.Close()
	if named.FileID().Key() != key {
		return applicationAgentImage{}, errors.New("agent enrollment: image replaced")
	}
	image := applicationAgentImage{Program: identity.NormalizeSubjectProgram(program), Key: key, Binding: binding, Proof: proof, Publisher: asks.Publisher{Subject: code.Subject, Trusted: code.Trusted, Description: code.Description, OriginalName: code.OriginalName}}
	if beforeErr == nil && code.Trusted && proof == identity.ProofBound && !code.Catalog && publisherKey != "" {
		after, readErr := readApplicationNamedResource(f, stat.Size(), agentReleaseResourceName)
		if readErr == nil && bytes.Equal(before, after) {
			document, snapshot, parseErr := agentReleaseSnapshot(after)
			pin, pinErr := updateImage(ctx, program)
			if parseErr == nil && pinErr == nil && pin.Key == key {
				image.Release = &agentNativeRelease{Component: document.Component, PublisherKey: publisherKey, Channel: document.Channel, Release: document.Release, Declaration: snapshot, ImageKey: key, ImageHash: pin.Hash, ImageBytes: pin.Bytes, Authenticated: true}
			}
		}
	}
	return image, ctx.Err()
}

// applicationAgentBasicInfo is FILE_BASIC_INFO, which x/sys/windows does not
// declare.
type applicationAgentBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32
}

// readApplicationAgentStamp opens program's final name itself, without
// following a link there and without reading data, and lets other openers
// read, write, rename and delete it meanwhile.
func readApplicationAgentStamp(program string) (applicationAgentStamp, error) {
	name, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return applicationAgentStamp{}, err
	}
	fd, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return applicationAgentStamp{}, err
	}
	f := os.NewFile(uintptr(fd), program)
	defer f.Close()
	key, err := fileid.FileKeyOf(f)
	if err != nil {
		return applicationAgentStamp{}, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(fd, &info); err != nil {
		return applicationAgentStamp{}, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return applicationAgentStamp{}, errors.New("agent enrollment: ordinary native image required")
	}
	var basic applicationAgentBasicInfo
	if err = windows.GetFileInformationByHandleEx(fd, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return applicationAgentStamp{}, err
	}
	return applicationAgentStamp{Key: key, Links: uint64(info.NumberOfLinks), Size: int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow), Written: info.LastWriteTime.Nanoseconds(), Changed: basic.ChangeTime}, nil
}

// No caller spelling chooses the shared gateway. Selection reads the actual MSI
// installation, proves it is this runtime, then resolves its declared component.
func applicationAgentGatewayExclusion(ctx context.Context, program, key string) (bool, error) {
	g, err := installedApplicationAgentGateway(ctx)
	if err != nil || g == nil {
		return false, err
	}
	defer g.Close()
	return key == g.FileID().Key(), ctx.Err()
}

func installedApplicationAgentSource(ctx context.Context) (agentUpdateImage, error) {
	g, err := installedApplicationAgentGateway(ctx)
	if err != nil {
		return agentUpdateImage{}, err
	}
	if g == nil {
		return agentUpdateImage{}, errors.New("installed agent component absent")
	}
	defer g.Close()
	pin, err := updateImage(ctx, g.FileID().Path())
	if err != nil || pin.Key != g.FileID().Key() {
		return agentUpdateImage{}, errors.Join(errors.New("installed agent source changed"), err)
	}
	return pin, nil
}

func installedApplicationAgentGateway(ctx context.Context) (*fileid.Handle, error) {
	selected, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	held, err := fileid.Open(ctx, fileid.Self, self, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer held.Close()
	expected, err := fileid.Open(ctx, fileid.Self, selected.Server.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer expected.Close()
	if !held.FileID().Equal(expected.FileID()) {
		return nil, errors.New("agent enrollment: installed runtime does not name this runtime")
	}
	return openSharedGatewayBeside(ctx, expected.FileID().Path())
}

// installedSharedGatewayKey reads the installed shared gateway image's file
// key through the MSI installation's registration, whichever runtime asks.
// found is false only when no runtime is installed or the optional gateway
// component is absent.
func installedSharedGatewayKey(ctx context.Context) (string, bool, error) {
	selected, err := bootstrap.SelectInstalled(ctx)
	if err == corebootstrap.ErrNoTrustedInstallation {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	expected, err := fileid.Open(ctx, fileid.Self, selected.Server.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return "", false, err
	}
	defer expected.Close()
	g, err := openSharedGatewayBeside(ctx, expected.FileID().Path())
	if err != nil || g == nil {
		return "", false, err
	}
	defer g.Close()
	return g.FileID().Key(), true, ctx.Err()
}

// openSharedGatewayBeside opens the shared gateway component of the
// installation whose runtime is runtimePath; nil when the component is absent.
func openSharedGatewayBeside(ctx context.Context, runtimePath string) (*fileid.Handle, error) {
	gateway := filepath.Join(filepath.Dir(filepath.Dir(runtimePath)), filepath.FromSlash(applicationSharedGatewayComponent))
	g, err := fileid.Open(ctx, fileid.Self, gateway, fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} // Known absent optional MSI feature.
	if err != nil {
		return nil, err
	}
	if g.FileID().IsDir() || g.FileID().Links() != 1 {
		return nil, errors.Join(errors.New("agent enrollment: shared gateway component is ambiguous"), g.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, g.Close())
	}
	return &g, nil
}
