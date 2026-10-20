package nativecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/placetest"
	"golang.org/x/sys/windows"
)

func TestClaudeCodeCommandLine(t *testing.T) {
	for line, want := range map[string]bool{
		`"C:\Program Files\nodejs\node.exe" C:\Users\p\AppData\Roaming\npm\node_modules\@anthropic-ai\claude-code\cli.js`: true,
		`node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js --resume x`:                                          true,
		`node "--require=@anthropic-ai/claude-code" C:\work\bootstrap.js`:                                                 true,
		`node --inspect "C:\Applications and Tools\CLAUDE-CODE\cli.cjs" --resume abc`:                                     true,
		`node C:\work\server.js --log "mention @anthropic-ai/claude-code in docs"`:                                        false,
		`node C:\work\@anthropic-ai\claude-code-sample\cli.js`:                                                            false,
		`"C:\Program Files\nodejs\node.exe" C:\work\server.js`:                                                            false,
		``: false,
	} {
		if got, err := claudeCodeCommandLine(line); err != nil || got != want {
			t.Fatal(line, got, err)
		}
	}
}

func TestClaudeCodeCommandLineDetectsNodeStartupCode(t *testing.T) {
	const code = `import(require('node:url').pathToFileURL(require('node:path').resolve('@anthropic-ai/claude-code','cli.js')).href)`
	const module = "@anthropic-ai/claude-code"
	const require = "require('" + module + "')"
	const sample = `require('@anthropic-ai/claude-code-sample')`
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{"eval shell", []string{"-e", code}, true},
		{"eval argument", []string{"--eval", code}, true},
		{"eval equals", []string{"--eval=" + code}, true},
		{"short eval equals", []string{"-e=" + code}, true},
		{"print argument", []string{"-p", code}, true},
		{"long print argument", []string{"--print", code}, true},
		{"print equals", []string{"--print=" + code}, true},
		{"short print equals", []string{"-p=" + code}, true},
		{"combined print and eval", []string{"-pe", code}, true},
		{"print followed by eval", []string{"--print", "--eval=" + code}, true},
		{"eval after a preload", []string{"--require", "C:/work/preload.js", "-e", code}, true},
		{"eval after an unlabelled Node option value", []string{"--dns-result-order", "ipv4first", "-e", code}, true},
		{"eval with a bare package specifier", []string{"-e", require}, true},
		{"bare package eval after an unlabelled option value", []string{"--dns-result-order", "ipv4first", "-e", require}, true},
		{"preload after an unlabelled option value", []string{"--dns-result-order", "ipv4first", "--require", module, "server.js"}, true},
		{"entry after end of options", []string{"--", module + "/cli.js"}, true},
		{"different npm package", []string{"--eval=" + sample}, false},
		{"program arguments", []string{"server.js", "--eval", code}, false},
		{"program preload argument", []string{"server.js", "--require", module}, false},
		{"program after an unlabelled Node option value", []string{"--dns-result-order", "ipv4first", "server.js", "--eval", code}, false},
		{"program after a valueless Node option", []string{"--no-warnings", "server.js", "--eval", code}, false},
		{"after end of options", []string{"--", "server.js", "-e", code}, false},
		{"eval arguments after end of options", []string{"-e", "console.log(process.argv)", "--", module}, false},
		{"ordinary description", []string{"server.js", "--log", "node -e " + code}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := windows.ComposeCommandLine(append([]string{"node.exe"}, test.args...))
			if got, err := claudeCodeCommandLine(line); err != nil || got != test.want {
				t.Fatalf("running = %t, want %t, parse error %v", got, test.want, err)
			}
		})
	}
}

func TestSameClaudeCLIInstallUsesNativeAlias(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "installed")
	program := claudeCLIFixture(t, installed, "2.1.321", "latest")
	alias := filepath.Join(root, "alias")
	if err := placetest.Junction(alias, installed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil {
			t.Error(err)
		}
	})
	saved := filepath.Join(alias, "Claude-Data", claudeProductFamily, "claude-code", "2.1.309", "old", "claude.exe")
	if !SameClaudeCLIInstall(saved, program) {
		t.Fatal("the same install did not match through its junction")
	}
}

// The inventory reads this session's processes without failing.
func TestClaudeRunningReadsTheInventory(t *testing.T) {
	if _, err := ClaudeRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
}
