package main

import (
	"bytes"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// Receive the actual Microsoft resource compiler output without executing the
// image. Quoting a named identifier can compile successfully with the quotes
// retained in the PE name; source-string comparisons cannot detect that error.
func TestCompiledNativeResourceNames(t *testing.T) {
	vcvars, err := findVCVars64()
	if err != nil {
		t.Skip(err)
	}
	panelDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	_, monitor, err := layout(panelDir)
	if err != nil {
		t.Fatal(err)
	}
	panel, err := resourceScript("0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	comfy := filepath.Join(filepath.Dir(monitor), "adopters/comfyui/installer/backendlaunch")
	connector := filepath.Join(filepath.Dir(monitor), "adopters/comfyui/connector")
	// The charter carries only the canonical inputs used by this proof.
	projected := filepath.Join(monitor, "icon", "gen", "testdata", "comfy-native-resources")
	if _, err := os.Stat(projected); err == nil {
		comfy = filepath.Join(projected, "launcher")
		connector = filepath.Join(projected, "connector")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	comfyRC, err := os.ReadFile(filepath.Join(comfy, "native_resources.rc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, source string
		recipe       []byte
		members      map[string]string
		extra        []string
		object       string
	}{
		{"Panel", monitor, panel, map[string]string{"OA_APPLICATION_MANIFEST": "panel-application.json"}, []string{"icon/panel.ico", "panel.manifest"}, ""},
		{"PanelTrackedObject", monitor, nil, map[string]string{"OA_APPLICATION_MANIFEST": "panel-application.json"}, nil, filepath.Join("internal", "panelres", "rsrc_windows_amd64.syso")},
		{"PanelTrackedARM64Object", monitor, nil, map[string]string{"OA_APPLICATION_MANIFEST": "panel-application.json"}, nil, filepath.Join("internal", "panelres", "rsrc_windows_arm64.syso")},
		// The presenter carries no application manifest; its version
		// resource is checked by scripts/train_version.py.
		{"PresenterTrackedObject", monitor, nil, nil, nil, filepath.Join("internal", "presenterres", "rsrc_windows_amd64.syso")},
		{"PresenterTrackedARM64Object", monitor, nil, nil, nil, filepath.Join("internal", "presenterres", "rsrc_windows_arm64.syso")},
		// The ComfyUI connector's tracked object carries its manifest; the
		// registration service reads it from the connector's image.
		{"ComfyConnectorTrackedObject", connector, nil, map[string]string{"OA_APPLICATION_MANIFEST": "application-manifest.json"}, nil, "rsrc_windows_amd64.syso"},
		{"Comfy", comfy, comfyRC, map[string]string{"OA_APPLICATION_MANIFEST": "application-manifest.json", "OA_BACKEND_PIN": "backend-pin.json", "OA_BACKEND_MANIFEST": "backend-manifest.json", "OA_CONTROL_PIN": "control-pin.json"}, nil, ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			files := append([]string{}, fixture.extra...)
			for _, member := range fixture.members {
				files = append(files, member)
			}
			for _, name := range files {
				data, err := os.ReadFile(filepath.Join(fixture.source, name))
				if err != nil {
					t.Fatal(err)
				}
				dst := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dst, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.rc"), fixture.recipe, 0600); err != nil {
				t.Fatal(err)
			}
			// A resource-only image has no entry point and is loaded as data only.
			bat := fmt.Sprintf("@echo off\r\ncall \"%s\" >nul || exit /b 1\r\nrc.exe /nologo /fo fixture.res fixture.rc || exit /b 1\r\nlink.exe /nologo /dll /noentry /machine:x64 /out:fixture.dll fixture.res || exit /b 1\r\n", vcvars)
			if fixture.object != "" {
				machine := "x64"
				if filepath.Base(fixture.object) == "rsrc_windows_arm64.syso" {
					machine = "arm64"
				}
				bat = fmt.Sprintf("@echo off\r\ncall \"%s\" >nul || exit /b 1\r\nlink.exe /nologo /dll /noentry /machine:%s /out:fixture.dll \"%s\" || exit /b 1\r\n", vcvars, machine, filepath.Join(fixture.source, fixture.object))
			}
			if err := os.WriteFile(filepath.Join(dir, "compile.cmd"), []byte(bat), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("cmd.exe", "/c", filepath.Join(dir, "compile.cmd"))
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("resource compiler: %v\n%s", err, out)
			}
			kernel := windows.NewLazySystemDLL("kernel32.dll")
			image, err := syscall.UTF16PtrFromString(filepath.Join(dir, "fixture.dll"))
			if err != nil {
				t.Fatal(err)
			}
			h, _, callErr := kernel.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(image)), 0, 0x20|2)
			if h == 0 {
				t.Fatal(callErr)
			}
			defer syscall.FreeLibrary(syscall.Handle(h))
			for name, member := range fixture.members {
				id, _ := syscall.UTF16PtrFromString(name)
				resource, _, callErr := kernel.NewProc("FindResourceExW").Call(h, 10, uintptr(unsafe.Pointer(id)), 0)
				if resource == 0 {
					t.Errorf("plain neutral resource %s absent: %v", name, callErr)
					continue
				}
				size, _, _ := kernel.NewProc("SizeofResource").Call(h, resource)
				loaded, _, _ := kernel.NewProc("LoadResource").Call(h, resource)
				data, _, _ := kernel.NewProc("LockResource").Call(loaded)
				want, err := os.ReadFile(filepath.Join(fixture.source, member))
				if err != nil {
					t.Fatal(err)
				}
				if size != uintptr(len(want)) || data == 0 || !bytes.Equal(unsafe.Slice((*byte)(nativePointer(data)), int(size)), want) {
					t.Errorf("resource %s changed payload", name)
				}
				quoted, _ := syscall.UTF16PtrFromString("\"" + name + "\"")
				if found, _, _ := kernel.NewProc("FindResourceExW").Call(h, 10, uintptr(unsafe.Pointer(quoted)), 0); found != 0 {
					t.Errorf("quoted resource alias %s present", name)
				}
			}
		})
	}
}
