package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// presenterNotice is the one OS notification a presenter report posts in
// place of a popup: a failure of work the person started, with the action
// that continues it (architect popup ruling 2026-11-10; CONV-Q2, CONV-U1).
type presenterNotice struct {
	Title, Body string
	// Action is the notice's one button, noticeOpenSetup or none.
	Action    string
	Key       string
	ExpiresMs int64
}

// noticeOpenSetup is the "Open Setup" action of a Setup that didn't finish.
const noticeOpenSetup = "open-setup"
const noticeReviewQuestions = "review-questions"

// presenterNoticePrefix starts every presenter notice's request key, so its
// action reaches activatePresenterNotice.
const presenterNoticePrefix = "oa-presenter-"

var presenterNoticeSerial atomic.Uint64

// presenterNoticeRequest is the notification a presenter notice posts.
func presenterNoticeRequest(n presenterNotice, now time.Time) askclient.NotificationRequest {
	until := now.Add(24 * time.Hour).UnixMilli()
	key := presenterNoticePrefix + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.FormatUint(presenterNoticeSerial.Add(1), 36)
	if n.Key != "" {
		key, until = n.Key, n.ExpiresMs
	}
	q := askclient.NotificationRequest{RequestRef: askclient.RequestRef{Key: key, ReplayUntilMs: until}, Title: n.Title, Body: n.Body,
		Tag: presenterNoticePrefix + "report", Delivery: askclient.NotificationDeliveryNow, ExpiresMs: until, Actions: []askclient.NotificationAction{}}
	switch n.Action {
	case noticeReviewQuestions:
		q.Actions = append(q.Actions, askclient.NotificationAction{ID: noticeReviewQuestions, Title: "Review", Activation: askclient.NotificationActivationApp})
		q.Tag = ""
	case noticeOpenSetup:
		q.Actions = append(q.Actions, askclient.NotificationAction{ID: noticeOpenSetup, Title: "Open Catalog", Activation: askclient.NotificationActivationApp})
	}
	return q
}

// postPresenterNotice posts n through the notification owner. Tests replace
// it.
var postPresenterNotice = func(ctx context.Context, n presenterNotice) error {
	q := presenterNoticeRequest(n, time.Now())
	if !asks.ValidNotificationRequest(q) {
		return errors.New("the notice is invalid")
	}
	notifications, err := panelMachine().ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	posted, err := notifications.PostContext(ctx, q)
	if err != nil {
		return err
	}
	switch posted.Outcome {
	case askclient.NotificationOutcomePosted, askclient.NotificationOutcomeScheduled:
		return nil
	}
	return fmt.Errorf("the notice was not posted: %s", posted.Outcome)
}

// launchSetupPanel opens the Panel on Setup; tests replace it.
var launchSetupPanel = launchProgramRepairPanel

// activatePresenterNotice runs a presenter notice's action. ok is false for a
// notice another owner posted.
func activatePresenterNotice(record askclient.NotificationRecord, action string) (handled bool, err error) {
	if !strings.HasPrefix(record.Request.RequestRef.Key, presenterNoticePrefix) {
		return false, nil
	}
	switch action {
	case noticeOpenSetup:
		return true, launchSetupPanel()
	case noticeReviewQuestions:
		return true, launchQuestionPanel()
	default:
		return true, errors.New("presenter notice action unavailable")
	}
}
