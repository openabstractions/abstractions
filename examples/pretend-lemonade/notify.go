package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	asksbook "github.com/openabstractions/abstraction-asks/go"
	asks "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// --notify-when-done posts a notification the runtime holds until the
// download ends, then this app exits (ASK-N1). The person can choose Copy now
// or Show in Activity hours later with nothing of this app running. A later
// run with --notification reads the recorded choice: Copy now copies the
// verified result as a normal run would, and the app then acknowledges it.
// The notification's full reference stays in a small file beside the output,
// so every later run names the same occurrence.

const (
	notifyCopy     = "copy"
	notifyActivity = "activity"
)

// downloadName is the file name the copy uses, as deliver derives it.
func downloadName(source string) string {
	parsed, err := url.Parse(source)
	if err != nil {
		return "download"
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" {
		return "download"
	}
	return name
}

// notificationRefFile holds one download's notification reference.
func notificationRefFile(destination, requestKey string) string {
	return filepath.Join(destination, ".pretend-lemonade-"+requestKey[len(requestKey)-16:]+".notification.json")
}

// retainedRef reads the reference this download already posted under, or
// creates and durably writes a fresh one: the replay deadline is fixed once.
func retainedRef(path, requestKey string, create bool) (asks.RequestRef, error) {
	var ref asks.RequestRef
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &ref); err == nil && asksbook.ValidChoiceReference(ref) {
			return ref, nil
		}
		return ref, fmt.Errorf("%s holds no notification reference", path)
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return ref, err
	}
	sum := sha256.Sum256([]byte(requestKey))
	ref = asks.RequestRef{Key: fmt.Sprintf("pretend-lemonade-done-%x", sum[:12]), ReplayUntilMs: time.Now().Add(7*24*time.Hour - time.Minute).UnixMilli()}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ref, err
	}
	data, err = json.Marshal(ref)
	if err != nil {
		return ref, err
	}
	return ref, os.WriteFile(path, data, 0o600)
}

// postDone posts the held notification for an accepted download.
func postDone(ctx context.Context, selected *facade.Machine, ref asks.RequestRef, operationID, name string, size int64, out io.Writer) error {
	notifications, err := selected.ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("notification service: %w", err)
	}
	expires := time.Now().Add(24 * time.Hour).UnixMilli()
	if expires > ref.ReplayUntilMs {
		expires = ref.ReplayUntilMs
	}
	q := asks.NotificationRequest{
		RequestRef: ref,
		Title:      "Download finished",
		Body:       fmt.Sprintf("%s (%d bytes) is ready to copy.", name, size),
		Actions: []asks.NotificationAction{
			{ID: notifyCopy, Title: "Copy now", Activation: asks.NotificationActivationApp},
			{ID: notifyActivity, Title: "Show in Activity", Activation: asks.NotificationActivationOpenActivity},
		},
		ExpiresMs:   expires,
		OperationID: operationID,
		Delivery:    asks.NotificationDeliveryOperationEnd,
	}
	posted, err := notifications.PostContext(ctx, q)
	if err != nil {
		return err
	}
	switch posted.Outcome {
	case asks.NotificationOutcomeScheduled, asks.NotificationOutcomePosted, asks.NotificationOutcomeActed, asks.NotificationOutcomeDismissed, asks.NotificationOutcomeWithdrawn:
		_, err = fmt.Fprintf(out, "notification %s: %s; this app can exit\n", posted.ID, posted.Outcome)
		return err
	}
	return fmt.Errorf("notification not posted: %s", posted.Outcome)
}

// notificationOutcome reads the recorded choice. copy reports whether the
// person chose Copy now; ack acknowledges the result once the caller acted on it.
func notificationOutcome(ctx context.Context, selected *facade.Machine, ref asks.RequestRef, out io.Writer) (copy bool, ack func() error, err error) {
	notifications, err := selected.ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return false, nil, fmt.Errorf("notification service: %w", err)
	}
	seen, err := notifications.ObserveContext(ctx, ref, 0)
	if err != nil {
		return false, nil, err
	}
	line := "notification: " + seen.Outcome.String()
	if seen.Result != nil && seen.Result.Action != "" {
		line += " (" + seen.Result.Action + ")"
	}
	if _, err := fmt.Fprintln(out, line); err != nil {
		return false, nil, err
	}
	if seen.Result == nil {
		return false, nil, nil
	}
	result := *seen.Result
	return result.Action == notifyCopy, func() error {
		acked, err := notifications.AcknowledgeContext(ctx, ref, result)
		if err == nil && acked.Outcome != asks.ChoiceAckOutcomeAcknowledged {
			err = fmt.Errorf("notification acknowledgement: %s", acked.Outcome)
		}
		return err
	}, nil
}
