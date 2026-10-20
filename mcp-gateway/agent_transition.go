package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
)

// immutableHelperTransitions says whether this platform offers the immutable
// helper transition: Darwin keeps the prior helper image beside the new one,
// and no other platform has the command. It chooses no path rule.
func immutableHelperTransitions() bool { return runtime.GOOS == "darwin" }

// Previous is recovery data, never native authority. The Panel's Applications
// owner revalidates the exact capture before retiring an old enrollment.
type agentPrevious struct {
	Application string `json:"application"`
	Program     string `json:"program"`
	FileKey     string `json:"file_key"`
	Revision    string `json:"revision"`
}

func decodeAgentPrevious(raw []byte) (*agentPrevious, error) {
	fields, err := claudeConfigObject(raw)
	if err != nil || len(fields) != 4 {
		return nil, errors.New("unknown prior enrollment capture")
	}
	var out agentPrevious
	for key, value := range fields {
		var text string
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil || text == "" {
			return nil, errors.New("invalid prior enrollment capture")
		}
		switch key {
		case "application":
			out.Application = text
		case "program":
			out.Program = text
		case "file_key":
			out.FileKey = text
		case "revision":
			out.Revision = text
		default:
			return nil, errors.New("unknown prior enrollment field")
		}
	}
	return &out, nil
}

// transitionOwnedConfiguration has the vendor's explicit name-based gap. Only
// an exact known current value is removed. A failed publication restores the
// old value only while the namespace is proven empty and the original marker
// and identity checks still hold. Unknown occupants are never removed.
func transitionOwnedConfiguration(ctx context.Context, path string, base, next []byte, old, new any, read func() (any, error), write func(any) error, identity func() error) error {
	check := func() error {
		if err := identity(); err != nil {
			return err
		}
		actual, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
		if err != nil || !bytes.Equal(actual, base) {
			return errors.New("connection ownership changed; preserved")
		}
		return nil
	}
	current, err := read()
	if err != nil || !reflect.DeepEqual(current, old) {
		return errors.New("owned connection changed; preserved")
	}
	if err := check(); err != nil {
		return err
	}
	restore := func(cause error) error {
		if err := check(); err != nil {
			return errors.Join(cause, errors.New("prior connection restoration is unconfirmed; inspect retained ownership"))
		}
		current, err := read()
		if err != nil {
			return errors.Join(cause, errors.New("connection readback unknown; no recovery mutation"))
		}
		if reflect.DeepEqual(current, old) {
			return cause
		}
		if current != nil {
			return errors.Join(cause, errors.New("changed connection preserved; no recovery mutation"))
		}
		if err := write(old); err != nil {
			return errors.Join(cause, errors.New("prior connection restoration reply unavailable"))
		}
		current, err = read()
		if err != nil || !reflect.DeepEqual(current, old) {
			return errors.Join(cause, errors.New("prior connection restoration unconfirmed"))
		}
		return errors.Join(cause, errors.New("exact prior connection restored; original ownership retained"))
	}
	if err := write(nil); err != nil {
		return restore(errors.New("connection removal reply unavailable"))
	}
	current, err = read()
	if err != nil || current != nil {
		return errors.New("connection removal is unconfirmed; original ownership and unknown objects preserved")
	}
	if err := check(); err != nil {
		return restore(err)
	}
	writeErr := write(new)
	current, err = read()
	if err != nil {
		return errors.New("new connection outcome unknown; original ownership retained")
	}
	if !reflect.DeepEqual(current, new) {
		return restore(errors.Join(writeErr, errors.New("new connection not confirmed")))
	}
	if err := check(); err != nil {
		return errors.Join(err, errors.New("new connection saved; ownership commit unknown"))
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	if err := store.WriteContext(ctx, path, casapi.Value{Data: base}, next); err != nil {
		actual, readErr := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
		if readErr == nil && bytes.Equal(actual, next) {
			return identity()
		}
		if readErr == nil && bytes.Equal(actual, base) && identity() == nil {
			current, readErr = read()
			if readErr == nil && reflect.DeepEqual(current, new) {
				_ = write(nil)
				return restore(errors.New("ownership commit failed"))
			}
		}
		return errors.New("new connection saved; ownership commit unknown; retained images require inspection")
	}
	return identity()
}
