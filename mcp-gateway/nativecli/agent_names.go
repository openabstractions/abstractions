package nativecli

import (
	"encoding/hex"
	"runtime"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// agentApplications are OpenAbstractions' own connector bases in routing order.
var agentApplications = []string{
	"oa-claude-integration",
	"oa-codex-integration",
	"oa-opencode-integration",
}

// AgentApplicationBases lists the connector base names.
func AgentApplicationBases() []string {
	bases := make([]string, len(agentApplications))
	copy(bases, agentApplications)
	return bases
}

// AgentApplicationForParty looks up a connection's fixed helper base in the
// same roster that classifies native routing labels. A party outside that
// roster returns an empty name.
func AgentApplicationForParty(party string) string {
	return AgentApplicationBase("oa-" + party + "-integration")
}

// AgentTitle names the agent app a connector base name connects ("Claude",
// "Codex", "OpenCode"), and reports whether base is a connector base name.
func AgentTitle(base string) (string, bool) {
	for _, app := range agentApplications {
		if app == base {
			party := strings.TrimSuffix(strings.TrimPrefix(base, "oa-"), "-integration")
			return product.AgentConnectionTitle(party), true
		}
	}
	return "", false
}

// AgentApplicationName translates an exact dedicated-helper filename into its
// private native routing label. Enrollment and file identity supply authority.
// On Windows the fixed helper file name is compared by the identity owner's
// name rule; the Darwin labels stay exact.
func AgentApplicationName(leaf string) string {
	return agentApplicationName(leaf, runtime.GOOS)
}

// DarwinAgentApplicationName validates recovery metadata for the immutable
// Darwin transition, including when exercised by an inert cross-host fixture.
func DarwinAgentApplicationName(leaf string) string { return agentApplicationName(leaf, "darwin") }

func agentApplicationName(leaf, platform string) string {
	for _, base := range agentApplications {
		if (platform == "windows" && identity.SamePathName(leaf, base+".exe")) || (platform == "darwin" && leaf == base) {
			return base
		}
		if platform == "darwin" && strings.HasPrefix(leaf, base+"-") {
			digest := strings.TrimPrefix(leaf, base+"-")
			if validAgentDigest(digest, 32) {
				return base + "-" + digest[:32]
			}
		}
	}
	return ""
}

func validAgentDigest(text string, bytes int) bool {
	raw, err := hex.DecodeString(text)
	return err == nil && len(raw) == bytes && hex.EncodeToString(raw) == text
}

// AgentApplicationBase classifies private routing labels. Callers retain the
// exact label for every authorization, inbox and native enrollment operation.
func AgentApplicationBase(application string) string {
	return agentApplicationBase(application, runtime.GOOS)
}

func agentApplicationBase(application, platform string) string {
	for _, base := range agentApplications {
		if application == base {
			return base
		}
		if platform == "darwin" && strings.HasPrefix(application, base+"-") && validAgentDigest(strings.TrimPrefix(application, base+"-"), 16) {
			return base
		}
	}
	return ""
}

// MovedAgentImage reports a program path that is a connector copy Setup moved
// aside when it published an update: "<helper>.exe.<hex>.tmp.previous" or
// ".oa-helper-old-<digits>" in an "agent-integrations" folder. Windows names a
// running helper by its moved file, so an app that kept running the previous
// connector reaches OA under this path until it restarts. Title names the app
// ("Claude", "Codex", "OpenCode") when the file name still says which.
func MovedAgentImage(program string) (title string, moved bool) {
	dir, leaf := splitProgramPath(program)
	if !strings.EqualFold(lastPathElement(dir), "agent-integrations") {
		return "", false
	}
	lower := strings.ToLower(leaf)
	if digits, ok := strings.CutPrefix(lower, ".oa-helper-old-"); ok {
		return "", digits != "" && len(digits) <= 20 && strings.Trim(digits, "0123456789") == ""
	}
	if !strings.HasSuffix(lower, ".previous") {
		return "", false
	}
	for strings.HasSuffix(lower, ".previous") {
		lower = strings.TrimSuffix(lower, ".previous")
	}
	for _, base := range agentApplications {
		digest, ok := strings.CutPrefix(lower, base+".exe.")
		if !ok {
			continue
		}
		digest, ok = strings.CutSuffix(digest, ".tmp")
		if ok && digest != "" && len(digest) <= 128 && strings.Trim(digest, "0123456789abcdef") == "" {
			title, _ := AgentTitle(base)
			return title, true
		}
	}
	return "", false
}

// MovedAgentImageSentence is what OA tells an app still running a connector
// copy Setup moved aside.
func MovedAgentImageSentence(title string) string {
	if title == "" {
		title = "the agent app"
	}
	return "Restart " + title + " to use the updated connector."
}

// splitProgramPath splits at the last separator of either platform, so a
// Windows program path classifies the same on every host.
func splitProgramPath(program string) (string, string) {
	i := strings.LastIndexAny(program, `\/`)
	if i < 0 {
		return "", program
	}
	return program[:i], program[i+1:]
}

func lastPathElement(dir string) string {
	_, leaf := splitProgramPath(strings.TrimRight(dir, `\/`))
	return leaf
}
