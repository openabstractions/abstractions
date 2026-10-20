//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// The returned hold spans the registration CAS. Every authority fact, including
// the digest and FileKey, belongs to the same write/delete-excluding image.
func observeShippedRegistrationImage(ctx context.Context, program string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
	v, s, r, c, _ := observeShippedRegistrationImageCause(ctx, program, prior)
	return v, s, r, c
}

func observeShippedRegistrationImageCause(ctx context.Context, program string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error, string) {
	executable, err := os.Executable()
	if err != nil {
		return 0, nil, nil, nil, "identity:unavailable"
	}
	cause := "identity:unavailable"
	v, s, r, c := observeShippedRegistrationImageAtCause(ctx, program, prior, executable, shippedProviderImages, &cause)
	return v, s, r, c, cause
}

func observeShippedRegistrationImageAt(ctx context.Context, program string, prior *applicationV3Source, executable, encoded string) (registration.Verification, *applicationV3Source, func() error, func() error) {
	return observeShippedRegistrationImageAtCause(ctx, program, prior, executable, encoded, nil)
}

func observeShippedRegistrationImageAtCause(ctx context.Context, program string, prior *applicationV3Source, executable, encoded string, cause *string) (registration.Verification, *applicationV3Source, func() error, func() error) {
	setCause := func(value string) {
		if cause != nil {
			*cause = value
		}
	}
	refused := func() (registration.Verification, *applicationV3Source, func() error, func() error) {
		return 0, nil, nil, nil
	}
	setCause("identity:unavailable")
	runtimeImage, err := openHeldApplicationImage(ctx, fileid.Self, executable)
	if err != nil {
		return refused()
	}
	accepted := false
	defer func() {
		if !accepted {
			runtimeImage.Close()
		}
	}()
	folder, err := fileid.Open(ctx, fileid.Self, filepath.Dir(runtimeImage.program), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return refused()
	}
	defer func() {
		if !accepted {
			folder.Close()
		}
	}()
	held, err := openHeldApplicationImage(ctx, fileid.Self, program)
	if err != nil {
		return refused()
	}
	defer func() {
		if !accepted {
			held.Close()
		}
	}()
	setCause("shipped:outside")
	name, err := registryHeldImageToolsRelativeTo(ctx, held, folder)
	if err != nil {
		return refused()
	}
	setCause("identity:unavailable")
	record, err := parseShippedProviderImages(encoded)
	if err != nil || len(record) == 0 || prior == nil {
		if err == nil {
			setCause("shipped:unrecorded")
		}
		return refused()
	}
	expected, ok := record[name]
	if !ok {
		setCause("shipped:unrecorded")
		return refused()
	}
	digest, err := registryHeldImageSHA256(ctx, held)
	if err != nil {
		return refused()
	}
	if digest != expected {
		if held.Recheck(ctx) == nil {
			setCause("shipped:hash-mismatch")
		}
		return refused()
	}
	code, proof, err := identity.VerifyFile(held.program, nil)
	if err != nil || !registrationProgramCodeAccepted(code, proof) {
		return refused()
	}
	runtimeCode, runtimeProof, err := identity.VerifyFile(runtimeImage.program, nil)
	if err != nil || !registrationProgramCodeAccepted(runtimeCode, runtimeProof) || !shippedPublisherContinuous(prior, code, proof, runtimeCode, runtimeProof) {
		return refused()
	}
	if held.Recheck(ctx) != nil || runtimeImage.Recheck(ctx) != nil {
		return refused()
	}
	verification := registration.VerificationUnverified
	if code.Trusted && proof == identity.ProofBound {
		verification = registration.VerificationVerified
	}
	accepted = true
	return verification, &applicationV3Source{Program: program, FileKey: held.key, Code: code, Proof: proof}, func() error {
		if err := runtimeImage.Recheck(ctx); err != nil {
			return err
		}
		return held.Recheck(ctx)
	}, func() error { return errors.Join(held.Close(), folder.Close(), runtimeImage.Close()) }
}

// The review owner uses this same native containment proof on its already-held
// image, both at capture and immediately before consuming a person's answer.
func registryHeldImageToolsRelative(ctx context.Context, held *heldApplicationManifest) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return registryHeldImageToolsRelativeAt(ctx, held, executable)
}

func registryHeldImageToolsRelativeAt(ctx context.Context, held *heldApplicationManifest, executable string) (string, error) {
	runtimeImage, err := openHeldApplicationImage(ctx, fileid.Self, executable)
	if err != nil {
		return "", err
	}
	defer runtimeImage.Close()
	folder, err := fileid.Open(ctx, fileid.Self, filepath.Dir(runtimeImage.program), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return "", err
	}
	defer folder.Close()
	name, err := registryHeldImageToolsRelativeTo(ctx, held, folder)
	if err != nil {
		return "", err
	}
	if err := runtimeImage.Recheck(ctx); err != nil {
		return "", err
	}
	return name, nil
}

func registryHeldImageToolsRelativeTo(ctx context.Context, held *heldApplicationManifest, folder fileid.Handle) (string, error) {
	if err := held.Recheck(ctx); err != nil {
		return "", err
	}
	from, err := fileid.FromFile(ctx, held.file)
	if err != nil {
		return "", err
	}
	defer from.Close()
	relation, err := from.Relate(folder)
	if err != nil {
		return "", err
	}
	if relation.Kind != fileid.RelationInside || relation.MultiName {
		return "", errors.New("shipped:outside")
	}
	name := filepath.ToSlash(relation.Rel)
	// Beneath must name the same held file; no lexical containment or reopened bytes.
	named, err := folder.Beneath(name, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return "", err
	}
	defer named.Close()
	if named.FileID().Key() != held.key {
		return "", errors.New("shipped:outside")
	}
	if err := held.Recheck(ctx); err != nil {
		return "", err
	}
	return name, nil
}

// registryHeldImageSHA256 never reopens a locator. Callers retain this image
// through their conditional effect, and recheck the same hold at that boundary.
func registryHeldImageSHA256(ctx context.Context, held *heldApplicationManifest) (string, error) {
	if err := held.Recheck(ctx); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(held.file, 0, int64(uint64(held.stamp.FileSizeHigh)<<32|uint64(held.stamp.FileSizeLow)))); err != nil {
		return "", err
	}
	if err := held.Recheck(ctx); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// matchesEmbeddedShippedImage recognizes an existing deployment for recipe
// projection. It does not rebind registrations, permissions or startup state.
func matchesEmbeddedShippedImage(ctx context.Context, program, fileKey, executable, embedded string) bool {
	record, err := parseShippedProviderImages(embedded)
	if err != nil || len(record) == 0 {
		return false
	}
	held, err := openRegistryProgramImage(ctx, fileid.Self, program)
	if err != nil {
		return false
	}
	defer held.Close()
	if held.key != fileKey {
		return false
	}
	name, err := registryHeldImageToolsRelativeAt(ctx, held, executable)
	if err != nil || record[name] == "" {
		return false
	}
	digest, err := registryHeldImageSHA256(ctx, held)
	return err == nil && digest == record[name]
}
