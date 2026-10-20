package startprofile

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

// ColdResume is selected by Messages from its current retained chat binding.
// It travels only on the existing admitted owned-start channel, grants no
// authority, and is never accepted as an ordinary tool argument.
type ColdResume struct{ Context, Binding, CwdKey, Revision string }

const ColdResumeMetaKey = "oa_receiver_cold"

func (c *ColdResume) Launch() (*msg.ChatLaunch, error) {
	bad := func() (*msg.ChatLaunch, error) { return nil, errors.New("owned cold resume correlation unavailable") }
	if c == nil || !label.MatchString(c.Context) || c.CwdKey == "" || len(c.CwdKey) > 4096 || len(c.Binding) > 8192 {
		return bad()
	}
	digest, err := hex.DecodeString(c.Revision)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != c.Revision {
		return bad()
	}
	claim, err := msg.DecodeChatBinding([]byte(c.Binding))
	if err != nil || claim.Launch == nil || claim.ChatID != claim.Launch.Session || claim.SourceInstance != "" || claim.ChatTitle != "" {
		return bad()
	}
	launch := claim.Launch
	if !filepath.IsAbs(launch.Cwd) || identity.LocalPath(launch.Cwd) != nil {
		return bad()
	}
	switch launch.Origin {
	case msg.ChatOriginDesktop, msg.ChatOriginCli:
	default:
		return bad()
	}
	switch launch.Mode {
	case msg.ChatModePlan, msg.ChatModeReadOnly, msg.ChatModeDefault, msg.ChatModeAcceptEdits:
	default:
		return bad()
	}
	return launch, nil
}

// codexThread is the form of a Codex thread ID the Codex queue accepts.
var codexThread = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Thread returns the Codex thread a retained Codex chat names. Codex supplies
// no launch facts, so the selection is the thread ID alone: no launch and no
// folder key. The runtime only queues a turn into that existing thread.
func (c *ColdResume) Thread() (string, error) {
	bad := func() (string, error) { return "", errors.New("owned cold thread selection unavailable") }
	if c == nil || !label.MatchString(c.Context) || c.CwdKey != "" || len(c.Binding) > 8192 {
		return bad()
	}
	digest, err := hex.DecodeString(c.Revision)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != c.Revision {
		return bad()
	}
	claim, err := msg.DecodeChatBinding([]byte(c.Binding))
	if err != nil || claim.Launch != nil || claim.SourceInstance != "" || claim.ChatTitle != "" || !codexThread.MatchString(claim.ChatID) {
		return bad()
	}
	return claim.ChatID, nil
}

// Valid accepts either selection form: a Claude launch or a Codex thread.
func (c *ColdResume) Valid() error {
	if _, err := c.Launch(); err == nil {
		return nil
	}
	_, err := c.Thread()
	return err
}

func (c *ColdResume) Metadata() map[string]any {
	return map[string]any{"context": c.Context, "binding": c.Binding, "cwd_key": c.CwdKey, "revision": c.Revision}
}
func ParseColdResumeMetadata(meta map[string]any) (*ColdResume, error) {
	raw, present := meta[ColdResumeMetaKey]
	if !present {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok || len(fields) != 4 {
		return nil, errors.New("owned cold resume metadata invalid")
	}
	c := new(ColdResume)
	for name, out := range map[string]*string{"context": &c.Context, "binding": &c.Binding, "cwd_key": &c.CwdKey, "revision": &c.Revision} {
		v, ok := fields[name].(string)
		if !ok {
			return nil, errors.New("owned cold resume metadata invalid")
		}
		*out = v
	}
	if err := c.Valid(); err != nil {
		return nil, err
	}
	return c, nil
}
