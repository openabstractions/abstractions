package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// codexConfigVendor stands in for the Codex CLI with config.toml as its store:
// add appends the server table the way codex mcp add does, and get reports
// tool_timeout_sec when that table carries it.
type codexConfigVendor struct {
	*codexVendorFixture
	home string
}

func (v *codexConfigVendor) config() string { return filepath.Join(v.home, "config.toml") }

func (v *codexConfigVendor) run(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
	result, err := v.codexVendorFixture.run(ctx, program, directory, args)
	if err != nil || len(args) < 3 || args[0] != "mcp" {
		return result, err
	}
	raw, _ := os.ReadFile(v.config())
	header := "[mcp_servers." + args[2] + "]"
	switch args[1] {
	case "add":
		section := "\r\n" + header + "\r\ncommand = '" + args[4] + "'\r\nargs = ['" + strings.Join(args[5:], "', '") + "']\r\n"
		if err := os.WriteFile(v.config(), append(raw, section...), 0600); err != nil {
			return result, err
		}
	case "get":
		if v.entry != nil {
			at := bytes.LastIndex(raw, []byte(header))
			body := raw[at+len(header):]
			if end := bytes.Index(body, []byte("\n[")); end >= 0 {
				body = body[:end]
			}
			entry := *v.entry
			entry.ToolTimeout = nil
			if bytes.Contains(body, []byte("\ntool_timeout_sec = 1800")) {
				timeout := float64(1800)
				entry.ToolTimeout = &timeout
			}
			result.Output, _ = json.Marshal(entry)
		}
	}
	return result, nil
}

const codexPersonConfig = "# my settings\r\nmodel = \"gpt-5\"\r\ninstructions = \"\"\"\r\n[mcp_servers.openabstractions]\r\nnot a header\r\n\"\"\"\r\n\r\n[mcp_servers.other]\r\ncommand = 'other.exe' # keep\r\n\r\n[profiles.fast]\r\nmodel = 'small'\r\n"

func codexConfigFixture(t *testing.T, person string) (codexSetupOptions, string, string, *codexConfigVendor, *[]string) {
	t.Helper()
	options, self, home, vendor := codexFixture(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(person), 0600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	previous := codexSetupLog
	codexSetupLog = func(line string) { logged = append(logged, line) }
	t.Cleanup(func() { codexSetupLog = previous })
	return options, self, home, &codexConfigVendor{codexVendorFixture: vendor, home: home}, &logged
}

func TestCodexConnectSetsToolTimeoutAndKeepsPersonConfig(t *testing.T) {
	options, self, home, vendor, logged := codexConfigFixture(t, codexPersonConfig)
	ctx := context.Background()
	page, err := applyCodexSetup(ctx, options, self, home, vendor.run)
	if err != nil || page.Registration != "saved" || !page.Owned || vendor.writes != 1 {
		t.Fatal(page, err, vendor.writes)
	}
	want := codexPersonConfig + "\r\n[mcp_servers.openabstractions]\r\ncommand = '" + self + "'\r\nargs = ['--state-dir', '" + options.state + "']\r\ntool_timeout_sec = 1800\r\n"
	if got, _ := os.ReadFile(vendor.config()); string(got) != want {
		t.Fatalf("config.toml\n%q\nwant\n%q", got, want)
	}
	if vendor.entry == nil || len(*logged) != 0 {
		t.Fatal("connect logged", *logged)
	}
	page, err = applyCodexSetup(ctx, options, self, home, vendor.run)
	if got, _ := os.ReadFile(vendor.config()); err != nil || page.Registration != "saved" || vendor.writes != 1 || string(got) != want {
		t.Fatal("second connect", page, err, vendor.writes, string(got))
	}
	options.action = "connections"
	if page, err := applyCodexSetup(ctx, options, self, home, vendor.run); err != nil || page.Registration != "saved" || !page.Owned {
		t.Fatal("connections", page, err)
	}
	options.action = "disconnect"
	if _, err := applyCodexSetup(ctx, options, self, home, vendor.run); err != nil || vendor.entry != nil {
		t.Fatal("disconnect", err)
	}
}

// An entry OA saved before it set the timeout is owned as it stands; Connect
// adds the timeout without re-adding the server and records the readback.
func TestCodexConnectAddsToolTimeoutToEarlierEntry(t *testing.T) {
	options, self, home, vendor, _ := codexConfigFixture(t, codexPersonConfig)
	ctx := context.Background()
	if _, err := applyCodexSetup(ctx, options, self, home, vendor.run); err != nil {
		t.Fatal(err)
	}
	withTimeout, _ := os.ReadFile(vendor.config())
	earlier := bytes.Replace(withTimeout, []byte("tool_timeout_sec = 1800\r\n"), nil, 1)
	if err := os.WriteFile(vendor.config(), earlier, 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := fileid.Resolve(ctx, fileid.Self, home, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(scope.Key() + "\x00" + options.name))
	markerPath := filepath.Join(options.state, "codex-setup-"+hex.EncodeToString(sum[:])+".json")
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := decodeCodexSetupMarker(raw)
	if err != nil || marker.Entry.ToolTimeout == nil {
		t.Fatal("marker records the timeout", err)
	}
	marker.Entry.ToolTimeout = nil
	raw, _ = json.Marshal(marker)
	if err := os.WriteFile(markerPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	page, err := applyCodexSetup(ctx, options, self, home, vendor.run)
	if got, _ := os.ReadFile(vendor.config()); err != nil || page.Registration != "saved" || vendor.writes != 1 || !bytes.Equal(got, withTimeout) {
		t.Fatal("upgrade connect", page, err, vendor.writes, string(got))
	}
	raw, _ = os.ReadFile(markerPath)
	if marker, err = decodeCodexSetupMarker(raw); err != nil || marker.Entry.ToolTimeout == nil || *marker.Entry.ToolTimeout != 1800 {
		t.Fatal("marker not updated", err)
	}
}

// A config.toml OA cannot read leaves Connect working on Codex's default.
func TestCodexConnectLeavesUnreadableConfig(t *testing.T) {
	person := "[profiles.\"a\\\"b\"]\r\nmodel = 'x'\r\n"
	options, self, home, vendor, logged := codexConfigFixture(t, person)
	page, err := applyCodexSetup(context.Background(), options, self, home, vendor.run)
	if err != nil || page.Registration != "saved" || !page.Owned {
		t.Fatal(page, err)
	}
	got, _ := os.ReadFile(vendor.config())
	if bytes.Contains(got, []byte("tool_timeout_sec")) || !strings.HasPrefix(string(got), person) || len(*logged) != 1 {
		t.Fatal(string(got), *logged)
	}
}

func TestCodexConfigWithToolTimeoutShapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"lf no trailing newline", "[mcp_servers.OA]\ncommand = 'x'", "[mcp_servers.OA]\ncommand = 'x'\ntool_timeout_sec = 1800"},
		{"quoted header and env table", "[mcp_servers.\"OA\"] # mine\nargs = [\n  'a', # one\n  ['b'],\n]\n\n[mcp_servers.OA.env]\nK = 'v'\n", "[mcp_servers.\"OA\"] # mine\nargs = [\n  'a', # one\n  ['b'],\n]\ntool_timeout_sec = 1800\n\n[mcp_servers.OA.env]\nK = 'v'\n"},
		{"bom and empty table", "\xef\xbb\xbf[mcp_servers.OA]\n[x]\n", "\xef\xbb\xbf[mcp_servers.OA]\ntool_timeout_sec = 1800\n[x]\n"},
		{"already set", "[mcp_servers.OA]\ntool_timeout_sec = 1800 # oa\n", "[mcp_servers.OA]\ntool_timeout_sec = 1800 # oa\n"},
	}
	for _, c := range cases {
		got, _, err := codexConfigWithToolTimeout([]byte(c.in), "OA")
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: %q %v", c.name, got, err)
		}
	}
	for _, bad := range []string{
		"[mcp_servers.OA]\ntool_timeout_sec = 600\n",
		"mcp_servers = { OA = { command = 'x' } }\n",
		"[mcp_servers.OA]\nx = \"\"\"\nopen\n",
		"[mcp_servers.OA]\n[mcp_servers.OA]\n",
		"[mcp_servers.OA\n",
	} {
		if _, _, err := codexConfigWithToolTimeout([]byte(bad), "OA"); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
