package setup

import (
	"errors"
	"path/filepath"
	"regexp"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

const (
	ComfyApplicationName   = "comfyui"
	comfyReadinessName     = "oa-integration-loaded"
	comfyReadinessProto    = "oa-comfyui"
	comfyReadinessContract = "openabstractions.comfyui/integration-ready@1"
	comfyLauncherName      = "oa_comfyui_launch.py"
)

var comfyRecordsDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ComfyApplicationDescriptor returns the one fixed activation recipe for the
// receipt-bound portable app. Registration and activation remain explicit
// public facade operations with their own rights checks.
func ComfyApplicationDescriptor(paths ComfyPaths) (facade.ApplicationDescriptor, error) {
	if paths.install == "" || paths.caller == "" || paths.user == "" || paths.input == "" || paths.temp == "" || paths.output == "" || paths.launch == "" {
		return facade.ApplicationDescriptor{}, errors.New("ComfyUI activation paths are incomplete")
	}
	for _, path := range []string{paths.install, paths.caller, paths.user, paths.input, paths.temp, paths.output, paths.launch} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return facade.ApplicationDescriptor{}, errors.New("ComfyUI activation paths must be absolute and clean")
		}
	}
	if !withinPath(paths.install, paths.caller) {
		return facade.ApplicationDescriptor{}, errors.New("ComfyUI activation program is outside the pinned installation")
	}
	portable := filepath.Join(paths.install, comfyPortableDirectory)
	main := filepath.Join(portable, "ComfyUI", "main.py")
	// The packaged launcher (adopters/comfyui/oa_comfyui_launch.py) checks the
	// private Python against its wheels' RECORD hashes, refuses pip and
	// ensurepip, and loads safetensors weights only, before main.py runs
	// (security review rocm-7.2.1-pytorch-2.9.1-python-3.12.10).
	launcher := filepath.Join(portable, comfyLauncherName)
	if !withinPath(paths.install, main) || !withinPath(paths.install, launcher) {
		return facade.ApplicationDescriptor{}, errors.New("ComfyUI entry point is outside the pinned installation")
	}
	if !comfyRecordsDigest.MatchString(paths.records) {
		return facade.ApplicationDescriptor{}, errors.New("the installed ComfyUI package has no reviewed RECORD-set pin; this OA build cannot activate it")
	}
	readiness := facade.ApplicationInterface{
		Name:     comfyReadinessName,
		Protocol: comfyReadinessProto,
		Contract: comfyReadinessContract,
	}
	return facade.ApplicationDescriptor{
		Name:          ComfyApplicationName,
		Program:       paths.caller,
		Title:         "ComfyUI — OA build",
		StartGuidance: "For the guided OA image example, ComfyUI uses CPU for preview/save tensors while the selected OA image engine performs inference. This recipe makes no claim about local ComfyUI GPU workflows. Readiness means the OA integration loaded; it does not probe ComfyUI's HTTP endpoint.",
		Activation: &facade.ApplicationActivationRecipe{
			Arguments: []string{
				"-B", "-s", launcher,
				"--records", paths.records,
				"--state", paths.launch,
				"--", main,
				"--windows-standalone-build",
				"--cpu",
				"--listen", "127.0.0.1",
				"--port", "8188",
				// ComfyUI's own switch (comfy/cli_args.py, v0.35.0): no
				// API nodes, which call paid remote services, and a CSP
				// that keeps the frontend off api.comfy.org.
				"--disable-api-nodes",
				"--user-directory", paths.user,
				"--input-directory", paths.input,
				"--temp-directory", paths.temp,
				"--output-directory", paths.output,
			},
			Readiness:          readiness,
			ReadinessTimeoutMs: 30000,
		},
	}, nil
}
