package nativecli

import (
	"context"
	"debug/pe"
	"errors"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
)

// FollowCodexCLI maps a saved CLI path in Codex Desktop's package layout to
// the CLI of the package version the OS registers now for Codex's publisher.
// Codex Desktop removes the earlier package folder on update. Any other path,
// or a Codex Desktop that is no longer registered, is returned unchanged; the
// caller authenticates the result as before.
func FollowCodexCLI(ctx context.Context, program string) (string, error) {
	return followCodexCLI(ctx, program, registeredCodexCLI)
}

func registeredCodexCLI(ctx context.Context) (string, bool, error) {
	image, err := packaged.OpenImage(ctx, codexProductFamily, "app/resources/codex.exe")
	if errors.Is(err, packaged.ErrNotRegistered) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	program := image.Program()
	return program, true, image.Close()
}

func currentCodexDesktop(ctx context.Context) (*CodexCLI, error) {
	rows, err := packaged.Registrations(ctx, codexProductFamily)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, errors.New("Codex Desktop registration is ambiguous; update it and retry")
	}
	processes, err := currentProductImages(ctx, codexProductFamily, "ChatGPT.exe", pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
	if err != nil {
		return nil, err
	}
	return selectCodexDesktop(ctx, processes, func(ctx context.Context, relative string) (nativeProductImage, error) {
		image, err := packaged.OpenImage(ctx, codexProductFamily, relative)
		if err != nil {
			return nil, err
		}
		kind := uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI)
		if relative == "app/ChatGPT.exe" {
			kind = pe.IMAGE_SUBSYSTEM_WINDOWS_GUI
		}
		if err := productSubsystem(image.File(), kind); err != nil {
			return nil, errors.Join(err, image.Close())
		}
		return image, nil
	})
}

// CodexDesktopRunning reports a Codex Desktop window process of this account
// in this logon session. It reads the process inventory only. While it runs,
// the Desktop owns its conversations and OA queues into them.
func CodexDesktopRunning(ctx context.Context) (bool, error) {
	rows, err := currentProductImages(ctx, codexProductFamily, "ChatGPT.exe", pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
	if err != nil {
		return false, err
	}
	var errs []error
	for _, row := range rows {
		if row.close != nil {
			errs = append(errs, row.close())
		}
	}
	return len(rows) > 0, errors.Join(errs...)
}

func selectCodexDesktop(ctx context.Context, rows []claudeProcessImage, open func(context.Context, string) (nativeProductImage, error)) (*CodexCLI, error) {
	var gui, cli nativeProductImage
	closeAll := func() error {
		var errs []error
		if gui != nil {
			errs = append(errs, gui.Close())
		}
		for _, row := range rows {
			if row.close != nil {
				errs = append(errs, row.close())
			}
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (*CodexCLI, error) {
		if cli != nil {
			err = errors.Join(err, cli.Close())
		}
		return nil, errors.Join(err, closeAll())
	}
	if len(rows) > 64 || open == nil {
		return fail(errors.New("Codex Desktop selection evidence is unavailable"))
	}
	// The current package registration owns the command installation. Keeping
	// that image held permits connection maintenance after the desktop quits.
	// Any live desktop evidence must still match the registered image.
	var err error
	gui, err = open(ctx, "app/ChatGPT.exe")
	if err != nil {
		return fail(err)
	}
	check := func(ctx context.Context) error {
		if err := gui.Check(ctx); err != nil {
			return err
		}
		for _, row := range rows {
			if row.image == nil || row.current == nil || row.close == nil {
				return errors.New("Codex Desktop native evidence is incomplete")
			}
			if err := row.current(); err != nil {
				if errors.Is(err, errProductProcessEnded) {
					continue
				}
				return err
			}
			if err := row.image.Check(ctx); err != nil {
				return err
			}
			if row.image.FileKey() != gui.FileKey() {
				return errors.New("Codex Desktop registered image changed; restart it and retry")
			}
		}
		return ctx.Err()
	}
	if err := check(ctx); err != nil {
		return fail(err)
	}
	cli, err = open(ctx, "app/resources/codex.exe")
	if err != nil {
		return fail(err)
	}
	result := &CodexCLI{image: cli, desktopCheck: check, desktopClose: closeAll}
	if err := result.Check(ctx); err != nil {
		return fail(err)
	}
	return result, nil
}
