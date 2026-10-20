package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// messagePermissionPresenterFixture is "Let Claude send messages to
// OpenCode?" as the native prompt shows it.
func messagePermissionPresenterFixture(t *testing.T) (askclient.RecordMetadata, *fakePermissionOperator) {
	t.Helper()
	record, details := typedNativeFixture(t, false, false)
	record.ID = strings.Repeat("9", 32)
	details.Question.Question.Record = &record
	subject := details.Effect.Subject
	// The sender's program is OA's own Claude connector and the receiver is
	// Setup's installed OpenCode connector; connector names alone title
	// neither.
	savedOwn := ownProgramTitles
	ownProgramTitles = func() []oaProgramTitle { return []oaProgramTitle{{subject.Program, "Claude"}} }
	t.Cleanup(func() { ownProgramTitles = savedOwn })
	images := installedConnectorImages(t, "oa-opencode-integration")
	permission, err := grants.AgentMessagePermission(subject,
		applications.CapturedAgentApplication{Name: "oa-claude-integration", Program: subject.Program, FileKey: "sender-key", EnrollmentRevision: "sender-revision"},
		[]applications.CapturedAgentApplication{{Name: "oa-opencode-integration", Program: images["oa-opencode-integration"], FileKey: "opencode-key", EnrollmentRevision: "opencode-revision"}})
	if err != nil {
		t.Fatal(err)
	}
	details.Effect, details.Display, details.EffectDigest = &permission.Effect, &permission.Display, permission.Digest
	details.Origin, details.Purpose = permission.Origin, permission.Purpose
	return record, &fakePermissionOperator{details: details}
}

// Owner 2026-11-09: the person allowed Claude to message OpenCode, the save
// failed, and the Panel still confirmed. The confirmation now follows only a
// saved permission; an Allow serve could not save gets one short notice.
func TestMessageAllowNotSavedShowsOneNoticeAndNoConfirmation(t *testing.T) {
	record, operator := messagePermissionPresenterFixture(t)
	rules := &fakePermissionRules{outcome: rightsclient.PolicyEditOutcomeUnavailable, onApply: operator.release}
	result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(t.Context(), record)
	if result.TypedAnswered == nil || !result.TypedAnswered.NotSaved || len(rules.applied) != 1 {
		t.Fatal("unsaved Allow not recognised", result, rules.applied)
	}
	// Architect popup ruling 2026-11-10, row 32: one OS notification.
	report := presenterResultReport(result)
	if report.OpenQuestions || report.Notice == nil || report.Notice.Title != "Couldn't save that permission" || report.Notice.Body != "Claude will ask again." {
		t.Fatalf("not-saved notice: %+v", report)
	}
	if strings.Contains(report.Notice.Title+report.Notice.Body, "can send messages") {
		t.Fatal("unsaved Allow shown as confirmed", report)
	}
	attrs := permissionCompletionFailure(result)
	if attrs["permission.outcome"] != "unavailable" || attrs["question.outcome"] != "not_applied" || attrs["question.id"] != record.ID {
		t.Fatal("completion record", attrs)
	}
}

// A saved Allow closes silently; the answer is the Activity line (architect
// popup ruling 2026-11-10, row 32).
func TestMessageAllowSavedClosesSilently(t *testing.T) {
	record, operator := messagePermissionPresenterFixture(t)
	rules := &fakePermissionRules{}
	result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(t.Context(), record)
	if result.TypedAnswered == nil || result.TypedAnswered.NotSaved || len(rules.applied) != 1 {
		t.Fatal(result, rules.applied)
	}
	report := presenterResultReport(result)
	if report.OpenQuestions || report.Notice != nil {
		t.Fatalf("saved Allow echoed: %+v", report)
	}
	if attrs := permissionCompletionFailure(result); attrs != nil {
		t.Fatal("saved Allow logged as a failure", attrs)
	}
}

// A lost reply leaves the question answered: the outcome is unknown, and the
// completion record names the failure's class without its text.
func TestPermissionCompletionRecordNamesErrorClass(t *testing.T) {
	record, operator := messagePermissionPresenterFixture(t)
	rules := &fakePermissionRules{err: context.DeadlineExceeded}
	result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(t.Context(), record)
	if result.TypedAnswered == nil || result.TypedAnswered.NotSaved {
		t.Fatal("an answered question read as released", result)
	}
	attrs := permissionCompletionFailure(result)
	if attrs["permission.outcome"] != "unconfirmed" || attrs["permission.error"] != "deadline" || attrs["question.outcome"] != "" {
		t.Fatal("completion record", attrs)
	}
	if report := presenterResultReport(result); report.Notice != nil && strings.Contains(report.Notice.Title+report.Notice.Body, "can send messages") {
		t.Fatal("unconfirmed Allow shown as confirmed", report)
	}
	for err, class := range map[error]string{
		classedErr("messages: target preparation unavailable"):                                "messages: target preparation unavailable",
		fmt.Errorf("%w: open C:\\Users\\someone\\x: denied", rights.ErrMetaHolderUnavailable): rights.ErrMetaHolderUnavailable.Error(),
		errorString(`open C:\Users\someone\rights.json: denied`):                              "other",
		errorString("messages: target preparation unavailable"):                               "other",
	} {
		if got := grants.PermissionErrorClass(err); got != class {
			t.Errorf("class of %q = %q, want %q", err, got, class)
		}
	}
}

// classedErr names its own class, as serve's closed errors do.
type classedErr string

func (e classedErr) Error() string      { return string(e) }
func (e classedErr) ErrorClass() string { return string(e) }

type errorString string

func (e errorString) Error() string { return string(e) }
