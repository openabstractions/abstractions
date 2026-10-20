package nativecli

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

const appModelNoPackage syscall.Errno = 15700

type anchoredClaudeImage struct {
	nativeProductImage
	anchor nativeProductImage
}

func (i *anchoredClaudeImage) Check(ctx context.Context) error {
	return errors.Join(i.nativeProductImage.Check(ctx), i.anchor.Check(ctx))
}
func (i *anchoredClaudeImage) Close() error {
	return errors.Join(i.nativeProductImage.Close(), i.anchor.Close())
}

func trustedClaudePublisher(code identity.Code, proof identity.Proof) bool {
	return proof == identity.ProofBound && code.Trusted && !code.Catalog && code.Subject != "" && code.Issuer != ""
}
func matchesClaudePublisher(code identity.Code, proof identity.Proof, anchor identity.Code, anchorProof identity.Proof) bool {
	return trustedClaudePublisher(anchor, anchorProof) && trustedClaudePublisher(code, proof) && code.Subject == anchor.Subject && code.Issuer == anchor.Issuer && code.Description == "Claude Code"
}
func claudeCandidatePackage(family string, err error) error {
	if errors.Is(err, appModelNoPackage) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(family, claudeProductFamily) {
		return errOtherClaudeImage
	}
	return nil
}

func queriedClaudeFileKey(program string) (key string, err error) {
	file, err := os.Open(program)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return fileid.FileKeyOf(file)
}

func classifyClaudeProcessImage(ctx context.Context, query windows.Handle, program string, classifiedKey *string) (subsystem uint16, err error) {
	// The packaged owner opens the OS-registered root directly. Its held
	// image/PE evidence remains usable when WindowsApps ancestors are opaque.
	registered, match, err := packaged.MatchImage(ctx, claudeProductFamily, "app/claude.exe", program)
	if err != nil {
		return 0, err
	}
	if match {
		defer func() { err = errors.Join(err, registered.Close()) }()
		observedKey, err := queriedClaudeFileKey(program)
		if err != nil || observedKey != registered.FileKey() {
			return 0, errors.Join(err, errors.New("Code queried image differs from registered image"))
		}
		subsystem, err := productImageSubsystem(registered.File())
		if err != nil {
			return 0, err
		}
		if err := registered.Check(ctx); err != nil {
			return 0, err
		}
		var exit uint32
		if err := windows.GetExitCodeProcess(query, &exit); err != nil {
			return 0, err
		}
		if exit != 259 {
			return 0, errProductProcessEnded
		}
		*classifiedKey = observedKey
		return subsystem, ctx.Err()
	}
	image, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	id := image.FileID()
	if id.IsDir() || id.Links() != 1 {
		return 0, errors.New("Code native image kind or links unavailable")
	}
	subsystem, err = productImageSubsystem(image.File())
	if err != nil {
		return 0, err
	}
	current, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !current.Equal(id) {
		return 0, errors.Join(err, errors.New("Code native image changed during classification"))
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(query, &exit); err != nil {
		return 0, err
	}
	if exit != 259 {
		return 0, errProductProcessEnded
	}
	*classifiedKey = id.Key()
	return subsystem, ctx.Err()
}

func currentSignedClaudeImages(ctx context.Context) ([]claudeProcessImage, error) {
	return currentProductImages(ctx, claudeProductFamily, "claude.exe", pe.IMAGE_SUBSYSTEM_WINDOWS_CUI)
}

// Query-only classification admits no command. A proven GUI is excluded before
// requesting SYNCHRONIZE; unknown account/image/PE evidence keeps the inventory
// unavailable. Only an authenticated CUI candidate receives the lifetime hold.
func holdSignedClaudeProcess(ctx context.Context, pid uint32, query windows.Handle, program string) (claudeProcessImage, error) {
	var classifiedKey string
	subsystem, err := classifyClaudeProcessImage(ctx, query, program, &classifiedKey)
	if err != nil {
		return claudeProcessImage{}, fmt.Errorf("Code native PE classification: %w", err)
	}
	if subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		return claudeProcessImage{}, errOtherClaudeImage
	}
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		return claudeProcessImage{}, errors.New("Code native PE subsystem unavailable")
	}
	selected, err := holdSelectedProductImage(ctx, claudeProductFamily, program, pe.IMAGE_SUBSYSTEM_WINDOWS_CUI)
	if err != nil {
		return claudeProcessImage{}, err
	}
	fail := func(err error) (claudeProcessImage, error) {
		closeErr := selected.Close()
		if closeErr != nil && errors.Is(err, errOtherClaudeImage) {
			return claudeProcessImage{}, fmt.Errorf("Code candidate exclusion cleanup failed (%v): %w", err, closeErr)
		}
		return claudeProcessImage{}, errors.Join(err, closeErr)
	}
	if selected.FileKey() != classifiedKey {
		return fail(errors.New("Code image changed after native classification"))
	}
	family, err := claudeProcessPackage(query)
	if err := claudeCandidatePackage(family, err); err != nil {
		return fail(err)
	}
	anchored, err := authenticateClaudeImage(ctx, selected)
	if err != nil {
		return claudeProcessImage{}, err
	}
	return retainProductProcess(ctx, pid, query, anchored)
}

func holdSignedClaudeImage(ctx context.Context, program string) (nativeProductImage, error) {
	selected, err := holdSelectedProductImage(ctx, claudeProductFamily, program, pe.IMAGE_SUBSYSTEM_WINDOWS_CUI)
	if err != nil {
		return nil, err
	}
	return authenticateClaudeImage(ctx, selected)
}

func authenticateClaudeImage(ctx context.Context, selected nativeProductImage) (nativeProductImage, error) {
	fail := func(err error) (nativeProductImage, error) { return nil, errors.Join(err, selected.Close()) }
	anchor, err := packaged.OpenImage(ctx, claudeProductFamily, "app/claude.exe")
	if err != nil {
		return fail(err)
	}
	failWithAnchor := func(err error) (nativeProductImage, error) { return fail(errors.Join(err, anchor.Close())) }
	if err := productSubsystem(anchor.File(), pe.IMAGE_SUBSYSTEM_WINDOWS_GUI); err != nil {
		return failWithAnchor(err)
	}
	anchorCode, anchorProof, err := identity.VerifyFile(anchor.Program(), nil)
	if err != nil || !trustedClaudePublisher(anchorCode, anchorProof) {
		return failWithAnchor(errors.Join(err, errors.New("Code registered publisher anchor is unavailable")))
	}
	code, proof, err := identity.VerifyFile(selected.Program(), nil)
	if err != nil || !matchesClaudePublisher(code, proof, anchorCode, anchorProof) {
		return failWithAnchor(errors.Join(err, errors.New("Code publisher or description differs from its registered product")))
	}
	if err := anchor.Check(ctx); err != nil {
		return failWithAnchor(err)
	}
	if err := selected.Check(ctx); err != nil {
		return failWithAnchor(err)
	}
	return &anchoredClaudeImage{nativeProductImage: selected, anchor: anchor}, nil
}
