package main

import (
	"regexp"
	"strings"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// agentUpdateSentence reads the catalogue's connection-update sentence:
// the member list and the request code it ends in.
var agentUpdateSentence = regexp.MustCompile(`^Update the (.+?) connections\? .* Request ([0-9a-f]{16,})\.$`)

// agentUpdateQuestionView is the connection update's native popup (architect
// popup ruling 2026-11-10, row 8): one question naming the apps, one
// sentence, Update / Not now, and the request code and local time behind
// Details. A same-source update that widens nothing asks no question
// (ASK-R12); this popup is for an update that widens or whose source is
// unproven. It names the apps and the act and gives no internal reason
// (architect 2026-11-11).
func agentUpdateQuestionView(record askclient.RecordMetadata, _ trayPublisher, choices []trayQuestionChoice, now time.Time) (trayQuestionPresentation, bool) {
	m := agentUpdateSentence.FindStringSubmatch(record.Text)
	if m == nil {
		return trayQuestionPresentation{}, false
	}
	// Connection titles read "<App> integration"; the person knows the app.
	members := strings.ReplaceAll(m[1], " integration", "")
	details := []string{}
	if record.Asked != "" {
		details = append(details, "Asked: "+localAskedTime(record.Asked, now))
	}
	details = append(details, "Request: "+m[2], "Question: "+record.ID)
	return trayQuestionPresentation{
		presentation: nativeQuestionConsent,
		Title:        popupTitle,
		Instruction:  "Update OpenAbstractions in " + members + "?",
		Content:      "Restart them afterwards.",
		Details:      strings.Join(details, "\n"),
		Choices:      choices,
		// This update widens what the connections may do: Not now is the
		// default, as for every permission, and Escape means Not now.
		Default: "not_now",
		Dismiss: "not_now",
		Level:   promptLevelBlue,
	}, true
}

// localAskedTime shows a recorded RFC 3339 time in local, human form:
// "Today 13:29", "Yesterday 09:05" or "2 Nov 2026 18:40".
func localAskedTime(asked string, now time.Time) string {
	at, err := time.Parse(time.RFC3339Nano, asked)
	if err != nil {
		return asked
	}
	at = at.In(now.Location())
	day := func(t time.Time) time.Time {
		y, mo, d := t.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
	}
	switch day(now).Sub(day(at)) {
	case 0:
		return "Today " + at.Format("15:04")
	case 24 * time.Hour:
		return "Yesterday " + at.Format("15:04")
	}
	return at.Format("2 Jan 2006 15:04")
}
