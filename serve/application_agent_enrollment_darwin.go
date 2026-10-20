package main

import (
	"context"
	"debug/macho"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	corebootstrap "github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func readApplicationAgentImage(ctx context.Context, program, binding string) (applicationAgentImage, error) {
	held, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return applicationAgentImage{}, err
	}
	defer held.Close()
	id := held.FileID()
	info, err := held.File().Stat()
	if err != nil {
		return applicationAgentImage{}, err
	}
	if id.IsDir() || id.Links() != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return applicationAgentImage{}, errors.New("agent enrollment: ordinary native executable required")
	}
	program = id.Path()
	check := func() error {
		named, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return err
		}
		defer named.Close()
		if !named.FileID().Equal(id) || named.FileID().Links() != 1 {
			return errors.New("agent enrollment: image replaced")
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return applicationAgentImage{}, err
	}
	if err := applicationAgentMachO(held.File(), info.Size()); err != nil {
		return applicationAgentImage{}, err
	}
	// Static file signatures are unsupported by identity.VerifyFile on Darwin.
	// No publisher or OriginalName is manufactured from a signing identifier.
	// Known names, virtual environments and image markers still refuse shared
	// interpreters; unsigned renamed interpreters retain ID-I1's documented limit.
	if identity.SharedInterpreterProgram(program, identity.Code{}) {
		return applicationAgentImage{}, errors.New("agent enrollment: shared interpreter is not an agent subject")
	}
	if err := check(); err != nil {
		return applicationAgentImage{}, err
	}
	return applicationAgentImage{Program: identity.NormalizeSubjectProgram(program), Key: id.Key(), Binding: binding}, ctx.Err()
}

func applicationAgentMachO(reader io.ReaderAt, size int64) error {
	var cpu macho.Cpu
	switch runtime.GOARCH {
	case "arm64":
		cpu = macho.CpuArm64
	case "amd64":
		cpu = macho.CpuAmd64
	default:
		return errors.New("agent enrollment: unsupported native architecture")
	}
	if image, err := macho.NewFile(io.NewSectionReader(reader, 0, size)); err == nil {
		defer image.Close()
		if image.Type == macho.TypeExec && image.Cpu == cpu {
			return nil
		}
	} else if image, err := macho.NewFatFile(io.NewSectionReader(reader, 0, size)); err == nil {
		defer image.Close()
		for _, arch := range image.Arches {
			if arch.Cpu == cpu && arch.File.Type == macho.TypeExec {
				return nil
			}
		}
	}
	return errors.New("agent enrollment: executable Mach-O native slice required")
}

// readApplicationAgentStamp reads program's own metadata without following a
// final link and without reading data.
func readApplicationAgentStamp(program string) (applicationAgentStamp, error) {
	info, err := os.Lstat(program)
	if err != nil {
		return applicationAgentStamp{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return applicationAgentStamp{}, errors.New("agent enrollment: ordinary native image required")
	}
	key, err := fileid.FileKey(program)
	if err != nil {
		return applicationAgentStamp{}, err
	}
	return applicationAgentStamp{Key: key, Links: uint64(st.Nlink), Size: st.Size, Written: st.Mtimespec.Nano(), Changed: st.Ctimespec.Nano()}, nil
}

// The Darwin installer places the shared gateway beside its selected runtime.
// Selection and real FileIDs determine exclusion; a basename conveys no class.
func applicationAgentGatewayExclusion(ctx context.Context, program, key string) (bool, error) {
	selected, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return false, err
	}
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	held, err := fileid.Open(ctx, fileid.Self, self, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return false, err
	}
	defer held.Close()
	expected, err := fileid.Open(ctx, fileid.Self, selected.Server.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return false, err
	}
	defer expected.Close()
	if !held.FileID().Equal(expected.FileID()) {
		return false, errors.New("agent enrollment: installed runtime does not name this runtime")
	}
	gateway, err := fileid.Open(ctx, fileid.Self, filepath.Join(filepath.Dir(expected.FileID().Path()), "openabstractions-mcp"), fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer gateway.Close()
	if gateway.FileID().IsDir() || gateway.FileID().Links() != 1 {
		return false, errors.New("agent enrollment: shared gateway component is ambiguous")
	}
	return key == gateway.FileID().Key(), ctx.Err()
}

// installedSharedGatewayKey reads the installed shared gateway image's file
// key beside the selected installed runtime, whichever runtime asks. found is
// false only when no runtime is installed or the gateway is absent.
func installedSharedGatewayKey(ctx context.Context) (string, bool, error) {
	selected, err := bootstrap.SelectInstalled(ctx)
	if err == corebootstrap.ErrNoTrustedInstallation {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	gateway, err := fileid.Open(ctx, fileid.Self, filepath.Join(filepath.Dir(selected.Server.Program), "openabstractions-mcp"), fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer gateway.Close()
	if gateway.FileID().IsDir() || gateway.FileID().Links() != 1 {
		return "", false, errors.New("agent enrollment: shared gateway component is ambiguous")
	}
	return gateway.FileID().Key(), true, ctx.Err()
}
