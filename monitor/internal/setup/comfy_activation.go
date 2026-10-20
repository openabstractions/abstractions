package setup

import (
	"errors"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"path/filepath"
)

const ComfyApplicationName = "comfyui"

// ComfyApplicationTitle is the title OpenAbstractions' builds of ComfyUI
// register under, the installed build and a developer build alike.
const ComfyApplicationTitle = "ComfyUI — OA build"

// ComfyApplicationDescriptor is receipt-derived display information. Native
// registration reads its signed manifest; historical receipts grant no start.
func ComfyApplicationDescriptor(paths ComfyPaths) (facade.ApplicationDescriptor, error) {
	program := paths.Program()
	if paths.install == "" || !filepath.IsAbs(program) || filepath.Clean(program) != program || !withinPath(paths.install, program) {
		return facade.ApplicationDescriptor{}, errors.New("application program is outside the receipt-bound installation")
	}
	if paths.native == "" && !sha256Hex.MatchString(paths.records) {
		return facade.ApplicationDescriptor{}, errors.New("the installed package has no reviewed RECORD-set pin")
	}
	return facade.ApplicationDescriptor{Name: ComfyApplicationName, Program: program, Title: ComfyApplicationTitle}, nil
}
