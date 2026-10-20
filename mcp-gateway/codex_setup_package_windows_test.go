package main

import (
	"context"
	"os"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
)

func TestCodexSetupRegisteredPackageInertInventory(t *testing.T) {
	if os.Getenv("OA_TEST_CODEX_REGISTERED_PACKAGE") != "1" {
		t.Skip("requires explicitly selected current-user Codex package metadata")
	}
	ctx := context.Background()
	image, err := packaged.OpenImage(ctx, "OpenAI.Codex_2p2nqsd0c76g0", "app/resources/codex.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	options, self, home, vendor := codexFixture(t)
	options.cli = image.Program()
	options.action = "connections"
	calls := 0
	page, err := applyCodexSetup(ctx, options, self, home, func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
		calls++
		if program != image.Program() {
			t.Fatal("not shared owner-selected image")
		}
		if !(len(args) == 1 && args[0] == "--version") && !(len(args) == 4 && args[0] == "mcp" && args[1] == "get" && args[3] == "--json") {
			t.Fatal("inventory attempted mutation", args)
		}
		return vendor.run(ctx, program, directory, args)
	})
	if err != nil || page.Registration != "absent" || vendor.writes != 0 || calls != 2 {
		t.Fatal(page, err, calls, vendor.writes)
	}
}
