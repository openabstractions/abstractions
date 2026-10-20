package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// appModelCallFailure answers a command call whose native call ended
// without an answer: the wait ran out, or the connection to OpenAbstractions
// closed. It answers one plain sentence naming that state, never the
// transport's own error text.
func appModelCallFailure(application, key string, err error) (*mcp.CallToolResult, error) {
	outcome, sentence := "unknown", "OpenAbstractions lost its connection to "+application+" before receiving the command result."+appModelUnconfirmedCall
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		sentence = application + " did not answer within the command's wait." + appModelUnconfirmedCall
	}
	if key != "" {
		sentence += " To check the same receipt, repeat the exact command with the original idempotency_key."
	}
	raw, marshalErr := json.Marshal(map[string]string{"outcome": outcome, "reason": "connection"})
	if marshalErr != nil {
		return nil, marshalErr
	}
	result, resultErr := appModelResult(raw, true)
	return appModelWithSentence(result, resultErr, sentence)
}

const appModelUnconfirmedCall = " The command may have taken effect. Check the application's state before submitting another command."

// appModelCallSentence is the plain sentence for a refused or failed command
// whose reason is OpenAbstractions' own word, or "" when the reason already
// is the application's sentence.
func appModelCallSentence(application, outcome, reason string) string {
	switch {
	case reason == "owned_host":
		return application + " is not open in OpenAbstractions. Call oa_applications_open for " + application + ", then call the command again."
	case strings.HasPrefix(reason, "instance:"):
		return "That " + application + " instance has ended. Call oa_applications_open for " + application + " to get its current instance."
	case reason == "answer:wait_ended":
		return application + " did not answer within the command's wait." + appModelUnconfirmedCall
	case reason == "answer:unavailable" || strings.HasPrefix(reason, "answer:-"):
		return "OpenAbstractions lost its connection to " + application + " before receiving the command result." + appModelUnconfirmedCall
	case reason == "receipt" && outcome == "unknown":
		return "OpenAbstractions has no confirmed result for this command." + appModelUnconfirmedCall
	case reason == "context":
		return application + " has no open context for this command yet; it may still be starting. Call the command again in a few seconds."
	}
	return ""
}

// appModelWithSentence adds sentence for the agent to relay.
func appModelWithSentence(result *mcp.CallToolResult, err error, sentence string) (*mcp.CallToolResult, error) {
	if err != nil || result == nil || sentence == "" {
		return result, err
	}
	result.Content = append(result.Content, &mcp.TextContent{Text: sentence, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}})
	return result, nil
}
