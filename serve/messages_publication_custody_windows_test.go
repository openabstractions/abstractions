package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestNativeMessagePublicationDirectoryHandoff(t *testing.T) {
	f := messagesObserveFixture(t)
	const app, source = "native-agent", "source"
	instance, _ := messagesStateOpen(t, f, f.reader, app, source)
	d := f.run.d
	ctx, cancel := context.WithTimeout(f.run.ctx, 5*time.Second)
	defer cancel()
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	_, who, session, process, err := nativeInboxPeer(pin, d.now())
	if err != nil {
		t.Fatal(err)
	}
	if !d.lock(ctx) {
		t.Fatal(ctx.Err())
	}
	admission := *d.nativeInboxes.rows[app+"\x00"+source].admission
	var facts messagePublicationFacts
	err = d.withRegistrationReadLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		_, marks, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		return d.captureMessagePublication(ctx, app, nil, marks.Data, &facts)
	})
	d.unlock()
	if err != nil {
		t.Fatal(err)
	}
	if facts.descriptor != filepath.Join(d.dir, "descriptors.json") {
		t.Fatal("unexpected descriptor lock")
	}
	check := func() (bool, error) {
		row, err := d.nativeMessageOwnerLocked(pin, instance, source, app, who, session, process, &admission)
		return row != nil, err
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		published, err := d.publishMessageWithReadCustody(ctx, &facts, func() error {
			if owned, err := check(); err != nil || !owned {
				return fmt.Errorf("first owner: %t, %v", owned, err)
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if !published {
			err = fmt.Errorf("first publication did not enter: %v", err)
		}
		first <- err
	}()
	select {
	case <-entered:
	case err := <-first:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	// Live inbox checks can proceed without waiting for an unrelated durable
	// publication. Registration and writer-owned sweeps still wait below.
	lookup, stopLookup := context.WithTimeout(ctx, time.Second)
	live, err := d.messageContextOpen(lookup, app, source)
	stopLookup()
	if !live || err != nil {
		t.Fatalf("live inbox lookup under publication custody: %t, %v", live, err)
	}
	secondCtx, stop := context.WithTimeout(ctx, time.Second)
	published, err := d.publishMessageWithReadCustody(secondCtx, &facts, func() error {
		if owned, err := check(); err != nil || !owned {
			return fmt.Errorf("second owner: %t, %v", owned, err)
		}
		return nil
	})
	stop()
	if !published || err != nil {
		t.Fatalf("second publication: %t, %v", published, err)
	}
	blockedCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	blocked := d.withdrawInSession(blockedCtx, who, session, app, instance)
	stop()
	if blocked.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatalf("withdraw crossed publication custody: %v", blocked)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if withdrawn := d.withdrawInSession(ctx, who, session, app, instance); withdrawn.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("withdraw after publication: %v", withdrawn)
	}
	published, err = d.publishMessageWithReadCustody(ctx, &facts, func() error {
		if owned, err := check(); err != nil || owned {
			return fmt.Errorf("withdrawn owner: %t, %v", owned, err)
		}
		return nil
	})
	if !published || err != nil {
		t.Fatalf("descriptor validation after withdrawal: %t, %v", published, err)
	}
}
