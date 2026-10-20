package main

import (
	"context"
	"errors"
	"path/filepath"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

type messageStartCLIIdentity interface {
	FileKey() string
	Check(context.Context) error
	Close() error
}

// approveClaudeMessageStartProfile validates a current explicitly configured
// recipe without executing the CLI. The caller already owns kept enrollment
// and current descriptor checks. Person choices/C59 still gate composition.
func approveClaudeMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile) error {
	return validateClaudeMessageStartProfile(ctx, descriptor, profile, func(ctx context.Context, stored []string) (messageStartCLIIdentity, error) {
		return nativecli.HoldClaudeSavedCLI(ctx, stored)
	})
}

func validateClaudeMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile, hold func(context.Context, []string) (messageStartCLIIdentity, error)) error {
	return validateMessageStartProfile(ctx, descriptor, profile, "oa-claude-integration", "claude-v1", nativecli.SameClaudeCLIInstall, hold)
}

// approveCodexMessageStartProfile validates a current codex-v1 recipe the same
// way, without executing the CLI. The Codex recipe only queues a turn into an
// existing thread of the running Codex app.
func approveCodexMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile) error {
	return validateCodexMessageStartProfile(ctx, descriptor, profile, func(ctx context.Context, stored []string) (messageStartCLIIdentity, error) {
		return nativecli.HoldCodexSavedCLI(ctx, stored)
	})
}

func validateCodexMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile, hold func(context.Context, []string) (messageStartCLIIdentity, error)) error {
	return validateMessageStartProfile(ctx, descriptor, profile, "oa-codex-integration", "codex-v1", nativecli.SameCodexCLIInstall, hold)
}

// approveMessageStartProfile approves each agent's own recipe kind only.
func approveMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile) error {
	switch profile.Name {
	case "claude-v1":
		return approveClaudeMessageStartProfile(ctx, descriptor, profile)
	case "codex-v1":
		return approveCodexMessageStartProfile(ctx, descriptor, profile)
	}
	return errors.New("message start: unsupported profile")
}

func validateMessageStartProfile(ctx context.Context, descriptor wire.ApplicationDescriptor, profile startprofile.Profile, base, name string, sameInstall func(a, b string) bool, hold func(context.Context, []string) (messageStartCLIIdentity, error)) error {
	if nativecli.AgentApplicationBase(descriptor.Name) != base || (descriptor.Name != base && nativecli.AgentApplicationName(filepath.Base(descriptor.Program)) != descriptor.Name) || descriptor.Activation == nil || hold == nil || sameInstall == nil {
		return errors.New("message start: unsupported recipe")
	}
	if err := client.ValidApplicationDescriptor(descriptor); err != nil {
		return err
	}
	parsed, err := startprofile.Parse(descriptor.Activation.Arguments)
	if err != nil || parsed != profile || profile.Name != name {
		return errors.New("message start: unsupported profile")
	}
	stored, err := fileid.ParseStoredFileID(profile.CLIFileID)
	if err != nil {
		return errors.New("message start: saved image unavailable")
	}
	current, state, err := fileid.Revalidate(ctx, stored)
	if err != nil {
		return errors.New("message start: saved image changed")
	}
	cli, err := hold(ctx, []string{profile.CLIFileID})
	if err != nil || cli == nil {
		if cli != nil {
			err = errors.Join(err, cli.Close())
		}
		return errors.Join(errors.New("message start: native image unavailable"), err)
	}
	checkErr := cli.Check(ctx)
	// A vendor update moves the CLI to a new version folder of the same
	// installation. The hold follows the saved locator there and
	// authenticates that image; otherwise the saved image itself must hold.
	located, ok := cli.(interface{ Program() string })
	followed := ok && !fileid.SamePathName(located.Program(), stored.Path()) && sameInstall(stored.Path(), located.Program())
	if !followed && (state != fileid.StateSame || current.IsDir() || current.Links() != 1 || cli.FileKey() != current.Key()) {
		checkErr = errors.Join(checkErr, errors.New("message start: saved image changed"))
	}
	return errors.Join(checkErr, cli.Close())
}

// Installation supplies the actual person-approved recipe and C59 wording
// authority. Until that approval exists, composition retains a nil resolver.
// The callback must validate the native helper and saved CLI/profile approval.
func messageStartProfileResolver(approve func(context.Context, wire.ApplicationDescriptor, startprofile.Profile) error) func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
	if approve == nil {
		return nil
	}
	return func(ctx context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Activation == nil {
			return applicationMessageStartRequest{}, errors.New("message start: recipe unavailable")
		}
		profile, err := startprofile.Parse(descriptor.Activation.Arguments)
		if err != nil {
			return applicationMessageStartRequest{}, err
		}
		if err = approve(ctx, descriptor, profile); err != nil {
			return applicationMessageStartRequest{}, err
		}
		session, err := messageStartNativeSession()
		if err != nil {
			return applicationMessageStartRequest{}, err
		}
		folders := make([]fileid.FileID, 0, 2)
		for _, path := range []string{profile.Folder, profile.StateDir} {
			folder, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil || !folder.IsDir() {
				return applicationMessageStartRequest{}, errors.New("message start: approved folder unavailable")
			}
			folders = append(folders, folder)
		}
		return applicationMessageStartRequest{Session: session, Context: profile.Context, ProfileFingerprint: profile.Fingerprint(descriptor.Program), PermitRecipe: profile.PermitFingerprint(descriptor.Program), Validate: func(ctx context.Context) error {
			current, err := messageStartNativeSession()
			if err != nil || current != session {
				return errors.New("message start: native session changed")
			}
			if err = approve(ctx, descriptor, profile); err != nil {
				return err
			}
			for i, path := range []string{profile.Folder, profile.StateDir} {
				current, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
				if err != nil || !current.IsDir() || !current.Equal(folders[i]) {
					return errors.New("message start: approved folder changed")
				}
			}
			return ctx.Err()
		}}, nil
	}
}
