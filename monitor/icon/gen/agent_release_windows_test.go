package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCompiledAgentReleaseResource(t *testing.T) {
	vcvars, err := findVCVars64()
	if err != nil {
		t.Skip(err)
	}
	// Exact private parser grammar. This image has no code or entry point.
	body := []byte(`{"channel":"stable","component":"openabstractions-mcp","needs":{"files":[],"models":[]},"release":[0,8,0,1],"requests":[],"tools":[{"inputSchema":{"type":"object"},"name":"fixture"}],"verbs":[],"version":1}`)
	for _, resourceName := range []string{"OA_AGENT_RELEASE", "OA_INSTALLER_GATEWAY_HISTORY"} {
		panelDir := ""
		if resourceName == "OA_INSTALLER_GATEWAY_HISTORY" {
			panelDir, err = filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
		}
		body := body
		if resourceName == "OA_INSTALLER_GATEWAY_HISTORY" {
			body = []byte(`{"predecessors":[],"schema":1}`)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(resourceName+"/"+arch, func(t *testing.T) {
				dir := t.TempDir()
				payload, object := filepath.Join(dir, "agent-release.json"), filepath.Join(dir, "rsrc_windows_"+arch+".syso")
				if err := os.WriteFile(payload, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := buildNativeDataObject(payload, object, arch, vcvars, resourceName, panelDir); err != nil {
					t.Fatal(err)
				}
				if err := buildAgentReleaseObject(payload, object, arch, vcvars); err == nil {
					t.Fatal("existing resource object replaced")
				}
				machine := "x64"
				if arch == "arm64" {
					machine = "arm64"
				}
				bat := fmt.Sprintf("@echo off\r\ncall \"%s\" >nul || exit /b 1\r\nlink.exe /nologo /dll /noentry /machine:%s /out:fixture.dll \"%s\" || exit /b 1\r\n", vcvars, machine, object)
				path := filepath.Join(dir, "link.cmd")
				if err := os.WriteFile(path, []byte(bat), 0600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("cmd.exe", "/c", path)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture resource link: %v\n%s", err, out)
				}
				kernel := windows.NewLazySystemDLL("kernel32.dll")
				image, _ := syscall.UTF16PtrFromString(filepath.Join(dir, "fixture.dll"))
				h, _, callErr := kernel.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(image)), 0, 0x20|2)
				if h == 0 {
					t.Fatal(callErr)
				}
				defer syscall.FreeLibrary(syscall.Handle(h))
				name, _ := syscall.UTF16PtrFromString(resourceName)
				resource, _, callErr := kernel.NewProc("FindResourceExW").Call(h, 10, uintptr(unsafe.Pointer(name)), 0)
				if resource == 0 {
					t.Fatal("neutral unquoted OA_AGENT_RELEASE absent", callErr)
				}
				size, _, _ := kernel.NewProc("SizeofResource").Call(h, resource)
				loaded, _, _ := kernel.NewProc("LoadResource").Call(h, resource)
				data, _, _ := kernel.NewProc("LockResource").Call(loaded)
				if data == 0 || size != uintptr(len(body)) || !bytes.Equal(unsafe.Slice((*byte)(nativePointer(data)), int(size)), body) {
					t.Fatal("linked resource changed producer bytes")
				}
				quoted, _ := syscall.UTF16PtrFromString(`"` + resourceName + `"`)
				if found, _, _ := kernel.NewProc("FindResourceExW").Call(h, 10, uintptr(unsafe.Pointer(quoted)), 0); found != 0 {
					t.Fatal("quoted resource alias admitted")
				}
				if resourceName == "OA_INSTALLER_GATEWAY_HISTORY" {
					// Go accepts one resource-bearing object. Link the real combined
					// icon/manifest/history object into inert Go code, without running it.
					if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26.0\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
						t.Fatal(err)
					}
					// GOROOT is set by the source-check runner; an ordinary
					// `go test` finds the go command on PATH.
					goBinary := filepath.Join(os.Getenv("GOROOT"), "bin", "go.exe")
					if os.Getenv("GOROOT") == "" {
						if found, err := exec.LookPath("go"); err == nil {
							goBinary = found
						}
					}
					linked := filepath.Join(dir, "linked.exe")
					build := exec.Command(goBinary, "build", "-o", linked, ".")
					build.Dir = dir
					build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0", "GOOS=windows", "GOARCH="+arch)
					if out, err := build.CombinedOutput(); err != nil {
						t.Fatalf("combined Panel resource Go link: %v\n%s", err, out)
					}
					image, _ := syscall.UTF16PtrFromString(linked)
					goImage, _, err := kernel.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(image)), 0, 0x20|2)
					if goImage == 0 {
						t.Fatal(err)
					}
					defer syscall.FreeLibrary(syscall.Handle(goImage))
					for _, kind := range []uintptr{14, 24} {
						if found, _, _ := kernel.NewProc("FindResourceW").Call(goImage, 1, kind); found == 0 {
							t.Fatal("Go-linked Panel icon/manifest absent", kind)
						}
					}
					if found, _, _ := kernel.NewProc("FindResourceExW").Call(goImage, 10, uintptr(unsafe.Pointer(name)), 0); found == 0 {
						t.Fatal("Go-linked history absent")
					}
				}
			})
		}
	}
}
