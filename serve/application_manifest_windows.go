//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// heldApplicationManifest owns the file until the runtime has completed its
// later exact-process activation check. Parsing Bytes grants no authority.
// The caller must Close on every failure and after the activation owner has
// finished with this hold, and Recheck before that owner starts the program.
type heldApplicationManifest struct {
	Bytes []byte
	Code  identity.Code
	Proof identity.Proof
	// Signed is a trusted Authenticode signature bound to this held image. An
	// unsigned or untrusted image carries its pin instead: the SHA-256 and size
	// of these held bytes. Pinned is set only after a registration compared
	// that pin with the one the person kept.
	Signed       bool
	ImageSHA256  string
	ImageSize    int64
	Pinned       bool
	file         *os.File
	program, key string
	stamp        windows.ByHandleFileInformation
}

func (h *heldApplicationManifest) Close() error {
	if h == nil || h.file == nil {
		return nil
	}
	f := h.file
	h.file = nil
	return f.Close()
}

func (h *heldApplicationManifest) Recheck(ctx context.Context) error {
	if h == nil || h.file == nil {
		return errors.New("application manifest: file hold closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var now windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(h.file.Fd()), &now); err != nil {
		return err
	}
	if now != h.stamp { // Access time is excluded below: reading may advance it.
		now.LastAccessTime = h.stamp.LastAccessTime
		if now != h.stamp {
			return errors.New("application manifest: held file metadata changed")
		}
	}
	key, err := fileid.FileKeyOf(h.file)
	if err != nil || key != h.key {
		return errors.Join(errors.New("application manifest: held file identity changed"), err)
	}
	named, err := fileid.Open(ctx, fileid.Self, h.program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer named.Close()
	if named.FileID().Key() != h.key {
		return errors.New("application manifest: program no longer names held file")
	}
	return nil
}

// openSignedApplicationManifest composes the existing fileid locality/link
// admission, serve's FILE_SHARE_READ-only hold pattern and identity.VerifyFile.
// A trusted signature sets Signed. An unsigned image, or one whose signature
// Windows refuses, is returned with Signed false and its held SHA-256 and size;
// the registration owner decides what such an image may do.
// The ordinary local namespace is revalidated after the final hold. On Windows
// this non-delete-shared file also blocks renaming its ordinary ancestors; the
// native counterexample tests retain that property. Reparse paths are refused.
// Consequently VerifyFile's path and this reader's handle name the same object
// throughout verification. The verifier uses its ordinary cached/offline policy;
// this call requests no revocation network access and never executes the PE.
func openSignedApplicationManifest(ctx context.Context, peer fileid.Peer, program string) (_ *heldApplicationManifest, err error) {
	h, err := openHeldApplicationImage(ctx, peer, program)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, h.Close())
		}
	}()
	program = h.program

	size := int64(uint64(h.stamp.FileSizeHigh)<<32 | uint64(h.stamp.FileSizeLow))
	if size <= 0 {
		return nil, errApplicationManifestResource
	}
	// Read twice across actual signature verification. Byte equality supplements
	// native write exclusion and the stable file/namespace evidence.
	before, readErr := readApplicationManifestResource(h.file, size)
	h.Code, h.Proof, err = identity.VerifyFile(program, nil)
	if err != nil {
		return nil, err
	}
	h.Signed = heldSigned(h)
	if !h.Signed {
		if !applicationImageUnsigned(h.Code, h.Proof) {
			return nil, errors.New("application manifest: program signature unavailable")
		}
		if h.ImageSHA256, err = registryHeldImageSHA256(ctx, h); err != nil {
			return nil, err
		}
		h.ImageSize = size
	}
	if err = h.Recheck(ctx); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	after, err := readApplicationManifestResource(h.file, size)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(before, after) {
		return nil, errors.New("application manifest: resource changed during verification")
	}
	if err = h.Recheck(ctx); err != nil {
		return nil, err
	}
	h.Bytes = after
	return h, nil
}

func openHeldApplicationImage(ctx context.Context, peer fileid.Peer, program string) (_ *heldApplicationManifest, err error) {
	admitted, err := fileid.Open(ctx, peer, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer admitted.Close()
	if admitted.FileID().IsDir() {
		return nil, errors.New("application manifest: program is a directory")
	}
	program = admitted.FileID().Path()
	name, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return nil, err
	}
	fd, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, err
	}
	h := &heldApplicationManifest{file: os.NewFile(uintptr(fd), program), program: program}
	defer func() {
		if err != nil {
			err = errors.Join(err, h.Close())
		}
	}()
	if err = windows.GetFileInformationByHandle(fd, &h.stamp); err != nil {
		return nil, err
	}
	if h.stamp.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return nil, errors.New("application manifest: program is not an ordinary regular file")
	}
	h.key, err = fileid.FileKeyOf(h.file)
	if err != nil {
		return nil, err
	}
	if h.key != admitted.FileID().Key() {
		return nil, errors.New("application manifest: program replaced during open")
	}
	if err = h.Recheck(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// openTrustedApplicationImage reuses the exact native read-only image hold for
// a service-owned child without requiring an application manifest declaration.
func openTrustedApplicationImage(ctx context.Context, peer fileid.Peer, program string) (_ *heldApplicationManifest, err error) {
	h, err := openHeldApplicationImage(ctx, peer, program)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, h.Close())
		}
	}()
	h.Code, h.Proof, err = identity.VerifyFile(h.program, nil)
	if err != nil {
		return nil, err
	}
	if !h.Code.Trusted || h.Proof != identity.ProofBound {
		return nil, errors.New("application image: program signature not trusted")
	}
	if err = h.Recheck(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// applicationHeldPin is the held image's SHA-256 and size, read from this
// same write-excluding hold when the reader did not already record them.
func applicationHeldPin(ctx context.Context, h *heldApplicationManifest) (string, int64, error) {
	if h == nil || h.file == nil {
		return "", 0, errors.New("application manifest: file hold closed")
	}
	if h.ImageSHA256 != "" {
		return h.ImageSHA256, h.ImageSize, nil
	}
	digest, err := registryHeldImageSHA256(ctx, h)
	if err != nil {
		return "", 0, err
	}
	return digest, int64(uint64(h.stamp.FileSizeHigh)<<32 | uint64(h.stamp.FileSizeLow)), nil
}
