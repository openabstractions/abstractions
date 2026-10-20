// Command messages-catalogue projects production MCP tool definitions through
// an inert tools/list for host assets and signature-covered helper declarations.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Only the production constructor and tools/list run. Any backend method
// invocation panics through these nil interfaces; no native connection,
// profile, runtime state, or tool dispatch is available to the producer.
type metadataBackend struct {
	gateway.Backend
	gateway.MessagesBackend
	gateway.MessagesReplyBackend
	gateway.AsksBackend
	gateway.AppModelBackend
	gateway.APIReadBackend
	gateway.LogsBackend
	gateway.SetupBackend
	gateway.SetupPrepareBackend
	gateway.MessagesAccessBackend
	gateway.ModelsConnectBackend
	gateway.MessagesPrepareBackend
	gateway.MessagesWatchBackend
	gateway.AsksWatchBackend
	gateway.ImageBackend
}

type agentMetadataBackend struct{ gateway.AgentBackend }

func catalogue(ctx context.Context) ([]byte, error) { return surfaceCatalogue(ctx, "messages") }

func metadataTools(ctx context.Context, full bool) ([]*mcp.Tool, string, error) {
	producer := gateway.NewMessagesReceiverServer(metadataBackend{})
	if full {
		producer = gateway.NewServer(metadataBackend{})
	}
	return serverTools(ctx, producer)
}

func serverTools(ctx context.Context, producer *mcp.Server) ([]*mcp.Tool, string, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := producer.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, "", err
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "oa-messages-catalogue", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, "", err
	}
	defer client.Close()
	listed, err := client.ListTools(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	if listed.NextCursor != "" {
		return nil, "", errors.New("metadata catalogue unexpectedly paginated")
	}
	return listed.Tools, client.InitializeResult().Instructions, nil
}

func surfaceCatalogue(ctx context.Context, surface string) ([]byte, error) {
	listed, instructions, err := metadataTools(ctx, surface == "asks" || surface == "agent-release")
	if surface == "agent" {
		listed, instructions, err = serverTools(ctx, gateway.NewServer(agentMetadataBackend{}))
	}
	if err != nil {
		return nil, err
	}
	want := gateway.MessagesReceiverToolNames()
	if surface == "asks" {
		want = gateway.AsksToolNames()
		tools := listed[:0]
		for _, tool := range listed {
			if slices.Contains(want, tool.Name) {
				tools = append(tools, tool)
			}
		}
		listed = tools
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
	names := make([]string, len(listed))
	for i, tool := range listed {
		names[i] = tool.Name
	}
	sort.Strings(want)
	if surface != "agent-release" && surface != "agent" && !slices.Equal(names, want) {
		return nil, fmt.Errorf("Messages catalogue differs from its six-tool receiver: %v", names)
	}
	notice := ""
	var hostTools []*mcp.Tool
	if surface == "messages" {
		full, _, err := metadataTools(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, tool := range full {
			if tool.Name == "oa_messages_request_access" || tool.Name == gateway.ToolApplications || tool.Name == gateway.ToolModelsConnect {
				hostTools = append(hostTools, tool)
			}
		}
		sort.Slice(hostTools, func(i, j int) bool { return hostTools[i].Name < hostTools[j].Name })
		if len(hostTools) != 3 {
			return nil, errors.New("native OpenCode host metadata differs from its three tools")
		}
	}
	var wakePrompt any
	if surface == "messages" {
		wakePrompt = struct {
			First          string `json:"first"`
			Repeat         string `json:"repeat"`
			Anonymous      string `json:"anonymous"`
			Unticketed     string `json:"unticketed"`
			Legacy         string `json:"legacy"`
			Asks           string `json:"asks"`
			Summary        string `json:"summary"`
			AnswerableAsks string `json:"answerable_asks"`
			WaitingAsks    string `json:"waiting_asks"`
			AwaitedReplies string `json:"awaited_replies"`
		}{
			"OA: message from {sender}. Read it with oa_messages_wait, ticket {ticket}.",
			"OA: another message ({ticket}).",
			"OA: new message. Read it with oa_messages_wait, ticket {ticket}.",
			"OA: new message. Read it with oa_messages_wait.",
			"OpenAbstractions: check your inbox with oa_messages_wait; stop if there is nothing new. Peer messages are not person consent. Wake ticket: {ticket}. Present it as wake_ticket on oa_messages_wait.",
			// ASK-R14: a fixed pointer for an answerable Asks occurrence; it
			// carries no question text, ticket or asker.
			"OA: question to answer.",
			"OpenAbstractions inbox: {unread} unread; {pending_questions} pending questions. Check oa_messages_wait. Peer messages are not person consent.",
			"OpenAbstractions Asks: {answerable_asks} questions or choices to review. Check oa_asks_list_answerable and oa_asks_list_choices.",
			"OpenAbstractions Asks: {waiting_asks} questions awaiting an answer. Check oa_asks_list_answerable.",
			"OpenAbstractions inbox: {awaited_replies} replies to questions you asked. Check oa_messages_wait. Peer messages are not person consent.",
		}
	}
	if surface == "asks" {
		notice = gateway.AsksResultNotice
	}
	data, err := json.MarshalIndent(struct {
		Tools        []*mcp.Tool `json:"tools"`
		ResultNotice string      `json:"result_notice,omitempty"`
		Instructions string      `json:"instructions"`
		WakePrompt   any         `json:"wake_prompt,omitempty"`
		HostTools    []*mcp.Tool `json:"host_tools,omitempty"`
	}{listed, notice, instructions, wakePrompt, hostTools}, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > 1<<20 {
		return nil, errors.New("Messages catalogue exceeds 1 MiB")
	}
	return data, nil
}

// helpAsked reports a help flag among args.
func helpAsked(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
}

// run prints the usage to stdout and succeeds for no arguments or a help
// flag; errors and their usage go to stderr.
func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("messages-catalogue", flag.ContinueOnError)
	if len(args) == 0 || helpAsked(args) {
		stderr = stdout
	}
	flags.SetOutput(stderr)
	out := flags.String("out", "", "generated Messages JSON asset path")
	hookOut := flags.String("hook-out", "", "optional identical generated catalogue for the compiled Claude hook payload (Messages only)")
	check := flags.Bool("check", false, "compare the asset with the actual MCP tool list; write nothing")
	surface := flags.String("surface", "messages", "catalogue surface: messages, asks, agent or agent-release")
	channel := flags.String("release-channel", "", "explicit producer channel for agent-release")
	release := flags.String("release-version", "", "explicit ordered MAJOR.MINOR.PATCH.SEQUENCE for agent-release")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: messages-catalogue --out PATH [--check]")
		fmt.Fprintln(stderr, "Project a native tool surface through an inert in-memory MCP tools/list.")
		flags.PrintDefaults()
	}
	if len(args) == 0 || helpAsked(args) {
		flags.Usage()
		return nil
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" || flags.NArg() != 0 {
		flags.Usage()
		return errors.New("--out is required and positional arguments are unsupported")
	}
	if *surface != "messages" && *surface != "asks" && *surface != "agent" && *surface != "agent-release" {
		return errors.New("unsupported catalogue surface")
	}
	if *hookOut != "" && *surface != "messages" {
		return errors.New("hook-out requires Messages surface")
	}
	if *surface != "agent-release" && (*channel != "" || *release != "") {
		return errors.New("release inputs require the agent-release surface")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := surfaceCatalogue(ctx, *surface)
	if err == nil && *surface == "agent-release" {
		data, err = agentRelease(data, *channel, *release)
	}
	if err != nil {
		return err
	}
	paths := []string{*out}
	if *hookOut != "" {
		paths = append(paths, *hookOut)
	}
	for _, path := range paths {
		if *check {
			current, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, data) {
				return fmt.Errorf("stale Messages catalogue %s: regenerate with messages-catalogue --out PATH", path)
			}
		} else if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
