package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestHelpInventoriesToolsAndAuthorityBoundary(t *testing.T) {
	var output bytes.Buffer
	flags, _ := commandFlags(&output, `C:\fixture\state`)
	if err := flags.Parse([]string{"--help"}); err != flag.ErrHelp {
		t.Fatalf("parse help: %v", err)
	}
	help := output.String()
	for _, want := range []string{
		"oa_applications_list",
		"oa_models_list",
		"oa_inference_complete",
		"oa_inference_job_submit",
		"oa_inference_job_status",
		"oa_inference_job_cancel",
		"MCP clientInfo, request metadata and parent process IDs never grant",
		"same executable share that configured",
		"Applications and models are read-only",
		"--state-dir",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}
