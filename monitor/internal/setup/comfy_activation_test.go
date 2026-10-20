package setup

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestComfyApplicationDescriptorUsesReceiptBoundProgramAndFixedArgs(t *testing.T) {
	// The install root holds the archive's top-level folder, as
	// OpenInstalledComfy resolves it.
	root := filepath.Join(t.TempDir(), "comfyui-0.35.0")
	local := filepath.Join(t.TempDir(), "Local")
	paths, err := comfyPathsAt(root,
		filepath.Join(root, filepath.FromSlash(comfyPythonRelativePath)),
		local, filepath.Join(t.TempDir(), "Pictures"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComfyApplicationDescriptor(paths); err == nil || !strings.Contains(err.Error(), "no reviewed RECORD-set pin") {
		t.Fatalf("a package without a RECORD-set pin got an activation recipe: %v", err)
	}
	paths.records = strings.Repeat("ab", 32)
	descriptor, err := ComfyApplicationDescriptor(paths)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Name != "comfyui" || descriptor.Program != paths.CallerProgram() || descriptor.Activation == nil {
		t.Fatalf("unexpected descriptor identity: %+v", descriptor)
	}
	portable := filepath.Join(root, "ComfyUI_windows_portable")
	if filepath.Dir(filepath.Dir(paths.CallerProgram())) != portable {
		t.Fatalf("embedded Python %s is not in %s", paths.CallerProgram(), portable)
	}
	if paths.LaunchStateDirectory() != filepath.Join(local, "OpenAbstractions", "ComfyUI", "launch") {
		t.Fatalf("launch state directory = %s", paths.LaunchStateDirectory())
	}
	want := []string{
		// main.py and the launcher sit beside python_embeded in the portable
		// folder; the launcher proves the private Python before main.py runs.
		"-B", "-s", filepath.Join(portable, "oa_comfyui_launch.py"),
		"--records", strings.Repeat("ab", 32),
		"--state", paths.LaunchStateDirectory(),
		"--", filepath.Join(portable, "ComfyUI", "main.py"),
		"--windows-standalone-build", "--cpu", "--listen", "127.0.0.1", "--port", "8188",
		"--disable-api-nodes",
		"--user-directory", paths.UserDirectory(),
		"--input-directory", paths.InputDirectory(),
		"--temp-directory", paths.TempDirectory(),
		"--output-directory", paths.OutputDirectory(),
	}
	if !reflect.DeepEqual(descriptor.Activation.Arguments, want) {
		t.Fatalf("activation args = %#v, want %#v", descriptor.Activation.Arguments, want)
	}
	if descriptor.Activation.Readiness.Name != comfyReadinessName ||
		descriptor.Activation.Readiness.Protocol != comfyReadinessProto ||
		descriptor.Activation.Readiness.Contract != comfyReadinessContract ||
		descriptor.Activation.ReadinessTimeoutMs != 30000 {
		t.Fatalf("unexpected claimed readiness: %+v", descriptor.Activation)
	}
	for _, arg := range descriptor.Activation.Arguments {
		if arg == "--disable-auto-launch" || arg == "--disable-metadata" {
			t.Fatalf("activation contains test-only or privacy-changing arg %q", arg)
		}
	}
}

func TestComfyApplicationDescriptorRejectsPathsOutsideInstall(t *testing.T) {
	paths := ComfyPaths{
		install: filepath.Join(t.TempDir(), "install"),
		caller:  filepath.Join(t.TempDir(), "outside", "python.exe"),
		user:    filepath.Join(t.TempDir(), "user"),
		input:   filepath.Join(t.TempDir(), "input"),
		temp:    filepath.Join(t.TempDir(), "temp"),
		output:  filepath.Join(t.TempDir(), "output"),
		launch:  filepath.Join(t.TempDir(), "launch"),
		records: strings.Repeat("ab", 32),
	}
	if _, err := ComfyApplicationDescriptor(paths); err == nil {
		t.Fatal("program outside the pinned install was accepted")
	}
}
