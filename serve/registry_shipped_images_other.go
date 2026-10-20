//go:build !windows

package main

import (
	"context"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

func observeShippedRegistrationImage(context.Context, string, *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
	return 0, nil, nil, nil
}

func observeShippedRegistrationImageCause(context.Context, string, *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error, string) {
	return 0, nil, nil, nil, "shipped:outside"
}

func registryHeldImageToolsRelative(context.Context, *heldApplicationManifest) (string, error) {
	return "", errApplicationManifestUnsupported
}

func registryHeldImageToolsRelativeAt(context.Context, *heldApplicationManifest, string) (string, error) {
	return "", errApplicationManifestUnsupported
}

func registryHeldImageSHA256(context.Context, *heldApplicationManifest) (string, error) {
	return "", errApplicationManifestUnsupported
}

func matchesEmbeddedShippedImage(context.Context, string, string, string, string) bool {
	return false
}
