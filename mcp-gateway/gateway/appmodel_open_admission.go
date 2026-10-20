package gateway

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Only an explicit handle follow is a read. Open with a verb may already
// have started the application when its admission was suspended.
func openFollowAdmissionDeadline(name string, request mcp.Request) time.Time {
	if name != ToolApplicationsOpen {
		return time.Time{}
	}
	params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
	if !ok {
		return time.Time{}
	}
	_, handle, wait, err := openArguments(params.Arguments)
	if err != nil || handle == "" || wait <= 0 {
		return time.Time{}
	}
	return time.Now().Add(wait)
}
