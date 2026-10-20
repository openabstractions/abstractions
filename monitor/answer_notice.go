package main

import (
	"context"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// postAnswerNotice posts one notification@1 notice that confirms what the
// person's answer did; the presenter shows it.
func postAnswerNotice(ctx context.Context, title, body string) error {
	return stepNotify(ctx, setup.StepNotice{Title: title, Body: body, Tag: "answer"})
}
