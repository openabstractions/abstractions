package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// This source-only adapter supplies explicit fixture server trust and an inert
// CLI. Production always uses installed discovery and the C1 native selector.
func agentStartOwnedFixtureMain() bool {
	raw := os.Getenv("OA_RECEIVER_START_OWNED_FIXTURE")
	if raw == "" || len(os.Args) < 2 {
		return false
	}
	var cfg struct {
		Endpoint, Vendor, VendorConfig, Context, ProbeMarker, ArgvMarker string
		Server                                                           listen.ServerExpectation
		Cold                                                             bool
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		os.Exit(4)
	}
	if cfg.Cold && (os.Args[1] == "--version" || os.Args[1] == "--help" || os.Args[1] == "auth" || os.Args[1] == "-p") {
		for _, name := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CODE_HOST_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_AGENT_SDK_FIXTURE", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID", "CLAUDE_CODE_USER_EMAIL", "CLAUDE_CODE_SUBSCRIPTION_TYPE", "CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_CODE_OAUTH_SCOPES"} {
			if _, inherited := os.LookupEnv(name); inherited {
				os.Exit(4)
			}
		}
	}
	switch os.Args[1] {
	case "start":
		receiverStartMachine = func() *client.Machine { return client.NewVerified(cfg.Endpoint, cfg.Server) }
		receiverStartCLISelector = func(ctx context.Context, p startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
			program, err := os.Executable()
			if err != nil {
				return nil, err
			}
			if _, err := probe(ctx, program); err != nil {
				return nil, err
			}
			version := ""
			if cfg.Cold {
				version = "2.1.317"
			}
			return &fixtureAgentStartCLI{program: program, version: version}, nil
		}
		os.Exit(ownedReceiverStartCommand(os.Args[1:], os.Stdin, os.Stdout, receiverStartConfirmation))
		return true
	case "--version":
		if cfg.ProbeMarker != "" {
			if os.WriteFile(cfg.ProbeMarker, []byte("inert-version-probe"), 0600) != nil {
				os.Exit(4)
			}
		}
		fmt.Fprintln(os.Stdout, "2.1.300 (Claude Code)")
		return true
	case "--help":
		fmt.Fprintln(os.Stdout, "--permission-mode --permission-prompts --restricted --strict-mcp-config --mcp-config --max-budget-usd --output-format --tools --allowedTools --model --disable-slash-commands --settings")
		return true
	case "auth":
		if len(os.Args) != 3 || os.Args[2] != "status" {
			os.Exit(4)
		}
		var vendor struct{ GatewayConfig string }
		var native struct{ State string }
		if json.Unmarshal([]byte(cfg.VendorConfig), &vendor) != nil || json.Unmarshal([]byte(vendor.GatewayConfig), &native) != nil {
			os.Exit(4)
		}
		raw, _ := json.Marshal(map[string]any{"authMethod": "claude.ai", "configDirectory": native.State})
		fmt.Fprintln(os.Stdout, string(raw))
		return true
	case "-p":
		if len(os.Args) < 3 || os.Args[2] != agentStartPrompt(cfg.Context) {
			os.Exit(4)
		}
		if cfg.Cold {
			if !fixtureColdStartRestrictions(os.Args[3:]) {
				os.Exit(4)
			}
		} else if strings.HasPrefix(cfg.Context, agentAttachPrefix) {
			// A copy of the person's session keeps its own access.
			if !fixtureAgentAttachAccess(os.Args[3:], "default") {
				os.Exit(5)
			}
		} else if !fixtureAgentStartRestrictions(os.Args[3:]) {
			os.Exit(4)
		}
		var vendor struct{ Gateway, GatewayConfig, AllowedTools, Release string }
		var native struct{ State, Context string }
		if json.Unmarshal([]byte(cfg.VendorConfig), &vendor) != nil || json.Unmarshal([]byte(vendor.GatewayConfig), &native) != nil {
			os.Exit(4)
		}
		var mcpConfig struct {
			Servers map[string]struct {
				Command string
				Args    []string
			} `json:"mcpServers"`
		}
		found := false
		for i, arg := range os.Args {
			if arg == "--allowedTools" && i+1 < len(os.Args) {
				vendor.AllowedTools = os.Args[i+1]
			}
			if arg == "--mcp-config" && i+1 < len(os.Args) {
				if json.Unmarshal([]byte(os.Args[i+1]), &mcpConfig) != nil {
					os.Exit(4)
				}
				found = true
			}
		}
		server, ok := mcpConfig.Servers["OpenAbstractions"]
		// The launcher names the gateway it runs as; the parent named the
		// helper it built. Both exist, and the filesystem says whether they
		// are one file, whatever spelling each path took.
		sameGateway, _ := fileid.SameFile(server.Command, vendor.Gateway)
		if !found || !ok || len(mcpConfig.Servers) != 1 || !sameGateway || !cfg.Cold && !reflect.DeepEqual(server.Args, []string{"--state-dir", native.State, "--inbox-context", native.Context, "--messages-only"}) {
			os.Exit(4)
		}
		var childConfig map[string]any
		if json.Unmarshal([]byte(vendor.GatewayConfig), &childConfig) != nil {
			os.Exit(4)
		}
		childConfig["MessagesOnly"] = !cfg.Cold
		if cfg.Cold {
			for i, arg := range server.Args {
				if i+1 >= len(server.Args) {
					continue
				}
				switch arg {
				case "--claude-resume-binding":
					childConfig["ColdBinding"] = server.Args[i+1]
				case "--claude-resume-cwd-key":
					childConfig["ColdCwdKey"] = server.Args[i+1]
				case "--inbox-context":
					childConfig["ColdContext"] = server.Args[i+1]
				}
			}
		}
		childRaw, err := json.Marshal(childConfig)
		if err != nil {
			os.Exit(4)
		}
		vendor.GatewayConfig = string(childRaw)
		command := exec.Command(cfg.Vendor, "-test.run=^TestMessageStartVendorChild$", "-test.count=1")
		tools := []string{"oa_messages_prepare", "oa_messages_reply"}
		if cfg.Cold {
			tools = []string{"oa_messages_wait", "oa_messages_reply"}
		}
		for _, tool := range tools {
			if !strings.Contains(","+vendor.AllowedTools+",", ",mcp__OpenAbstractions__"+tool+",") {
				os.Exit(4)
			}
		}
		vendorConfig, err := json.Marshal(vendor)
		if err != nil {
			os.Exit(4)
		}
		command.Env = append(os.Environ(), "OA_RECEIVER_START_VENDOR_FIXTURE="+string(vendorConfig))
		if cfg.Cold {
			id, valid := fixtureAgentSessionFlag(os.Args[3:])
			if !valid {
				os.Exit(4)
			}
			command.Env = append(command.Env, "CLAUDE_CODE_SESSION_ID="+id)
		}
		if output, err := command.CombinedOutput(); err != nil {
			_ = os.WriteFile(cfg.ProbeMarker+"-child-error", output, 0600)
			fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"error_during_execution","is_error":true}`)
			os.Exit(1)
		}
		id, valid := fixtureAgentSessionFlag(os.Args[3:])
		if !valid {
			os.Exit(4)
		}
		// The composed runtime test reads the session flags the launcher chose.
		if cfg.ArgvMarker != "" {
			var flags []string
			for i, arg := range os.Args[3:] {
				switch arg {
				case "--resume", "--session-id":
					flags = append(flags, arg, os.Args[4+i])
				case "--fork-session":
					flags = append(flags, arg)
				}
			}
			raw, _ := json.Marshal(flags)
			if os.WriteFile(cfg.ArgvMarker, raw, 0600) != nil {
				os.Exit(4)
			}
		}
		raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": id, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
		fmt.Fprintln(os.Stdout, string(raw))
		return true
	}
	return false
}

func fixtureColdStartRestrictions(args []string) bool {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--restricted", "--strict-mcp-config", "--disable-slash-commands":
			continue
		}
		if i+1 >= len(args) {
			return false
		}
		values[args[i]] = args[i+1]
		i++
	}
	return values["--permission-mode"] == "plan" && values["--tools"] == "" && values["--allowedTools"] == "mcp__OpenAbstractions__oa_messages_wait,mcp__OpenAbstractions__oa_messages_reply" && values["--resume"] != "" && values["--model"] == "" && values["--fork-session"] == "" && values["--session-id"] == ""
}
