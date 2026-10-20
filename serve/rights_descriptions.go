package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/client"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"io"
	"os"
	"unicode/utf8"
)

type actionDescriptionJSON struct {
	Action       string       `json:"action"`
	FeatureApp   probeSubject `json:"feature_app"`
	AppLabel     string       `json:"app_label"`
	Description  string       `json:"description"`
	DeclaredRisk string       `json:"declared_risk"`
}
type publishedDescriptionJSON struct {
	Action      string                 `json:"action"`
	Choices     []string               `json:"choices"`
	Description *actionDescriptionJSON `json:"description,omitempty"`
	PublishedBy *probeSubject          `json:"published_by,omitempty"`
	PublishedAt string                 `json:"published_at,omitempty"`
}

func descriptionJSON(item *wire.DescribedAction) *publishedDescriptionJSON {
	if item == nil {
		return nil
	}
	out := &publishedDescriptionJSON{Action: item.Action, Choices: item.Choices, PublishedAt: item.PublishedAt}
	if item.PublishedBy != nil {
		out.PublishedBy = subjectOf(*item.PublishedBy)
	}
	if d := item.Description; d != nil {
		out.Description = &actionDescriptionJSON{Action: d.Action, FeatureApp: *subjectOf(d.FeatureApp), AppLabel: d.AppLabel, Description: d.Description, DeclaredRisk: d.DeclaredRisk}
	}
	return out
}

// readDescriptionObject checks the two fixed object shapes before decoding their
// scalar fields. Duplicate keys and trailing documents are refused.
func readDescriptionObject(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("descriptor must be UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("descriptor must be a JSON object")
	}
	out := map[string]json.RawMessage{}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || out[key] != nil {
			return nil, errors.New("descriptor has duplicate or unknown fields")
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return nil, err
		}
		out[key] = raw
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("descriptor has trailing bytes")
	}
	if len(out) != len(keys) {
		return nil, errors.New("descriptor is missing a required field")
	}
	return out, nil
}
func readActionDescriptionFile(path string) (wire.ActionDescription, error) {
	f, err := os.Open(path)
	if err != nil {
		return wire.ActionDescription{}, fmt.Errorf("read descriptor: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return wire.ActionDescription{}, err
	}
	if len(data) > 64<<10 {
		return wire.ActionDescription{}, errors.New("descriptor exceeds 64 KiB")
	}
	fields, err := readDescriptionObject(data, "action", "feature_app", "app_label", "description", "declared_risk")
	if err != nil {
		return wire.ActionDescription{}, err
	}
	app, err := readDescriptionObject(fields["feature_app"], "account", "program")
	if err != nil {
		return wire.ActionDescription{}, err
	}
	d := wire.ActionDescription{}
	for _, field := range []struct {
		raw    json.RawMessage
		target *string
	}{{fields["action"], &d.Action}, {fields["app_label"], &d.AppLabel}, {fields["description"], &d.Description}, {fields["declared_risk"], &d.DeclaredRisk}, {app["account"], &d.FeatureApp.Account}, {app["program"], &d.FeatureApp.Program}} {
		if err = json.Unmarshal(field.raw, field.target); err != nil {
			return wire.ActionDescription{}, errors.New("descriptor fields must be strings")
		}
	}
	return rightspolicy.NormalizeActionDescription(d)
}
func rightsPublishDescription(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), command string, d wire.ActionDescription, revision string, asJSON bool, output io.Writer) error {
	op, err := resolveOperator(machine, source, call, command)
	if err != nil {
		return err
	}
	if revision == "" {
		ctx, cancel := call()
		page, err := op.ListPolicyContext(ctx, "", 1)
		cancel()
		if err != nil {
			return notResolved(command, source, err)
		}
		if page.Outcome != wire.PolicyPageOutcomePage {
			return rightsPrint(output, asJSON, rightsReply{Command: command, Outcome: page.Outcome.String()}, rightsOperatorRefusal(command, page.Outcome.String(), "listing the policy revision"))
		}
		revision = page.Revision
	}
	ctx, cancel := call()
	edit, err := op.RegisterDescribedActionContext(ctx, revision, d)
	cancel()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: description publication unconfirmed; read the catalogue before another edit: %w", command, err)}
	}
	reply := rightsReply{Command: command, Outcome: edit.Outcome.String(), Revision: edit.Revision, Action: d.Action, Description: descriptionJSON(edit.Current)}
	var failure error
	if edit.Outcome != wire.ActionEditOutcomeApplied {
		failure = refusal(command, edit.Outcome.String(), "at revision "+edit.Revision)
	}
	return rightsPrint(output, asJSON, reply, failure)
}
