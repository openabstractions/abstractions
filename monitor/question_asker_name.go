package main

import (
	"strings"
)

// questionAskerName is the requester as the person knows it in a question's
// heading. A Setup connection image uses its host application’s name, a
// registered program uses its title, and other programs use their file stem.
func questionAskerName(publisher trayPublisher, program, asker string) string {
	if program != "" {
		if _, title, ok := agentConnectorImage(program); ok {
			return strings.TrimSuffix(title, " integration")
		}
		if title, ok := publisher.Names.title(program); ok {
			return strings.TrimSuffix(title, " integration")
		}
		return programStem(program)
	}
	return programStem(asker)
}

// enrollmentWords is an agent connection question as the person reads it.
// Connection titles read "<App> integration" and the person knows the app
// ("Connect Claude?"). The record's text and slots stay as recorded, so the
// question's fingerprint is unchanged.
func enrollmentWords(text string) string {
	return strings.ReplaceAll(text, " integration", "")
}
