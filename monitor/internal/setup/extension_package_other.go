//go:build !windows

package setup

import (
	"context"
	"errors"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

var errExtensionWindowsOnly = errors.New("This integration operation requires Windows.")

func checkFreshExtensionPackage(context.Context, string) error { return errExtensionWindowsOnly }
func connectExtensionPackageWithEffect(context.Context, string, string, product.PackageSpec, extensionPackageMode, func() error) error {
	return errExtensionWindowsOnly
}

func runExtensionRegistration(context.Context, string) (string, string, error) {
	return "", "", errExtensionWindowsOnly
}

func startDesktopProgram(string) error { return errExtensionWindowsOnly }
