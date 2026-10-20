package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestHelpInventoriesToolsAndAuthorityBoundary(t *testing.T) {
	var output bytes.Buffer
	flags, _, _, _ := commandFlags(&output, `C:\fixture\state`)
	if err := flags.Parse([]string{"--help"}); err != flag.ErrHelp {
		t.Fatalf("parse help: %v", err)
	}
	help := output.String()
	for _, want := range []string{
		"oa_applications_list",
		"oa_applications_describe",
		"oa_applications_enrollment_read",
		"oa_applications_commands_list",
		"oa_applications_command_call",
		"oa_models_list",
		"oa_models_connect",
		"oa_asks_request_answering",
		"oa_asks_list_answerable",
		"oa_asks_list_choices",
		"oa_asks_answer",
		"oa_asks_answer_choice",
		"oa_setup_fit",
		"oa_logs_follow",
		"oa_setup_prepare",
		"oa_inference_complete",
		"oa_inference_requests",
		"oa_image_generate",
		"oa_video_generate",
		"oa_inference_job_submit",
		"oa_inference_job_status",
		"oa_inference_job_cancel",
		"oa_inference_job_diagnose",
		"oa_jobs_follow",
		"MCP clientInfo, request metadata and parent process IDs never grant",
		"same executable share that configured",
		"Application discovery and models are read-only",
		"When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now.",
		"oa_messages_prepare checks reach or permission first and sends nothing; its outcome gives the next step.",
		"--state-dir",
		"--check",
		"MCP connection separately from OA read availability",
		"does not activate",
		"connect claude --state-dir ABS [--claude ABS_EXE] [--project ABS]",
		"disconnect claude --state-dir ABS [--claude ABS_EXE] [--project ABS]",
		"start --profile NAME --context NAME",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}

func TestClaudeSetupHelpIsReadOnly(t *testing.T) {
	var output bytes.Buffer
	if exit := claudeSetupCommand([]string{"connect", "claude", "--help"}, &output, &output); exit != 0 {
		t.Fatal(exit)
	}
	for _, want := range []string{"project", "state-dir", "claude", "name"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal(output.String())
		}
	}
}

func TestMessagesOnlyOptionsNarrowNativeReceiver(t *testing.T) {
	var output bytes.Buffer
	flags, _, check, only := commandFlags(&output, "state")
	if err := flags.Parse([]string{"--messages-only"}); err != nil || !*only {
		t.Fatal(err)
	}
	if validateMessagesReceiverOptions(*only, *check, "") == nil || validateMessagesReceiverOptions(true, true, "context") == nil {
		t.Fatal("misused receiver switch accepted")
	}
	if err := validateMessagesReceiverOptions(true, false, "context"); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--messages-only=untrue", "--messages-onl"} {
		flags, _, _, _ := commandFlags(&output, "state")
		if flags.Parse([]string{arg}) == nil {
			t.Fatal("invalid switch accepted", arg)
		}
	}
}
