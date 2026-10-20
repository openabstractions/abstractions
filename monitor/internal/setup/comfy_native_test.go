package setup

import (
	"path/filepath"
	"testing"
)

// The native package's host makes ComfyUI's OA calls: it starts ComfyUI with
// ABSTRACTION_COMFYUI_NATIVE_RELAY naming its own image
// (adopters/comfyui/installer/backendlaunch runOptions), and ComfyUI's relay
// runs "<host> native-relay" (adopters/comfyui/test_connect_node.py). So the
// one program the registration and Setup's granted rules name is the host,
// never the venv interpreter the package also carries.
func TestComfyNativeProgramIsTheRelayHost(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	_, a, _, _ := nativeArchiveFixture(t)
	p, err := comfyPathsForArtifact(a)
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(p.install, a.ApplicationProgram)
	if p.Program() != host || p.caller != filepath.Join(p.install, filepath.FromSlash(a.ServiceProgram)) || p.Program() == p.caller {
		t.Fatalf("the relay host and the venv interpreter are conflated: %+v", p)
	}
	d, err := ComfyApplicationDescriptor(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Program != host || d.Activation != nil || d.StartGuidance != "" {
		t.Fatalf("the registration names another program or supplies native manifest authority: %+v", d)
	}
	p.native = filepath.Join(t.TempDir(), "outside.exe")
	if _, err := ComfyApplicationDescriptor(p); err == nil {
		t.Fatal("outside program substituted for receipt program")
	}
}
