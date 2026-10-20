package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"time"
)

// The installed producer records desired facts before Keep. The selected
// helper is opened for identity and never executed by this operation.
func declareCompanionsCommand(args []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("declare-companions", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	program := flags.String("helper-program", "", "absolute helper, opened only for identity")
	key := flags.String("helper-key", "", "held helper FileKey")
	host := flags.String("host", "", "claude or codex")
	requirement := flags.String("member", "", "explicit required or none")
	state := flags.String("state-dir", "", "existing private connection state")
	cli := flags.String("cli", "", "selected vendor CLI; not executed")
	project := flags.String("project", "", "explicit Claude project, or empty for account scope")
	name := flags.String("name", "OpenAbstractions", "existing connection name")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*program) || *key == "" || !filepath.IsAbs(*state) || !filepath.IsAbs(*cli) || !claudeSetupName(*name) || (*requirement != "required" && *requirement != "none") {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	image, _, err := setupIdentity(ctx, *program, false)
	if err != nil {
		return 1
	}
	if image.FileID().Key() != *key {
		_ = image.Close()
		return 1
	}
	defer image.Close()
	if *host == "claude" {
		err = applyClaudeSetup(ctx, claudeSetupOptions{action: "declare", project: *project, state: *state, cli: *cli, name: *name, companionRequirement: *requirement}, *program, nil, io.Discard)
	} else if *host == "codex" && *project == "" {
		home, e := codexConfigurationHome()
		if e != nil {
			return 1
		}
		_, err = applyCodexMCPSetup(ctx, codexSetupOptions{action: "declare", state: *state, cli: *cli, name: *name, companionRequirement: *requirement}, *program, home, nil)
	} else {
		return 2
	}
	if err != nil {
		return 1
	}
	if json.NewEncoder(out).Encode(map[string]string{"outcome": "declared"}) != nil {
		return 1
	}
	return 0
}
