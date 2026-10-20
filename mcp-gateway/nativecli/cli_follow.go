package nativecli

import (
	"context"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// A vendor that updates its agent CLI in place moves it to a new version
// folder of its own installation and removes the old one. OA saves a CLI path
// as a locator inside that installation. Following maps the locator to the
// version the vendor has installed now; the caller then authenticates that
// image exactly as it authenticated the saved one. Each vendor layout names
// the installation root of a path, or reports that the path is outside it.
type cliInstallRoot func(program string) (string, bool)

// sameCLIInstall reports whether two locators are one path or two versions
// in one installation of the layout. It grants no execution authority.
func sameCLIInstall(root cliInstallRoot, a, b string, sameRoot func(string, string) bool) bool {
	if a == b {
		return true
	}
	rootA, okA := root(a)
	rootB, okB := root(b)
	return okA && okB && sameRoot(rootA, rootB)
}

func sameCLIInstallRoot(a, b string) bool {
	same, err := fileid.SameFile(a, b)
	return err == nil && same
}

// followingImage follows a saved locator with the vendor layout's follow, then
// holds and authenticates the image it names with hold: registered publisher,
// PE and file identity, as for the saved path itself.
func followingImage[I any](follow func(context.Context, string) (string, error), hold func(context.Context, string) (I, error)) func(context.Context, string) (I, error) {
	return func(ctx context.Context, program string) (I, error) {
		current, err := follow(ctx, program)
		if err != nil {
			var none I
			return none, err
		}
		return hold(ctx, current)
	}
}
