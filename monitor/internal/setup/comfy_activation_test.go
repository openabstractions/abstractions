package setup

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestComfyDescriptorKeepsHistoricalReceiptReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "historical-install")
	paths, err := comfyPathsAt(root, filepath.Join(root, filepath.FromSlash(comfyPythonRelativePath)), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComfyApplicationDescriptor(paths); err == nil {
		t.Fatal("unreviewed record set accepted")
	}
	paths.records = strings.Repeat("a", 64)
	d, err := ComfyApplicationDescriptor(paths)
	if err != nil {
		t.Fatal(err)
	}
	if d.Program != paths.Program() || d.Activation != nil || d.StartGuidance != "" {
		t.Fatalf("historical receipt supplied obsolete activation: %+v", d)
	}
	paths.caller = filepath.Join(t.TempDir(), "outside.exe")
	if _, err := ComfyApplicationDescriptor(paths); err == nil {
		t.Fatal("outside program accepted")
	}
}

func TestComfyNativeDescriptorBindsTheHostWithoutGrantingActivation(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	_, artifact, _, _ := nativeArchiveFixture(t)
	paths, err := comfyPathsForArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := ComfyApplicationDescriptor(paths)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Program != paths.Program() || filepath.Base(descriptor.Program) != artifact.ApplicationProgram ||
		descriptor.Program == paths.caller || descriptor.Activation != nil || descriptor.StartGuidance != "" {
		t.Fatalf("native descriptor lost its host identity or invented activation: %+v", descriptor)
	}
}
