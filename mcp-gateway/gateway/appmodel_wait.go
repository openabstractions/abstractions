package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// appCommandWaitArgument takes wait_ms out of an application command call:
// the gateway owns it, and the generated call record does not carry it.
func appCommandWaitArgument(raw json.RawMessage) (json.RawMessage, time.Duration, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw, appCommandWait, nil
	}
	value, ok := fields["wait_ms"]
	if !ok {
		return raw, appCommandWait, nil
	}
	var ms int64
	if json.Unmarshal(value, &ms) != nil || ms < 0 {
		return nil, 0, errors.New("wait_ms must be a whole number of milliseconds")
	}
	delete(fields, "wait_ms")
	rest, err := json.Marshal(fields)
	if err != nil {
		return nil, 0, err
	}
	return bytes.TrimSpace(rest), callWait(ms, appCommandWait), nil
}

// appCommandHeartbeat reports elapsed time while a command runs, so a client
// showing progress shows a live call.
var appCommandHeartbeatEvery = 5 * time.Second

func appCommandHeartbeat(ctx context.Context) func() {
	if _, ok := ctx.Value(progressKey{}).(progressSink); !ok {
		return func() {}
	}
	done := make(chan struct{})
	started := time.Now()
	go func() {
		ticker := time.NewTicker(appCommandHeartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				line, d, t := progressLine("Running the command", 0, 0, "", time.Since(started))
				reportProgress(ctx, line, d, t)
			}
		}
	}()
	return func() { close(done) }
}

// appCommandBudget is the native call's own timeout: the call's wait, or the
// two-minute default.
func appCommandBudget(ctx context.Context) time.Duration {
	if wait, ok := ctx.Value(callWaitKey{}).(time.Duration); ok {
		return wait
	}
	return appCommandWait
}

// appJobArguments fills app_job's instance from the call's own when the
// agent passes only the job a command answered, {"job": "<job>"}: the job
// record names its instance, and the call already names it.
func appJobArguments(tool, arguments, instance string) string {
	if tool != "app_job" {
		return arguments
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &fields) != nil || fields == nil {
		return arguments
	}
	if _, ok := fields["instance"]; ok {
		return arguments
	}
	value, err := json.Marshal(instance)
	if err != nil {
		return arguments
	}
	fields["instance"] = value
	filled, err := json.Marshal(fields)
	if err != nil {
		return arguments
	}
	return string(filled)
}
