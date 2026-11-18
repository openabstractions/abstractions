package product

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// A-SEC6, architect ruling 2026-11-22 (1): standing needs a shipped program
// and a name the table lists for that program. The localai case: a shipped
// program declared under a name it does not ship has no standing.
func TestShippedProgramNeedsAShippedProgramAndItsOwnName(t *testing.T) {
	machine, elsewhere := t.TempDir(), t.TempDir()
	protected := []string{machine}
	for _, row := range []struct {
		program, name string
		want          bool
	}{
		{filepath.Join(machine, exe("inventoryd")), "local-stores", true},
		{filepath.Join(machine, exe("inventoryd")), "oa-setup-chat-467d9c6e43391a07c5853578", true},
		{filepath.Join(machine, exe("inventoryd")), "oa-setup-chat-", false},
		{filepath.Join(machine, exe("inventoryd")), "oa-setup-chat-XYZ", false},
		{filepath.Join(machine, exe("modelhostd")), ModelHost, true},
		{filepath.Join(machine, exe("pytorchd")), Diffusers, true},
		{filepath.Join(machine, exe("pytorchd")), DiffusersModelsHost, true},
		{filepath.Join(machine, exe("lemonaded")), Lemonade, true},
		{filepath.Join(machine, exe("thenoise-provider")), TheNoise, true},
		// A shipped program under a name it does not ship.
		{filepath.Join(machine, exe("inventoryd")), "localai", false},
		{filepath.Join(machine, exe("openabstractions")), "localai", false},
		{filepath.Join(machine, exe("modelhostd")), Lemonade, false},
		// The right name on a program outside the installation.
		{filepath.Join(elsewhere, exe("modelhostd")), ModelHost, false},
		{exe("modelhostd"), ModelHost, false},
		{"", ModelHost, false},
	} {
		if got := ShippedProgram(row.program, row.name, protected, nil, nil); got != row.want {
			t.Errorf("ShippedProgram(%q, %q) = %v, want %v", row.program, row.name, got, row.want)
		}
	}
}

// Every engine name the product states and every declaration the installer
// places is in the table under its own program.
func TestComponentTableCoversShippedDeclarations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tools")
	for _, name := range []string{ModelHost, Diffusers, DiffusersModelsHost, Lemonade, TheNoise} {
		found := false
		for _, c := range ProviderComponents() {
			found = found || slices.Contains(c.Names, name) && ShipsProvider(filepath.Join(root, exe(c.Program)), name)
		}
		if !found {
			t.Errorf("engine %q is in no component's names", name)
		}
	}

}

// Installer declarations are private-workspace inputs; the component table's
// own engine coverage above also runs in the published module.
func TestComponentTableCoversInstallerDeclarations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tools")
	for _, dir := range []string{
		privateInput(t, "..", "..", "installer", "declarations"),
		privateInput(t, "..", "..", "installer", "posix", "declarations"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var file struct {
				Program   string `json:"program"`
				Providers []struct {
					Name string `json:"name"`
				} `json:"providers"`
			}
			if err := json.Unmarshal(raw, &file); err != nil {
				t.Fatal(entry.Name(), err)
			}
			for _, p := range file.Providers {
				program := file.Program
				if runtime.GOOS != "windows" {
					program = filepath.Base(program[:len(program)-len(filepath.Ext(program))])
				}
				if !ShipsProvider(filepath.Join(root, program), p.Name) {
					t.Errorf("%s: %s under %s is not in the component table", entry.Name(), p.Name, file.Program)
				}
			}
		}
	}
}

// Declarer amendment of 2026-11-22: a recorded declarer resolves to a
// component by identity. In the per-machine folder the file name names it;
// in a signed folder only a declarer with a signed version resource
// resolves, and only when its OriginalFilename matches, so a renamed copy of
// the command line is no Panel. The command line and the MCP gateway are
// never declarers, and a component is a declarer only of the names the
// table lists for it.
func TestDeclarerComponentResolvesByIdentity(t *testing.T) {
	machine, user, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	protected, signed := []string{machine}, []string{user}
	sameSigner := func(string) bool { return true }
	original := map[string]string{}
	originalName := func(program string) string { return original[program] }
	panelName := "Abstraction Panel"
	for _, row := range []struct {
		program, original, want string
	}{
		{filepath.Join(machine, exe(panelName)), "", "oa:panel"},
		{filepath.Join(machine, exe("modelhostd")), "", "oa:modelhost"},
		{filepath.Join(machine, exe("openabstractions")), "", "oa:cli"},
		{filepath.Join(machine, exe("openabstractions-mcp")), "", "oa:mcp"},
		{filepath.Join(user, exe(panelName)), "Abstraction Panel.exe", "oa:panel"},
		{filepath.Join(user, exe(panelName)), "openabstractions.exe", ""},
		{filepath.Join(user, exe("modelhostd")), "", ""},
		{filepath.Join(elsewhere, exe(panelName)), "Abstraction Panel.exe", ""},
		{filepath.Join(machine, exe("some-app")), "", ""},
	} {
		original[row.program] = row.original
		if got := DeclarerComponent(row.program, protected, signed, sameSigner, originalName); got != row.want {
			t.Errorf("DeclarerComponent(%s, %q) = %q, want %q", row.program, row.original, got, row.want)
		}
	}
	pytorch, modelhost := filepath.Join(machine, exe("pytorchd")), filepath.Join(machine, exe("modelhostd"))
	for _, row := range []struct {
		program, name, component string
		want                     bool
	}{
		{pytorch, Diffusers, "oa:panel", true},
		{filepath.Join(machine, exe("thenoise-provider")), TheNoise, "oa:panel", true},
		{modelhost, ModelHost, "oa:panel", true},
		{pytorch, DiffusersModelsHost, "oa:panel", true},
		{modelhost, ModelHost, "oa:modelhost", false},
		{modelhost, ModelHost, "oa:cli", false},
		{pytorch, Diffusers, "oa:cli", false},
		{pytorch, Diffusers, "oa:mcp", false},
		{filepath.Join(machine, exe("lemonaded")), Lemonade, "oa:panel", false},
		{pytorch, Diffusers, "", false},
	} {
		if got := ListsDeclarer(row.program, row.name, row.component); got != row.want {
			t.Errorf("ListsDeclarer(%s, %s, %q) = %v, want %v", filepath.Base(row.program), row.name, row.component, got, row.want)
		}
	}
}
