package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func TestMessageStartConfirmationRequiresExactNativeOccurrenceAndCurrentAdmission(t *testing.T) {
	for _, mode := range []string{"current", "ordinary-helper", "pid", "birth", "account", "program", "session", "image", "generation", "recipe", "uppercase", "revoked", "expired", "runtime-ended", "closed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			peer, recheck := applicationV3NativePeer(t)
			session, _ := applicationV3PendingSession(t, peer)
			subject, err := rightsclient.SubjectFromPeer(peer)
			if err != nil {
				t.Fatal(err)
			}
			binding := session.bindingValue()
			d := &applicationDirectory{owner: subject.Account, epoch: binding.runtimeEpoch}
			_ = newOwnedMessageStartLauncher(d, context.Background())
			var checks atomic.Int32
			digest := strings.Repeat("a", 64)
			row := &ownedMessageStart{ctx: context.Background(), request: applicationMessageStartRequest{Application: binding.application, Account: subject.Account, Program: subject.Program, Session: binding.desktop, ProgramKey: binding.imageCode, RegistrationBinding: binding.manifestRevision, ProfileFingerprint: digest}, authority: func(context.Context) error {
				checks.Add(1)
				if mode == "revoked" {
					return errors.New("question/consent/pair/recipe ended")
				}
				return nil
			}}
			if mode != "ordinary-helper" {
				d.messageStartOwned.rows[session] = row
			}
			switch mode {
			case "account":
				row.request.Account += "-other"
			case "program":
				row.request.Program += "-other"
			case "expired":
				ended, stop := context.WithCancel(context.Background())
				stop()
				row.ctx = ended
			case "runtime-ended":
				ended, stop := context.WithCancel(context.Background())
				stop()
				d.messageStartOwned.ctx = ended
			case "pid":
				session.binding.pid++
			case "birth":
				session.binding.started = session.binding.started.Add(time.Nanosecond)
			case "session":
				row.request.Session += "-other"
			case "image":
				row.request.ProgramKey += "-other"
			case "generation":
				d.epoch += "-other"
			case "recipe":
				digest = strings.Repeat("b", 64)
			case "uppercase":
				digest = strings.Repeat("A", 64)
			case "closed":
				_ = session.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			caller := applicationV3Caller{subject: subject, peer: peer, recheck: recheck}
			result := d.confirmMessageStart(ctx, caller, digest)
			if mode == "current" {
				if result.Outcome != wire.ApplicationOutcomeApplied || result.Instance != "" || result.Reason != "" || !row.confirmed {
					t.Fatal(result)
				}
				if again := d.confirmMessageStart(ctx, caller, digest); again.Outcome != wire.ApplicationOutcomeApplied || checks.Load() != 2 {
					t.Fatal(again, checks.Load())
				}
			} else if result.Outcome == wire.ApplicationOutcomeApplied || row.confirmed {
				t.Fatal("non-admitted invocation confirmed", mode, result)
			}
		})
	}
}

func TestMessageStartOwnedCarrierDispatchesOnePrivateVerbWithoutInstance(t *testing.T) {
	session, server := ownedSessionFixture(t, nil)
	session.binding.instance = ""
	reader := ownedFixtureInitialize(t, session, server, "2025-11-25")
	var dispatches atomic.Int32
	custody := applicationMessageStartRequest{Account: "native-account", ProgramKey: "native-program", EnrollmentRevision: "enrollment-1", ProfileFingerprint: strings.Repeat("a", 64)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := ownedFixtureRequest(t, reader)
		var parameters struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      map[string]any  `json:"_meta"`
		}
		decodeErr := json.Unmarshal(request["params"], &parameters)
		binding, bindingErr := startprofile.ParseCustodyMetadata(parameters.Meta)
		if string(request["method"]) != `"tools/call"` || decodeErr != nil || parameters.Name != "receiver_start" || string(parameters.Arguments) != "{}" || bindingErr != nil || binding.Account != custody.Account || binding.ProgramKey != custody.ProgramKey || binding.EnrollmentRevision != custody.EnrollmentRevision || binding.RecipeDigest != custody.ProfileFingerprint {
			t.Error("unexpected private dispatch", string(request["method"]), string(request["params"]))
		}
		dispatches.Add(1)
		answer := applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
		ownedFixtureReply(server, request["id"], map[string]any{"content": []map[string]string{{"type": "text", "text": string(answer)}}, "isError": false})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.dispatchReceiverStart(ctx, func(context.Context) error { return nil }, custody); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := session.dispatchReceiverStart(ctx, func(context.Context) error { return nil }, custody); err == nil || dispatches.Load() != 1 {
		t.Fatal("private start redispatched", err, dispatches.Load())
	}
}

func TestMessageStartConfirmationGeneratedMethodUsesExistingEmptyInstanceGrammar(t *testing.T) {
	raw := wire.EncodeApplicationChange(&wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied})
	answer, err := wire.DecodeApplicationChange(raw)
	if err != nil || answer.Instance != "" || answer.Reason != "" {
		t.Fatal(answer, err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["instance"]; !ok {
		t.Fatal("required empty instance missing", object)
	}
}
