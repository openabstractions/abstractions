//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const webviewClientKey = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
const webviewCreateExport = "CreateWebViewEnvironmentWithOptionsInternal"

var webviewRuntime struct {
	sync.Once
	dll    *windows.DLL
	create *windows.Proc
	err    error
}

// installedWebViewPaths reads the Evergreen registration. It never searches
// PATH, the working directory, or other applications' private runtimes.
func installedWebViewPaths() []string {
	var paths []string
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_32KEY, registry.WOW64_64KEY} {
			key, err := registry.OpenKey(root, webviewClientKey, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			location, _, locErr := key.GetStringValue("location")
			version, _, verErr := key.GetStringValue("pv")
			closeErr := key.Close()
			if locErr != nil || verErr != nil || closeErr != nil {
				continue
			}
			path, err := webviewRuntimePath(location, version, runtime.GOARCH)
			if err == nil {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

func webviewRuntimePath(location, version, arch string) (string, error) {
	if !filepath.IsAbs(location) {
		return "", errors.New("WebView2 location must be absolute")
	}
	parts := strings.Split(version, ".")
	if len(parts) != 4 {
		return "", errors.New("invalid WebView2 version")
	}
	for _, part := range parts {
		if part == "" {
			return "", errors.New("invalid WebView2 version")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return "", errors.New("invalid WebView2 version")
			}
		}
	}
	platform := map[string]string{"amd64": "x64", "386": "x86", "arm64": "arm64"}[arch]
	if platform == "" {
		return "", errors.New("unsupported WebView2 architecture")
	}
	return filepath.Join(location, version, "EBWebView", platform, "EmbeddedBrowserWebView.dll"), nil
}

// Microsoft's documented runtime export avoids redistributing a loader DLL.
// It may change in future runtimes; failing to find it preserves the browser
// fallback. DLL dependencies resolve within the installation and System32.
// https://learn.microsoft.com/microsoft-edge/webview2/reference/win32/webview2-idl#createwebviewenvironmentwithoptionsinternal
func webviewCreateProc() (*windows.Proc, error) {
	webviewRuntime.Do(func() {
		webviewRuntime.err = errors.New("installed WebView2 runtime not found")
		for _, path := range installedWebViewPaths() {
			if info, err := os.Stat(path); err != nil || info.IsDir() {
				continue
			}
			handle, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
			if err != nil {
				webviewRuntime.err = err
				continue
			}
			dll := &windows.DLL{Name: path, Handle: handle}
			proc, err := dll.FindProc(webviewCreateExport)
			if err != nil {
				webviewRuntime.err = errors.Join(err, dll.Release())
				continue
			}
			webviewRuntime.dll, webviewRuntime.create, webviewRuntime.err = dll, proc, nil
			return
		}
	})
	if webviewRuntime.err != nil {
		return nil, fmt.Errorf("WebView2 runtime: %w", webviewRuntime.err)
	}
	return webviewRuntime.create, nil
}

// WebView2Available checks the runtime registration, loads its DLL and resolves
// the entry point. It does not create a browser process or a profile.
func WebView2Available() bool {
	_, err := webviewCreateProc()
	return err == nil
}
