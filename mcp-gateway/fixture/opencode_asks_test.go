package fixture

import (
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

func TestOpenCodeQuestionFixtureSelectsUniqueMCPTool(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"question-tool.txt": "oa_asks_request_answering", "question-call.json": "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	chat := openCodeFixtureChat{dir: dir, label: "question"}
	name := "OpenAbstractions_oa_asks_request_answering"
	request := wire.Request{Tools: []wire.Tool{{Name: name, Parameters: "{}"}}}
	admission, err := chat.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	page, err := chat.Observe(admission.Operation, 0, 0, 0, 0)
	if err != nil || len(page.Deltas) == 0 || page.Deltas[0].Part == nil || page.Deltas[0].Part.Name != name {
		t.Fatal(page, err)
	}
	request.Tools = append(request.Tools, wire.Tool{Name: "oa_asks_request_answering", Parameters: "{}"})
	if _, err := chat.Start(request); err == nil {
		t.Fatal("duplicate plugin/MCP Asks tool was selected")
	}
}
