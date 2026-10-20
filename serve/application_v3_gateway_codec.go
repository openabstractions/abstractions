package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/appmodelcall"
	manifestowner "github.com/openabstractions/abstraction-facade/go/manifest"
)

// Decoding this value establishes shape, never caller or instance authority.
type applicationV3Invocation struct {
	context        *appmodel.ContextRef
	class          appmodel.CommandClass
	mode           appmodel.InvokeMode
	key            string
	binding        bool
	invocation     *appmodel.Invocation
	revisionBefore string
	// contextName is the context a command call names, empty for the
	// instance's current context (FAC-M32).
	contextName string
}

// Custom commands retain command uncertainty even when their declared class is read.
func applicationV3ReadingTool(tool string) bool {
	return slices.Contains([]string{"app_describe", "app_contexts", "app_read", "app_changes", "app_reveal", "app_job"}, tool)
}

func applicationV3AnswerWord(err error) string {
	var refusal *appmodel.Refusal
	if errors.As(err, &refusal) {
		return refusal.Word
	}
	if err != nil && err.Error() == "canonical" {
		return "canonical"
	}
	return "malformed"
}

type applicationV3DescriptionError struct{ outcome, reason string }

func (e *applicationV3DescriptionError) Error() string { return e.reason }

// applicationV3DescriptionFailure names why an instance has no kept
// description (FAC-M32 step 7): the outcome and a reason that says what
// happened.
func applicationV3DescriptionFailure(err error) (string, string) {
	var defect *applicationV3DescriptionError
	if errors.As(err, &defect) {
		return defect.outcome, defect.reason
	}
	var invalid *ownedApplicationInvalidAnswer
	if errors.As(err, &invalid) {
		return "invalid", "answer:" + applicationV3AnswerWord(invalid.cause)
	}
	var rpc *ownedApplicationRPCError
	switch {
	case errors.Is(err, errOwnedApplicationVirtualized):
		return "unavailable", "virtualized"
	case errors.Is(err, errOwnedApplicationCatalogue):
		return "unavailable", "catalogue"
	case errors.As(err, &rpc):
		return "unavailable", fmt.Sprintf("answer:%d", rpc.code)
	case errors.Is(err, errOwnedApplicationAbandoned), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "unavailable", "answer:wait_ended"
	case errors.Is(err, errOwnedApplicationUncertain), errors.Is(err, io.ErrClosedPipe):
		return "unavailable", "owned_host"
	}
	return "unavailable", "description"
}

func decodeApplicationV3Invocation(call appmodel.GatewayCall) (applicationV3Invocation, error) {
	var v applicationV3Invocation
	v.binding = slices.Contains(appmodel.BindingTools, call.Tool)
	if v.binding {
		if member := appmodelcall.BindingMember(&call); member != "" {
			return v, errors.New(member)
		}
	}
	var err error
	switch call.Tool {
	case "app_describe", "app_contexts":
		v.class = appmodel.CommandClassRead
		var fields map[string]json.RawMessage
		if err := ownedApplicationJSON([]byte(call.Arguments)); err != nil {
			return v, errors.New("malformed")
		}
		if err := json.Unmarshal([]byte(call.Arguments), &fields); err != nil || fields == nil {
			return v, errors.New("wrong_type")
		}
		if len(fields) != 0 {
			return v, errors.New("unknown_field")
		}
	case "app_read":
		var x *appmodel.ReadRequest
		x, err = appmodel.DecodeReadRequest([]byte(call.Arguments))
		if err == nil {
			v.context = &x.Context
			if x.Limit < 1 || x.Limit > 1024 || len(x.Entities) > 256 {
				err = errors.New("bounds")
			}
		}
		v.class = appmodel.CommandClassRead
	case "app_changes":
		var x *appmodel.ChangesRequest
		x, err = appmodel.DecodeChangesRequest([]byte(call.Arguments))
		if err == nil {
			v.context = &x.Context
			v.revisionBefore = x.AfterRevision
			if x.WaitMs < 0 || x.WaitMs > 30000 {
				err = errors.New("wait_ms")
			}
		}
		v.class = appmodel.CommandClassRead
	case "app_reveal":
		var x *appmodel.Reveal
		x, err = appmodel.DecodeReveal([]byte(call.Arguments))
		if err == nil {
			v.context = &x.Context
			if len(x.Entities) < 1 || len(x.Entities) > 256 {
				err = errors.New("entities")
			}
		}
		v.class = appmodel.CommandClassReveal
	case "app_undo":
		var x *appmodel.UndoRequest
		x, err = appmodel.DecodeUndoRequest([]byte(call.Arguments))
		if err == nil {
			v.context = &x.Context
			v.key = x.IdempotencyKey
			v.revisionBefore = x.ExpectedRevision
			if x.ExpectedRevision == "" || x.UndoGroup == "" || len(x.IdempotencyKey) < 1 || len(x.IdempotencyKey) > 128 {
				err = errors.New("undo")
			}
		}
		v.class = appmodel.CommandClassEdit
	case "app_job":
		var x *appmodel.JobRequest
		x, err = appmodel.DecodeJobRequest([]byte(call.Arguments))
		if err == nil && x.Instance != call.Instance {
			err = errors.New("instance")
		}
		v.class = appmodel.CommandClassRead
	default:
		// arguments is the command's input_schema object; the gateway builds
		// the Invocation once it has read the context (FAC-M32).
		v.mode = appmodel.InvokeModeApply
		if call.Mode != nil {
			v.mode = *call.Mode
		}
		v.key = call.IdempotencyKey
		v.revisionBefore = call.ExpectedRevision
		v.contextName = call.Context
		if reason := appmodelcall.Arguments(&call); reason != "" {
			err = errors.New(reason)
		}
	}
	if err != nil {
		return v, applicationV3MissingField(call.Tool, call.Arguments, err)
	}
	if v.context != nil && v.context.Instance != call.Instance {
		return v, errors.New("instance")
	}
	if v.context != nil && (len(v.context.Instance) < 1 || len(v.context.Instance) > 128 || len(v.context.Context) < 1 || len(v.context.Context) > 64 || len(v.context.Epoch) < 1 || len(v.context.Epoch) > 64) {
		return v, errors.New("context")
	}
	return v, nil
}

func validateApplicationV3Command(v applicationV3Invocation, spec appmodel.CommandSpec) error {
	x := v.invocation
	if x == nil {
		return errors.New("invocation")
	}
	if x.Mode == appmodel.InvokeModeApply {
		if (spec.Class == appmodel.CommandClassEdit || spec.Class == appmodel.CommandClassCommit || spec.Class == appmodel.CommandClassDestroy) && x.ExpectedRevision == "" {
			return errors.New("expected_revision")
		}
		switch spec.Class {
		case appmodel.CommandClassEdit, appmodel.CommandClassCommit, appmodel.CommandClassDestroy, appmodel.CommandClassSpend, appmodel.CommandClassSend:
			if len(x.IdempotencyKey) < 1 || len(x.IdempotencyKey) > 128 {
				return errors.New("idempotency_key")
			}
		}
	}
	if x.Proposal != "" {
		if x.Mode != appmodel.InvokeModeApply || !spec.Proposable || spec.Class == appmodel.CommandClassPropose {
			return errors.New("proposal")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(x.Arguments), &fields) != nil || fields == nil || len(fields) != 0 {
			return errors.New("proposal")
		}
		return nil
	}
	return nil
}

func applicationV3CodecWord(err error) string {
	var refusal *appmodel.Refusal
	if errors.As(err, &refusal) {
		return refusal.Word
	}
	return err.Error()
}

// The application's canonical text is the only reply authority. Decode exactly
// the expected record, then re-encode it to refuse a second, ambiguous view.
func applicationV3Answer(tool string, raw []byte) (*appmodel.Envelope, error) {
	var env appmodel.Envelope
	var value any
	var err error
	switch tool {
	case "app_describe":
		env.Description, err = appmodel.DecodeDescriptionResult(raw)
		value = env.Description
	case "app_contexts":
		env.Contexts, err = appmodel.DecodeContextPage(raw)
		value = env.Contexts
	case "app_read":
		env.Snapshot, err = appmodel.DecodeContextSnapshot(raw)
		value = env.Snapshot
	case "app_changes":
		env.Changes, err = appmodel.DecodeChangeBatch(raw)
		value = env.Changes
	default:
		env.Result, err = appmodel.DecodeCommandResult(raw)
		value = env.Result
	}
	if err != nil {
		return nil, err
	}
	canonical, err := appmodel.ToJSON(value)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, raw) {
		return nil, errors.New("canonical")
	}
	return &env, nil
}

func applicationV3Refusal(tool, outcome, reason string, context *appmodel.ContextRef) *appmodel.Envelope {
	ref := appmodel.ContextRef{}
	if context != nil {
		ref = *context
	}
	reading, knownReading := appmodel.ParseReadingOutcome(outcome)
	if !knownReading {
		reading = appmodel.ReadingOutcomeUnavailable
	}
	command, knownCommand := appmodel.ParseCommandOutcome(outcome)
	if !knownCommand {
		command = appmodel.CommandOutcomeUnknown
	}
	switch tool {
	case "app_describe":
		return &appmodel.Envelope{Description: &appmodel.DescriptionResult{Outcome: reading, Reason: reason}}
	case "app_contexts":
		return &appmodel.Envelope{Contexts: &appmodel.ContextPage{Outcome: reading, Contexts: []appmodel.ContextEntry{}, Reason: reason}}
	case "app_read":
		return &appmodel.Envelope{Snapshot: &appmodel.ContextSnapshot{Outcome: reading, Context: ref, Entities: []appmodel.Entity{}, Collections: []appmodel.Collection{}, Selections: []appmodel.Selection{}, Reason: reason}}
	case "app_changes":
		return &appmodel.Envelope{Changes: &appmodel.ChangeBatch{Outcome: reading, Context: ref, Changes: []appmodel.Change{}, Reason: reason}}
	default:
		return &appmodel.Envelope{Result: &appmodel.CommandResult{Outcome: command, Reason: reason}}
	}
}

func applicationV3SecretAnswer(description *appmodel.Description, env *appmodel.Envelope) error {
	if description == nil {
		return errors.New("description")
	}
	secret := map[string]map[string]bool{}
	first := map[string]string{}
	for _, k := range description.Kinds {
		secret[k.Name] = map[string]bool{}
		for _, f := range k.Fields {
			if f.Role == appmodel.FieldRoleSecret {
				secret[k.Name][f.Name] = true
				if first[f.Name] == "" {
					first[f.Name] = k.Name
				}
			}
		}
	}
	if env.Snapshot != nil {
		for _, e := range env.Snapshot.Entities {
			var properties map[string]json.RawMessage
			if err := json.Unmarshal([]byte(e.Properties), &properties); err != nil {
				return err
			}
			for field := range properties {
				if secret[e.Kind][field] {
					return &applicationV3SecretRefusal{kind: e.Kind, field: field}
				}
			}
		}
	}
	if env.Changes != nil {
		for _, change := range env.Changes.Changes {
			if err := applicationV3SecretPatch(secret, first, []byte(change.Patch)); err != nil {
				return err
			}
		}
	}
	if env.Result != nil && env.Result.Effects != "" {
		var effects any
		if json.Unmarshal([]byte(env.Result.Effects), &effects) != nil {
			return errors.New("effects")
		}
		if err := applicationV3SecretMembers(effects, first, false); err != nil {
			return err
		}
	}
	return nil
}

type applicationV3SecretRefusal struct{ kind, field string }

func (e *applicationV3SecretRefusal) Error() string { return "secret:" + e.kind + "." + e.field }

// Effects match every member; untyped patch values match properties members.
func applicationV3SecretMembers(value any, first map[string]string, propertiesOnly bool) error {
	switch v := value.(type) {
	case []any:
		for _, child := range v {
			if err := applicationV3SecretMembers(child, first, propertiesOnly); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if !propertiesOnly && first[key] != "" {
				return &applicationV3SecretRefusal{kind: first[key], field: key}
			}
			if propertiesOnly && key == "properties" {
				if fields, ok := v[key].(map[string]any); ok {
					for field := range fields {
						if first[field] != "" {
							return &applicationV3SecretRefusal{kind: first[field], field: field}
						}
					}
				}
			}
			if err := applicationV3SecretMembers(v[key], first, propertiesOnly); err != nil {
				return err
			}
		}
	}
	return nil
}

func applicationV3SecretPatch(secret map[string]map[string]bool, first map[string]string, raw []byte) error {
	if err := ownedApplicationJSON(raw); err != nil {
		return err
	}
	var operations []map[string]json.RawMessage
	if json.Unmarshal(raw, &operations) != nil || operations == nil {
		return errors.New("patch")
	}
	anySecret := map[string]bool{}
	for _, fields := range secret {
		for name := range fields {
			anySecret[name] = true
		}
	}
	entity := func(raw json.RawMessage) error {
		var value struct {
			Kind       string                     `json:"kind"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Properties == nil {
			return errors.New("patch entity")
		}
		fields, known := secret[value.Kind]
		if !known {
			return errors.New("patch kind")
		}
		for name := range value.Properties {
			if fields[name] {
				return &applicationV3SecretRefusal{kind: value.Kind, field: name}
			}
		}
		return nil
	}
	entities := func(raw json.RawMessage) error {
		var values map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return errors.New("patch entities")
		}
		for _, value := range values {
			if err := entity(value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, op := range operations {
		var path, from, operation string
		if json.Unmarshal(op["op"], &operation) != nil || json.Unmarshal(op["path"], &path) != nil {
			return errors.New("patch operation")
		}
		_, hasValue := op["value"]
		_, hasFrom := op["from"]
		switch operation {
		case "add", "replace", "test":
			if !hasValue || hasFrom {
				return errors.New("patch operation")
			}
		case "remove":
			if hasValue || hasFrom {
				return errors.New("patch operation")
			}
		case "move", "copy":
			if hasValue || !hasFrom {
				return errors.New("patch operation")
			}
		default:
			return errors.New("patch operation")
		}
		for key := range op {
			if key != "op" && key != "path" && key != "value" && key != "from" {
				return errors.New("patch operation")
			}
		}
		parts, err := applicationV3Pointer(path)
		if err != nil {
			return err
		}
		if len(parts) >= 4 && parts[0] == "entities" && parts[2] == "properties" && first[parts[3]] != "" {
			return &applicationV3SecretRefusal{kind: first[parts[3]], field: parts[3]}
		}
		if source, ok := op["from"]; ok {
			if json.Unmarshal(source, &from) != nil {
				return errors.New("patch from")
			}
			p, err := applicationV3Pointer(from)
			if err != nil {
				return err
			}
			if len(p) >= 4 && p[0] == "entities" && p[2] == "properties" && first[p[3]] != "" {
				return &applicationV3SecretRefusal{kind: first[p[3]], field: p[3]}
			}
		}
		value := op["value"]
		if !hasValue || operation == "remove" {
			continue
		}
		var untyped any
		if json.Unmarshal(value, &untyped) != nil {
			return errors.New("patch value")
		}
		if len(parts) == 0 {
			var root map[string]json.RawMessage
			if json.Unmarshal(value, &root) != nil || root == nil {
				return errors.New("patch root")
			}
			if err := entities(root["entities"]); err != nil {
				return err
			}
			continue
		}
		if parts[0] != "entities" {
			if err := applicationV3SecretMembers(untyped, first, true); err != nil {
				return err
			}
			continue
		}
		if len(parts) == 1 {
			if err := entities(value); err != nil {
				return err
			}
			continue
		}
		if len(parts) == 2 {
			if err := entity(value); err != nil {
				return err
			}
			continue
		}
		if parts[2] != "properties" {
			if err := applicationV3SecretMembers(untyped, first, true); err != nil {
				return err
			}
			continue
		}
		if len(parts) > 3 {
			if err := applicationV3SecretMembers(untyped, first, true); err != nil {
				return err
			}
			continue
		}
		// A leaf properties patch carries no kind. Without a projection cache,
		// ambiguous secret-role names refuse rather than disclosing their value.
		var properties map[string]json.RawMessage
		if json.Unmarshal(value, &properties) != nil || properties == nil {
			return errors.New("patch properties")
		}
		for field := range properties {
			if anySecret[field] {
				return &applicationV3SecretRefusal{kind: first[field], field: field}
			}
		}
	}
	return nil
}

func applicationV3Pointer(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("patch pointer")
	}
	parts := strings.Split(path[1:], "/")
	for i, p := range parts {
		for at := 0; at < len(p); at++ {
			if p[at] == '~' {
				if at+1 == len(p) || (p[at+1] != '0' && p[at+1] != '1') {
					return nil, errors.New("patch pointer")
				}
				at++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func validateApplicationV3Description(d *appmodel.Description) error {
	if !slices.Contains(appmodel.Versions, d.Version) {
		return errors.New("version:" + d.Version)
	}
	if len(appmodel.EncodeDescription(d)) > 262144 || len(d.Kinds) < 1 || len(d.Kinds) > 64 || len(d.Commands) > 128 {
		return errors.New("description")
	}
	names := map[string]bool{}
	for _, k := range d.Kinds {
		if !applicationV3LocalName(k.Name) || names[k.Name] {
			return errors.New("kind:" + k.Name)
		}
		names[k.Name] = true
		if manifestowner.ValidateApplicationSchema([]byte(k.Schema), nil) != nil {
			return errors.New("schema:" + k.Name)
		}
		fields := map[string]bool{}
		for _, f := range k.Fields {
			if f.Name == "" || fields[f.Name] || !f.Role.Known() {
				return errors.New("fields:" + k.Name)
			}
			fields[f.Name] = true
		}
	}
	commands := map[string]bool{}
	for _, c := range d.Commands {
		parts := strings.Split(c.Name, ".")
		if len(c.Name) < 3 || len(c.Name) > 128 || len(parts) != 2 || !applicationV3LocalName(parts[0]) || !applicationV3LocalName(parts[1]) || commands[c.Name] {
			return errors.New("name:" + c.Name)
		}
		commands[c.Name] = true
		if !names[parts[0]] {
			return errors.New("kind:" + c.Name)
		}
		if !c.Class.Known() {
			return errors.New("class:" + c.Name)
		}
		for _, target := range c.Targets {
			if !names[target] {
				return errors.New("targets:" + c.Name)
			}
		}
		if c.Proposable && (!slices.Contains(d.Extensions, "proposal") || c.Class == appmodel.CommandClassRead || c.Class == appmodel.CommandClassReveal || c.Class == appmodel.CommandClassPropose) {
			return errors.New("proposable:" + c.Name)
		}
		if c.Undoable && c.Class != appmodel.CommandClassEdit {
			return errors.New("undoable:" + c.Name)
		}
		if c.LongRunning && (!slices.Contains(d.Extensions, "async") || !(c.Class == appmodel.CommandClassCommit || c.Class == appmodel.CommandClassSpend || c.Class == appmodel.CommandClassSend)) {
			return errors.New("long_running:" + c.Name)
		}
		if len(c.Title) < 1 || len(c.Title) > 256 || len(c.Description) > 4096 {
			return errors.New("title:" + c.Name)
		}
		if manifestowner.ValidateApplicationSchema([]byte(c.InputSchema), nil) != nil || manifestowner.ValidateApplicationSchema([]byte(c.OutputSchema), nil) != nil {
			return errors.New("schema:" + c.Name)
		}
	}
	b := d.Bounds
	if b.SnapshotBytes < 4096 || b.SnapshotBytes > 262144 || b.ChangeBytes < 1024 || b.ChangeBytes > 262144 || b.RetainedRevisions < 16 || b.RetainedRevisions > 65536 || b.PendingProposals < 1 || b.PendingProposals > 64 || b.CallsInFlight < 1 || b.CallsInFlight > 64 {
		return errors.New("bounds")
	}
	return nil
}

func applicationV3LocalName(s string) bool {
	if len(s) < 1 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// applicationV3BindingShapes encode each binding request with its fields at
// their zero values: the keys the record always carries.
var applicationV3BindingShapes = map[string]func() []byte{
	"app_read":    func() []byte { return appmodel.EncodeReadRequest(&appmodel.ReadRequest{}) },
	"app_changes": func() []byte { return appmodel.EncodeChangesRequest(&appmodel.ChangesRequest{}) },
	"app_reveal":  func() []byte { return appmodel.EncodeReveal(&appmodel.Reveal{}) },
	"app_undo":    func() []byte { return appmodel.EncodeUndoRequest(&appmodel.UndoRequest{}) },
	"app_job":     func() []byte { return appmodel.EncodeJobRequest(&appmodel.JobRequest{}) },
}

// applicationV3MissingField names the field a binding request lacks:
// missing_field:<name>, the first of the record's keys, in name order, that
// the arguments do not carry. Any other error is returned as it is.
func applicationV3MissingField(tool, arguments string, err error) error {
	var refusal *appmodel.Refusal
	shape := applicationV3BindingShapes[tool]
	if !errors.As(err, &refusal) || refusal.Word != "missing_field" || shape == nil {
		return err
	}
	var given map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &given) != nil {
		return err
	}
	keys := applicationV3ShapeKeys(shape)
	slices.Sort(keys)
	for _, key := range keys {
		if _, ok := given[key]; !ok {
			return &appmodel.Refusal{Word: "missing_field:" + key, Offset: refusal.Offset}
		}
	}
	return err
}

// applicationV3ShapeKeys is the top-level keys of shape's record, none when
// the record cannot be encoded at its zero values.
func applicationV3ShapeKeys(shape func() []byte) (keys []string) {
	defer func() {
		if recover() != nil {
			keys = nil
		}
	}()
	var fields map[string]json.RawMessage
	if json.Unmarshal(shape(), &fields) != nil {
		return nil
	}
	for key := range fields {
		keys = append(keys, key)
	}
	return keys
}
