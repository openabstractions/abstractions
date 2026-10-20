package setup

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestProviderReplyLimit(t *testing.T) {
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == "provider-output-fixture" {
		var stream io.Writer = os.Stdout
		if os.Args[len(os.Args)-1] == "stderr" {
			stream = os.Stderr
		}
		if _, err := io.WriteString(stream, strings.Repeat("x", maxProviderReply+1)); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			var stdout, stderr cappedReply
			cmd := exec.CommandContext(t.Context(), program, "-test.run=^TestProviderReplyLimit$", "--", "provider-output-fixture", stream)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err == nil || !strings.Contains(err.Error(), "output exceeded its limit") {
				t.Fatalf("expected an output limit error, got %v", err)
			}
			if len(stdout.Bytes()) > maxProviderReply || len(stderr.Bytes()) > maxProviderReply {
				t.Fatal("provider output was retained beyond its limit")
			}
		})
	}
}
