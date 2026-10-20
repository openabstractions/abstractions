//go:build windows

package main

import (
	"context"
	"testing"
)

func TestMessagesReceiverWatchEndsDuringCurrentClassCheck(t *testing.T) {
	run, _ := messages1608Fixture(t)
	type cancelAtImage struct{}
	var stop func()
	run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		image, err := readApplicationAgentImage(ctx, program, binding)
		if ctx.Value(cancelAtImage{}) == true && stop != nil {
			stop()
		}
		return image, err
	}
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", ""); got.outcome != "opened" {
		t.Fatal(got)
	}
	host := &applicationsHost{directory: run.d}
	receiver := &messagesReceiver{host: host, call: call, ctx: run.ctx}
	actor, _, _, ok := receiver.owner(instance, "ctx")
	if !ok {
		t.Fatal("native owner missing")
	}
	var err error
	stop, err = receiver.watchMessages(actor)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !host.messageIsWatching(run.ctx, actor.Account, actor.Program, "ctx") {
		t.Fatal("live admitted wait absent")
	}
	// Verify the image again, so the wait ends inside that verification.
	run.d.agentImages.forget()
	if host.messageIsWatching(context.WithValue(run.ctx, cancelAtImage{}, true), actor.Account, actor.Program, "ctx") {
		t.Fatal("retired wait survived final membership check")
	}
}
