package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

const agentSessionLimit = 16 << 10

var agentSessionUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type agentSessionError struct{ reason string }

func (e *agentSessionError) Error() string { return e.reason }
func sessionRefusal(reason string) error   { return &agentSessionError{reason} }

// One bounded C1 custody receipt per recipe context. It stores no vendor
// history, model output or credentials. Pending is durable before invocation.
type agentSessionRecord struct {
	Schema        string                      `json:"schema"`
	Binding       startprofile.CustodyBinding `json:"binding"`
	Context       string                      `json:"context"`
	Configuration string                      `json:"configuration"`
	Session       string                      `json:"session"`
	State         string                      `json:"state"`
	// Superseded keeps the forks a later fork replaced, newest last. Their
	// transcripts stay in the vendor's history, so discovery must still treat
	// them as OA's and never offer them for attach.
	Superseded []string `json:"superseded,omitempty"`
}

// agentSupersededLimit bounds the retained IDs within the record's 16 KiB.
// Past it the oldest is dropped; a fork is made at most once per receiver
// start, and starts are limited to one per receiver per hour.
const agentSupersededLimit = 128

type agentSessionLease struct {
	path      string
	record    agentSessionRecord
	resume    bool
	confirmed bool
}

func decodeAgentSession(raw []byte) (agentSessionRecord, error) {
	var record agentSessionRecord
	fields, err := claudeConfigObject(raw)
	if err != nil || len(fields) != 6 && (len(fields) != 7 || fields["superseded"] == nil) {
		return record, sessionRefusal("session_custody_unavailable")
	}
	for _, name := range []string{"schema", "binding", "context", "configuration", "session", "state"} {
		if _, ok := fields[name]; !ok {
			return record, sessionRefusal("session_custody_unavailable")
		}
	}
	bound, err := claudeConfigObject(fields["binding"])
	if err != nil || len(bound) != 4 {
		return record, sessionRefusal("session_custody_unavailable")
	}
	if json.Unmarshal(raw, &record) != nil || record.Schema != "oa.agent.session@1" || !record.Binding.Valid() || !agentRoutingContext.MatchString(record.Context) || record.Configuration == "" || len(record.Configuration) > 4096 || !agentSessionUUID.MatchString(record.Session) || (record.State != "creation_pending" && record.State != "created") || len(record.Superseded) > agentSupersededLimit {
		return agentSessionRecord{}, sessionRefusal("session_custody_unavailable")
	}
	for _, old := range record.Superseded {
		if !agentSessionUUID.MatchString(old) {
			return agentSessionRecord{}, sessionRefusal("session_custody_unavailable")
		}
	}
	return record, nil
}

func beginAgentSession(ctx context.Context, state string, binding startprofile.CustodyBinding, contextName, configuration string) (*agentSessionLease, error) {
	return beginAgentSessionMode(ctx, state, binding, contextName, configuration, false)
}

// beginAgentFork holds a fork of a person's session. Its configuration ends in
// "@<source change time>"; when the source changed after the fork was made, a
// created fork is replaced by a new one, so a later question sees the
// person's later work. Any other configuration change still refuses.
func beginAgentFork(ctx context.Context, state string, binding startprofile.CustodyBinding, contextName, configuration string) (*agentSessionLease, error) {
	return beginAgentSessionMode(ctx, state, binding, contextName, configuration, true)
}

func agentForkSource(configuration string) string {
	source, _, _ := strings.Cut(configuration, "@")
	return source
}

func beginAgentSessionMode(ctx context.Context, state string, binding startprofile.CustodyBinding, contextName, configuration string, refork bool) (*agentSessionLease, error) {
	if !filepath.IsAbs(state) || !binding.Valid() || !agentRoutingContext.MatchString(contextName) || configuration == "" || len(configuration) > 4096 {
		return nil, sessionRefusal("session_binding_unavailable")
	}
	key := sha256.Sum256([]byte(contextName))
	lease := &agentSessionLease{path: filepath.Join(state, "claude-session-"+hex.EncodeToString(key[:])+".json")}
	err := cas.ChangeLimitContext(ctx, lease.path, agentSessionLimit, func(raw []byte) ([]byte, error) {
		fresh := func(superseded []string) ([]byte, error) {
			var value [16]byte
			if _, err := rand.Read(value[:]); err != nil {
				return nil, sessionRefusal("session_custody_unavailable")
			}
			value[6] = (value[6] & 15) | 64
			value[8] = (value[8] & 63) | 128
			id := fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
			if len(superseded) > agentSupersededLimit {
				superseded = superseded[len(superseded)-agentSupersededLimit:]
			}
			lease.record = agentSessionRecord{Schema: "oa.agent.session@1", Binding: binding, Context: contextName, Configuration: configuration, Session: id, State: "creation_pending", Superseded: superseded}
			return json.Marshal(lease.record)
		}
		if raw == nil {
			return fresh(nil)
		}
		record, err := decodeAgentSession(raw)
		if err != nil {
			return nil, err
		}
		if record.Binding != binding || record.Context != contextName {
			return nil, sessionRefusal("session_binding_changed")
		}
		if record.Configuration != configuration {
			// A created fork whose source changed is replaced; an uncertain
			// creation or any other change refuses as before.
			if refork && record.State == "created" && agentForkSource(record.Configuration) == agentForkSource(configuration) {
				return fresh(append(append([]string(nil), record.Superseded...), record.Session))
			}
			return nil, sessionRefusal("session_configuration_changed")
		}
		if record.State != "created" {
			return nil, sessionRefusal("session_creation_uncertain")
		}
		lease.record, lease.resume = record, true
		return json.Marshal(lease.record)
	})
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (l *agentSessionLease) arguments() []string {
	flag := "--session-id"
	if l.resume {
		flag = "--resume"
	}
	return []string{flag, l.record.Session}
}

// Only a native envelope at stdout's top level can confirm the requested ID.
// In particular, a model result string containing JSON supplies no session ID.
func nativeAgentSessionEnvelope(raw []byte, requested string) (bool, error) {
	fields, err := claudeConfigObject(raw)
	if err != nil || !agentSessionUUID.MatchString(requested) {
		return false, sessionRefusal("session_result_unconfirmed")
	}
	var kind, subtype, id string
	if json.Unmarshal(fields["type"], &kind) != nil || json.Unmarshal(fields["subtype"], &subtype) != nil || json.Unmarshal(fields["session_id"], &id) != nil {
		return false, sessionRefusal("session_result_unconfirmed")
	}
	if !agentSessionUUID.MatchString(id) || id != requested {
		return false, sessionRefusal("session_result_mismatch")
	}
	if kind == "system" && subtype == "init" {
		return false, nil
	}
	var failed bool
	if kind != "result" || subtype != "success" || json.Unmarshal(fields["is_error"], &failed) != nil || failed {
		return false, sessionRefusal("session_result_unconfirmed")
	}
	return true, nil
}

func (l *agentSessionLease) observe(raw []byte) error {
	confirmed, err := nativeAgentSessionEnvelope(raw, l.record.Session)
	if err != nil {
		return err
	}
	if !confirmed {
		return sessionRefusal("session_result_unconfirmed")
	}
	l.confirmed = true
	return nil
}

// This records historical creation only. The owning lifecycle rechecks held
// program/configuration and recipe facts, without asking a completed question
// for fresh execution admission. Every future execution independently requires
// a fresh native runtime claim and compares its current enrollment binding.
func (l *agentSessionLease) commit(ctx context.Context, current func(context.Context) error) error {
	if !l.confirmed || current == nil {
		return sessionRefusal("session_result_unconfirmed")
	}
	if current(ctx) != nil {
		return sessionRefusal("session_binding_changed")
	}
	return cas.ChangeLimitContext(ctx, l.path, agentSessionLimit, func(raw []byte) ([]byte, error) {
		actual, err := decodeAgentSession(raw)
		if err != nil {
			return nil, err
		}
		if !sameAgentSession(actual, l.record) {
			return nil, sessionRefusal("session_custody_changed")
		}
		if current(ctx) != nil {
			return nil, sessionRefusal("session_binding_changed")
		}
		actual.State = "created"
		return json.Marshal(actual)
	})
}

func sameAgentSession(a, b agentSessionRecord) bool {
	return a.Schema == b.Schema && a.Binding == b.Binding && a.Context == b.Context && a.Configuration == b.Configuration && a.Session == b.Session && a.State == b.State && slices.Equal(a.Superseded, b.Superseded)
}

func agentSessionReason(err error) string {
	var refusal *agentSessionError
	if errors.As(err, &refusal) {
		return refusal.reason
	}
	return ""
}
