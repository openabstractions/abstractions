package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

// testFolders are three existing folders standing in for {app},
// {local_app_data} and {pictures}, and one folder outside all of them.
func testFolders(t *testing.T) (manifestFolders, string) {
	t.Helper()
	root := t.TempDir()
	folders := manifestFolders{}
	for name, dir := range map[string]string{"app": "app", "local_app_data": "local", "pictures": "pictures"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		folders[name] = path
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	return folders, outside
}

func TestEachPlaceholderResolvesInsideItsFolder(t *testing.T) {
	folders, _ := testFolders(t)
	for value, want := range map[string]string{
		"{app}/bin/app.exe":                   filepath.Join(folders["app"], "bin", "app.exe"),
		`{app}\bin\app.exe`:                   filepath.Join(folders["app"], "bin", "app.exe"),
		`{app}\bin/sub\app.exe`:               filepath.Join(folders["app"], "bin", "sub", "app.exe"),
		"{local_app_data}/OpenAbstractions/x": filepath.Join(folders["local_app_data"], "OpenAbstractions", "x"),
		"{pictures}/OpenAbstractions/x":       filepath.Join(folders["pictures"], "OpenAbstractions", "x"),
	} {
		got, err := folders.resolve(value, false)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", value, got, err, want)
		}
	}
	if got, err := folders.resolve("{app}/bin/app.exe", true); err != nil || got != filepath.Join(folders["app"], "bin", "app.exe") {
		t.Errorf("program: %q %v", got, err)
	}
	for _, literal := range []string{"--cpu", "127.0.0.1", "8188", "-B"} {
		if got, err := folders.resolve(literal, false); err != nil || got != literal {
			t.Errorf("literal %q: %q %v", literal, got, err)
		}
	}
	inside := filepath.Join(folders["local_app_data"], "data")
	if got, err := folders.resolve(inside, false); err != nil || got != inside {
		t.Errorf("an absolute argument inside a folder: %q %v", got, err)
	}
}

func TestPlaceholderPathsThatLeaveTheirFolderAreRefused(t *testing.T) {
	folders, outside := testFolders(t)
	for name, c := range map[string]struct {
		value   string
		program bool
	}{
		"dot-dot out of {app}":        {"{app}/../outside/app.exe", true},
		"dot-dot out of {pictures}":   {"{pictures}/a/../../outside/x", false},
		"backslash dot-dot out":       {`{app}\..\outside\app.exe`, true},
		"mixed dot-dot out":           {`{pictures}\a/..\../outside/x`, false},
		"the folder itself":           {"{local_app_data}", false},
		"the folder and a separator":  {"{pictures}/", false},
		"an absolute path outside":    {filepath.Join(outside, "evil.exe"), false},
		"an absolute program outside": {filepath.Join(outside, "evil.exe"), true},
		"a program in another folder": {"{local_app_data}/app.exe", true},
		"a relative program":          {"app.exe", true},
		"an unknown placeholder":      {"{home}/x", false},
		"a placeholder in the middle": {"--user-directory={app}/x", false},
		"an unclosed placeholder":     {"{app/x", false},
	} {
		if got, err := folders.resolve(c.value, c.program); err == nil {
			t.Errorf("%s: %q resolved to %q", name, c.value, got)
		}
	}
}

func TestTheManifestFileIsResolvedBeforeTheFacadeCheck(t *testing.T) {
	folders, _ := testFolders(t)
	manifest := `{"name":"example","program":"{app}/bin/app.exe","title":"Example","activation":{"arguments":["--state","{local_app_data}/OpenAbstractions/example","--out","{pictures}/OpenAbstractions/example","--cpu"],"readiness":{"name":"ready","protocol":"example","contract":"example/ready@1"},"readiness_timeout_ms":30000}}`
	d, err := client.ParseApplicationDescriptorResolving([]byte(manifest), folders.resolve)
	if err != nil {
		t.Fatal(err)
	}
	if d.Program != filepath.Join(folders["app"], "bin", "app.exe") || d.Activation.Arguments[1] != filepath.Join(folders["local_app_data"], "OpenAbstractions", "example") ||
		d.Activation.Arguments[3] != filepath.Join(folders["pictures"], "OpenAbstractions", "example") || d.Activation.Arguments[4] != "--cpu" {
		t.Fatalf("resolved %+v %v", d, d.Activation.Arguments)
	}
	if _, err := client.ParseApplicationDescriptor([]byte(manifest)); err == nil {
		t.Fatal("an unresolved placeholder passed the Facade's own check")
	}
	escape := strings.Replace(manifest, "{pictures}/OpenAbstractions/example", "{pictures}/../../evil", 1)
	if _, err := client.ParseApplicationDescriptorResolving([]byte(escape), folders.resolve); err == nil {
		t.Fatal("an argument that leaves {pictures} was accepted")
	}
}

// The showcase applications' own manifests (adopters/<app>/), which their
// MSIs ship, resolve inside the folders and pass the Facade's check. A tree
// without them, such as the published monitor module, skips.
func TestTheShowcaseManifestsResolve(t *testing.T) {
	folders, _ := testFolders(t)
	for _, app := range []string{"comfyui"} {
		data, err := os.ReadFile(filepath.Join("..", "adopters", app, "application-manifest.json"))
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("no adopters/%s in this tree", app)
		}
		if err != nil {
			t.Fatal(err)
		}
		// build_app.py fills the release's RECORD-set pin.
		filled := strings.ReplaceAll(string(data), "@records_sha256@", strings.Repeat("0", 64))
		d, err := client.ParseApplicationDescriptorResolving([]byte(filled), folders.resolve)
		if err != nil {
			t.Fatalf("%s: %v", app, err)
		}
		if d.Name != app {
			t.Fatalf("%s names %q", app, d.Name)
		}
		if inside, err := identity.Within(d.Program, folders["app"]); err != nil || !inside {
			t.Fatalf("%s program %s is not in {app}: %v", app, d.Program, err)
		}
	}
}

// venvProgram writes a stand-in interpreter folder and a program in {app}
// with the pyvenv.cfg Python would read for it, naming that folder as home.
// link makes the program a hard link to home's interpreter, not a copy.
func venvProgram(t *testing.T, folders manifestFolders, outside, relative, configFolder string, link bool) string {
	t.Helper()
	home := filepath.Join(outside, "Python312")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "python.exe")
	if err := os.WriteFile(shared, []byte("the shared interpreter"), 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(folders["app"], filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if link {
		if err := os.Link(shared, program); err != nil {
			t.Skipf("no hard links on this volume: %v", err)
		}
	} else if err := os.WriteFile(program, []byte("the shared interpreter"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(folders["app"], filepath.FromSlash(configFolder), "pyvenv.cfg")
	if err := os.WriteFile(config, []byte("Home = "+home+"\r\ninclude-system-site-packages = false\r\nhome = "+outside+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return program
}

// A program that is the same file as its venv's home interpreter is refused
// (VISION 2026-10-07); a copy in the application's folder is the
// application's own program.
func TestAProgramThatIsItsVenvsSharedInterpreterIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		relative, config string
		link, refused    bool
	}{
		"a copy beside its pyvenv.cfg":        {"helper/python.exe", "helper", false, false},
		"a hard link beside its pyvenv.cfg":   {"helper/python.exe", "helper", true, true},
		"a renamed hard link":                 {"helper/comfyui.exe", "helper", true, true},
		"a hard link below the venv's folder": {"venv/Scripts/python.exe", "venv", true, true},
		"a copy below the venv's folder":      {"venv/Scripts/python.exe", "venv", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			folders, outside := testFolders(t)
			program := venvProgram(t, folders, outside, c.relative, c.config, c.link)
			got, err := folders.resolve("{app}/"+c.relative, true)
			if c.refused {
				if err == nil || !strings.Contains(err.Error(), "same file as the shared interpreter") {
					t.Fatalf("%s resolved to %q, %v", program, got, err)
				}
				return
			}
			if err != nil || got != program {
				t.Fatalf("%q %v, want %q", got, err, program)
			}
		})
	}
}

func TestAProgramWithoutAVenvIsNotAsked(t *testing.T) {
	folders, _ := testFolders(t)
	program := filepath.Join(folders["app"], "bin", "app.exe")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := folders.resolve("{app}/bin/app.exe", true); err != nil || got != program {
		t.Fatalf("%q %v", got, err)
	}
}

func TestAVenvHomeThatIsNotAbsoluteOrLocalIsRefused(t *testing.T) {
	for name, home := range map[string]string{"relative": "Python312", "a share": `\\server\share\Python312`} {
		t.Run(name, func(t *testing.T) {
			if name == "a share" && !filepath.IsAbs(home) {
				t.Skip("a UNC path is a Windows path")
			}
			folders, _ := testFolders(t)
			program := filepath.Join(folders["app"], "helper", "python.exe")
			if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(program, []byte("copy"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(program), "pyvenv.cfg"), []byte("home = "+home+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := folders.resolve("{app}/helper/python.exe", true); err == nil {
				t.Fatalf("resolved to %q", got)
			}
		})
	}
}

func TestTheFirstHomeLineIsTheOnePythonReads(t *testing.T) {
	for text, want := range map[string]string{
		"home = C:\\A\nhome = C:\\B\n":      `C:\A`,
		"\xef\xbb\xbf HOME\t=  C:\\A  \r\n": `C:\A`,
		"version = 3.12\n":                  "",
		"homes = C:\\A\nhome=\n":            "",
		"include-system-site-packages = no": "",
	} {
		if got := venvConfigHome([]byte(text)); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// The manifest the ComfyUI extension's Connect action writes beside itself
// (adopters/comfyui/node/oa_connect.py) names its helper copy, which lies in
// {app}: the Panel reads it with no change. OA_CONNECT_MANIFEST names one
// such file written by a real Connect run; without it, the test writes the
// same shape itself.
func TestAConnectManifestNamingItsHelperCopyIsRead(t *testing.T) {
	path := os.Getenv("OA_CONNECT_MANIFEST")
	if path == "" {
		folders, outside := testFolders(t)
		program := venvProgram(t, folders, outside, "helper/python.exe", "helper", false)
		manifest, err := json.Marshal(map[string]string{"name": "comfyui", "program": program, "title": "ComfyUI",
			"start_guidance": "Start ComfyUI yourself. OpenAbstractions does not start it."})
		if err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(folders["app"], "application-manifest.json")
		if err := os.WriteFile(path, manifest, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := readApplicationManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if inside, err := identity.Within(d.Program, filepath.Dir(path)); err != nil || !inside {
		t.Fatalf("program %s is not beside the manifest: %v", d.Program, err)
	}
}

// A pyvenv.cfg that is a symbolic link is refused: the file Python reads
// could then differ from the one checked.
func TestALinkedVenvConfigIsRefused(t *testing.T) {
	folders, outside := testFolders(t)
	program := venvProgram(t, folders, outside, "helper/python.exe", "helper", false)
	config := filepath.Join(filepath.Dir(program), "pyvenv.cfg")
	elsewhere := filepath.Join(outside, "pyvenv.cfg")
	if err := os.Rename(config, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, config); err != nil {
		t.Skipf("no symbolic links for this account: %v", err)
	}
	if got, err := folders.resolve("{app}/helper/python.exe", true); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("resolved to %q, %v", got, err)
	}
}
func TestAVenvConfigThatIsNotAFileIsRefused(t *testing.T) {
	folders, outside := testFolders(t)
	program := venvProgram(t, folders, outside, "helper/python.exe", "helper", false)
	config := filepath.Join(filepath.Dir(program), "pyvenv.cfg")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := folders.resolve("{app}/helper/python.exe", true); err == nil || !strings.Contains(err.Error(), "not a file") {
		t.Fatalf("resolved to %q, %v", got, err)
	}
}
