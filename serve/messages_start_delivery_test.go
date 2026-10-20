package main

import (
	"context"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Review 31 (3) and (6): Activity says how an addressed session received the
// turn. A turn queued into the running Codex app runs there, not in OA's
// process, so the person's stop refuses it; a headless Codex turn ran
// read-only. An unknown word from the launcher is ignored.
func TestMessageStartRecordsDelivery(t *testing.T) {
	for _, mode := range []string{messageStartDesktopQueue, messageStartReadOnly} {
		h, key, r, calls, _ := chatStartFixture(t)
		sink := &messageAuditSink{}
		if err := h.configureMessageAudit(sink); err != nil {
			t.Fatal(err)
		}
		if err := h.messages.updateMessageNotice(context.Background(), key, r.Token, func(current *messageRecord) {
			entry, err := h.messageAudit.prepare(current)
			if err != nil {
				t.Fatal(err)
			}
			current.Audit = entry
		}); err != nil {
			t.Fatal(err)
		}
		current, err := h.messages.readRecord(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		var delivered func(string)
		h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, _ func(context.Context) error) error {
			calls.Add(1)
			delivered = request.Delivered
			return nil
		}
		allowMessageStartFixture(t, h, key, current)
		if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil || calls.Load() != 1 || delivered == nil {
			t.Fatal("attach start", err, calls.Load())
		}
		delivered("not-a-delivery")
		delivered(mode)
		delivered(messageStartDeliveredCopy) // the first report stands
		stored, err := h.messages.readRecord(context.Background(), key)
		if err != nil || stored.Start == nil || stored.Start.Delivery != mode {
			t.Fatal("delivery not recorded", mode, stored, err)
		}
		row := map[string]string{}
		for _, record := range sink.records {
			if record.Attrs["message_id"] == r.ID+":start" && record.Attrs["delivery"] != "" {
				row = record.Attrs
			}
		}
		if row["delivery"] != mode {
			t.Fatal("Activity row without the delivery", mode, row)
		}
		got := h.stopMessageStartForPerson(context.Background(), r.ID)
		if mode == messageStartDesktopQueue && (got.Outcome != msg.OutcomeConflict || got.Reason != "runs_in_codex") {
			t.Fatal("stop of a turn the Codex app runs", got)
		}
		if mode == messageStartReadOnly && got.Outcome != msg.OutcomePage {
			t.Fatal("stop of OA's own headless turn", got)
		}
		h.messageAudit.close()
	}
}
