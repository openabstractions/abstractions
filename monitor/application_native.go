package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The macOS launcher shows no Panel page for an installer's request. Its Go
// child asks that one confirmation on the launcher's on-top question panel,
// as every rights question is asked (Asks CONTRACT, routing by ask type), and
// applies it through confirmApplicationRegister and confirmApplicationRemove,
// the same code the Panel page's buttons call.

// errApplicationNotChanged ends a run whose request was cancelled or refused.
var errApplicationNotChanged = errors.New("the application entry was not changed")

// applicationQuestionSurface is the native question panel: the session
// evidence, one question at a time and a one-sentence report.
type applicationQuestionSurface interface {
	Unlocked() bool
	Ask(trayQuestionPresentation) (string, error)
	// Dismiss closes the open question; its Ask returns no option.
	Dismiss()
	showReport(presenterReport) bool
}

// applicationReviews bounds how often the person reviews an entry that
// changed under them before the run gives up.
const applicationReviews = 3

// applicationQuestion is one review of the waiting request: the question,
// the digest of the registered entry it showed, and the confirming option.
// refusal is set instead when the request cannot be confirmed at all.
type applicationQuestion struct {
	prompt  trayQuestionPresentation
	shown   string
	confirm string
	refusal string
}

// applicationDetailsText is applicationDetails as plain lines for a native
// question's Details. The values come from the manifest or the registered
// entry; the bridge cleans them as untrusted dialog text.
func applicationDetailsText(heading string, d *fwire.ApplicationDescriptor) string {
	lines := []string{heading, "Title: " + d.Title, "Name: " + d.Name, "Program: " + d.Program}
	if d.StartGuidance != "" {
		lines = append(lines, "Start guidance: "+d.StartGuidance)
	}
	if d.Activation == nil {
		lines = append(lines, "Start: OpenAbstractions cannot start it.")
	} else {
		arguments := "none"
		if len(d.Activation.Arguments) > 0 {
			arguments = strings.Join(d.Activation.Arguments, " ")
		}
		r := d.Activation.Readiness
		lines = append(lines, "Start arguments: "+arguments,
			"Ready when it announces: "+r.Name+" ("+r.Protocol+", "+r.Contract+") within "+
				strconv.FormatInt(d.Activation.ReadinessTimeoutMs, 10)+" ms")
	}
	return strings.Join(lines, "\n")
}

// applicationQuestion reviews the waiting request as the Panel page does:
// the same sentences, the entry it would replace and the digest the
// confirmation must still match.
func (p *servicePanel) applicationQuestion(ctx context.Context) applicationQuestion {
	request := &p.appRequest
	request.mu.Lock()
	pending, remove := request.register, request.remove
	request.mu.Unlock()
	var existing *fwire.ApplicationDescriptor
	readable := false
	name := remove
	if pending != nil {
		name = pending.Name
	}
	if pending == nil || !reservedTitle(pending.Title) {
		if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
			existing, readable = registeredApplication(ctx, apps, name)
		}
	}
	// OpenAbstractions asks this in its own words: blue, as the
	// self-registered question is (trayQuestionViewForAsker).
	prompt := trayQuestionPresentation{Level: promptLevelBlue}
	if pending != nil {
		if reservedTitle(pending.Title) {
			return applicationQuestion{refusal: reservedTitleRefusal}
		}
		sentences := []string{"An installer asks to add this application to OpenAbstractions. Registering grants it no permissions; it asks for those when it needs them."}
		details := applicationDetailsText("Application", pending)
		if existing != nil && applicationDigest(existing) != applicationDigest(pending) {
			sentences = append(sentences, "An application with this name is already registered. Registering replaces it.")
			details = applicationDetailsText("Registered now", existing) + "\n\n" + applicationDetailsText("Replacement", pending)
		}
		if !readable {
			sentences = append(sentences, "The Panel cannot list the registered applications, so it cannot show an entry this would replace.")
		}
		sentences = append(sentences, "Program: "+pending.Program)
		prompt.Title = "Register an application"
		prompt.Instruction = "Register " + pending.Title + "?"
		prompt.Content = strings.Join(sentences, "\n\n")
		prompt.Details = details
		prompt.Footer = "Cancel registers nothing."
		prompt.Choices = []trayQuestionChoice{{Label: "Register", Option: "register"}}
		return applicationQuestion{prompt: prompt, shown: applicationDigest(existing), confirm: "register"}
	}
	sentences := []string{"An uninstaller asks to remove this application's entry from OpenAbstractions. Removing the entry changes no file; the application's own files are its uninstaller's to remove."}
	label := remove
	details := "Name: " + remove
	if existing != nil {
		label = existing.Title
		details = applicationDetailsText("Registered now", existing)
		sentences = append(sentences, "Program: "+existing.Program)
	} else if readable {
		sentences = append(sentences, "No application is registered under this name.")
	}
	prompt.Title = "Remove an application"
	prompt.Instruction = "Remove " + label + "?"
	prompt.Content = strings.Join(sentences, "\n\n")
	prompt.Details = details
	prompt.Footer = "Cancel removes nothing."
	prompt.Choices = []trayQuestionChoice{{Label: "Remove", Option: "remove"}}
	return applicationQuestion{prompt: prompt, shown: applicationDigest(existing), confirm: "remove"}
}

// applicationRequestWait bounds an installer's request run: a session that
// stays locked, or a question nobody answers, ends it unchanged.
var applicationRequestWait = 10 * time.Minute

// settleUnchanged ends the waiting request with nothing changed and returns
// the error naming it.
func (p *servicePanel) settleUnchanged() error {
	request := &p.appRequest
	request.mu.Lock()
	defer request.mu.Unlock()
	message := "Nothing was registered."
	if request.remove != "" {
		message = "Nothing was removed."
	}
	request.settle(message)
	return fmt.Errorf("%w: %s", errApplicationNotChanged, message)
}

// askApplicationRequest asks the installer's one change until the person
// confirms or cancels, then applies it. It returns nil when the entry is
// as asked; a cancel, a refusal, a failure or applicationRequestWait passing
// is an error naming it, and a refusal or failure is also reported on the
// panel. A confirmed change writes no second dialog, as a permission answer
// does not.
func (p *servicePanel) askApplicationRequest(ctx context.Context, surface applicationQuestionSurface) error {
	deadline := time.Now().Add(applicationRequestWait)
	for reviews := 0; ; {
		for !surface.Unlocked() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return p.settleUnchanged()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(250*time.Millisecond, wait)):
			}
		}
		if !time.Now().Before(deadline) {
			return p.settleUnchanged()
		}
		readCtx, cancel := context.WithTimeout(ctx, trayAnswerBudget)
		question := p.applicationQuestion(readCtx)
		cancel()
		title := "Register an application"
		if question.confirm == "remove" {
			title = "Remove an application"
		}
		if question.refusal != "" {
			surface.showReport(presenterReport{Show: true, Title: title, Instruction: question.refusal})
			return fmt.Errorf("%w: %s", errApplicationNotChanged, question.refusal)
		}
		// A question still open at the deadline is dismissed, which answers
		// it as a cancel. Dismiss repeats until Ask returns, so a question
		// shown just after the deadline is closed too.
		asked := make(chan struct{})
		go func() {
			select {
			case <-asked:
				return
			case <-time.After(time.Until(deadline)):
			}
			for {
				surface.Dismiss()
				select {
				case <-asked:
					return
				case <-time.After(250 * time.Millisecond):
				}
			}
		}()
		option, err := surface.Ask(question.prompt)
		close(asked)
		if errors.Is(err, errPromptNotShown) {
			continue // Locked or asleep: ask again on unlock.
		}
		if err != nil {
			return err
		}
		if option != question.confirm {
			return p.settleUnchanged()
		}
		confirmCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		var settled applicationSettled
		if question.confirm == "remove" {
			settled, err = p.confirmApplicationRemove(confirmCtx, question.shown)
		} else {
			settled, err = p.confirmApplicationRegister(confirmCtx, question.shown)
		}
		cancel()
		if (errors.Is(err, applicationReviewAgain) || errors.Is(err, applicationChanged)) && reviews+1 < applicationReviews {
			reviews++
			continue // The entry changed under the person: review it again.
		}
		if err != nil {
			surface.showReport(presenterReport{Show: true, Title: title, Instruction: "Nothing was changed.", Content: err.Error()})
			return fmt.Errorf("%w: %v", errApplicationNotChanged, err)
		}
		log.Print(settled.Message)
		return nil
	}
}
