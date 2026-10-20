package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksclient "github.com/openabstractions/abstraction-asks/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	projectwire "github.com/openabstractions/abstractions/serve/go/opencode/session"
)

const openCodeOpenUsage = `Usage: openabstractions opencode open --project <absolute folder> --program EXE --plugin DIR --config FILE [options]
       openabstractions opencode open --resume-request <completed receipt> --program EXE --plugin DIR --config FILE [options]

Open the installed native OpenCode CLI in its fixed-grant Windows sandbox.
The selected folder is requested in OpenAbstractions Questions before launch:
read, run, create, edit and delete access includes all its subfolders.
The sandbox has no network or microphone capability. OA model access still
requires its existing configured rights.

  --project DIR         existing absolute local project folder
  --session ID          open this saved conversation without an automatic prompt
  --resume-request R    after manual close, consume this approved completed
                        receipt and open its exact saved project and conversation
  --new-window          open this command in a desktop console
  --program EXE         required: the official program its own installer placed
  --plugin DIR          required: the installed OA plugin folder
  --config FILE         required: the global config that names the OA provider
  --state-dir DIR       explicit runtime state; requires --endpoint and
                        --runtime-program for independent native server proof
  --endpoint EP         the selected runtime's resolver endpoint
  --runtime-program P   verified executable of that explicit same-account runtime

Additional folders use the installed plugin's project-access tool. Approve its
question, close OpenCode yourself, and use the tool's --resume-request command
to open the saved conversation with that folder. No prompt is sent on reopening.
Exit restores recorded grants. A missing completed-tool receipt refuses resume.
`

type openCodeSessionOptions struct {
	project, conversation, state, endpoint, runtimeProgram, resumeRequest string
	program, plugin, config                                               string
	newWindow                                                             bool
}

func openCodeSessionCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, openCodeOpenUsage)
		return err
	}
	var options openCodeSessionOptions
	flags := flag.NewFlagSet("opencode open", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.StringVar(&options.project, "project", "", "absolute local project folder")
	flags.StringVar(&options.conversation, "session", "", "saved conversation")
	flags.StringVar(&options.resumeRequest, "resume-request", "", "approved completed receipt from a closed launch")
	flags.StringVar(&options.state, "state-dir", "", "selected runtime state")
	flags.StringVar(&options.endpoint, "endpoint", "", "runtime resolver")
	flags.StringVar(&options.runtimeProgram, "runtime-program", "", "runtime executable for native proof")
	flags.BoolVar(&options.newWindow, "new-window", false, "desktop console")
	flags.StringVar(&options.program, "program", "", "official program")
	flags.StringVar(&options.plugin, "plugin", "", "installed OA plugin folder")
	flags.StringVar(&options.config, "config", "", "global config naming the OA provider")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.resumeRequest == "" && (!filepath.IsAbs(options.project) || identity.LocalPath(options.project) != nil) {
		return errors.New("opencode open: --project requires an existing absolute local folder")
	}
	if options.resumeRequest != "" && (!openCodeWord(options.resumeRequest) || options.project != "" || options.conversation != "") {
		return errors.New("opencode open: --resume-request selects its saved project and conversation; omit --project and --session")
	}
	if options.state != "" && (!filepath.IsAbs(options.state) || options.endpoint == "" || options.runtimeProgram == "") {
		return errors.New("opencode open: explicit state requires --endpoint and --runtime-program")
	}
	if (options.endpoint == "") != (options.runtimeProgram == "") {
		return errors.New("opencode open: explicit endpoint and runtime program must be supplied together")
	}
	if options.program == "" || options.plugin == "" || options.config == "" {
		return errors.New("--program, --plugin and --config are required together: the official program, the installed OA plugin and the config naming the OA provider")
	}
	if options.conversation != "" && !openCodeWord(options.conversation) {
		return errors.New("opencode open: invalid saved session ID")
	}
	return startOpenCodeSession(options, output, diagnostics)
}

func openCodeWord(value string) bool {
	return len(value) > 0 && len(value) <= 128 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// rejectOpenCodeAuthorityOverlap uses the filesystem identity owner for this
// application's actual installation, control state and native owner code.
func rejectOpenCodeAuthorityOverlap(folder string, protected []string) error {
	for _, path := range protected {
		same, err := identity.SameFile(path, folder)
		if err != nil {
			return err
		}
		inside, err := identity.Within(path, folder)
		if err != nil {
			return err
		}
		stat, err := os.Stat(path)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			below, err := identity.Within(folder, path)
			if err != nil {
				return err
			}
			inside = inside || below
		}
		if same || inside {
			return fmt.Errorf("OpenCode cannot edit this folder: it overlaps its verified installation or native session control; choose a separate project folder")
		}
	}
	return nil
}

func validOpenCodeCompletion(completion projectwire.ProjectCompletion, request projectwire.ProjectRequest, receipt string, created, expires, now time.Time) bool {
	return now.Before(expires) && completion.RequestKey == request.RequestKey && completion.SessionID == request.SessionID &&
		completion.MessageID == request.MessageID && completion.Receipt == receipt && receipt != "" &&
		openCodeWord(completion.PartID) && openCodeWord(completion.ToolCallID) &&
		completion.CompletedAt >= created.UnixMilli() && completion.CompletedAt < expires.UnixMilli() && completion.CompletedAt <= now.UnixMilli()
}

func openCodeProjectReceiptMessage(outcome projectwire.ProjectOutcome) string {
	switch outcome {
	case projectwire.ProjectOutcomePending:
		return "The folder question is waiting for your manual answer. Access has not changed."
	case projectwire.ProjectOutcomeDenied:
		return "The folder request was refused. Access has not changed."
	case projectwire.ProjectOutcomeExpired:
		return "The folder approval expired. Access has not changed."
	case projectwire.ProjectOutcomeApprovedPendingClose:
		return "Your manual approval is bound to this folder identity. Close OpenCode yourself before using the fresh launch command; access has not changed."
	case projectwire.ProjectOutcomeReceiptRecorded:
		return "The committed completed-tool receipt is retained for your fresh launch. Access has not changed."
	default:
		return "This folder request is unavailable. Access has not changed."
	}
}

// The native session owns the tuple. The human text merely describes it; only
// the admitting Book's retained ID and manual answer can satisfy this predicate.
// The asker comparison is a display sanity check only: the Book records the
// display name it derives from the bound program (asks.AskerDisplay, ASK-A2),
// and the question ID, which only this owner's own observation returned,
// binds the record to this owner's scope.
func approvedOpenCodeFolder(read asksclient.OperatorQuestion, questionID, ownerProgram string, question asksclient.ApplicationQuestion, created, expires, now time.Time) bool {
	if !now.Before(expires) || read.Outcome != asksclient.OperatorQuestionOutcomeFound || read.Record == nil ||
		read.AnsweredBy == nil || *read.AnsweredBy != asksclient.AnswererOperator {
		return false
	}
	r := read.Record
	if r.ID != questionID || r.Key != question.Key || r.Key != "opencode.project.edit" ||
		r.About != question.Slots["folder"] || r.Asker != asks.AskerDisplay(ownerProgram) ||
		r.Option != "once" || !r.Yes || r.Kept {
		return false
	}
	template, ok := asks.Find(question.Key)
	if !ok {
		return false
	}
	text, err := template.Render(asks.Ask{Asker: asks.AskerDisplay(ownerProgram), Slots: question.Slots})
	if err != nil || r.Text != text {
		return false
	}
	answered, err := time.Parse(time.RFC3339Nano, r.Answered)
	return err == nil && !answered.Before(created) && answered.Before(expires) && !answered.After(now)
}
