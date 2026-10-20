//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-download/go/powershell"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-inference/go/providerhost/enginefiles"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	projectwire "github.com/openabstractions/abstractions/serve/go/opencode/session"
	"golang.org/x/sys/windows"
)

// This is application-owned, protected recovery state, separate from OpenCode's
// mutable conversations. No guest field becomes an account, SID or program.
type openCodeFolderApproval struct {
	Request      projectwire.ProjectRequest     `json:"request"`
	Folder       string                         `json:"folder"`
	FileID       string                         `json:"file_id"`
	Access       sandbox.Access                 `json:"access"`
	Question     asksclient.ApplicationQuestion `json:"question"`
	QuestionID   string                         `json:"question_id,omitempty"`
	Created      time.Time                      `json:"created"`
	Expires      time.Time                      `json:"expires"`
	Receipt      string                         `json:"receipt,omitempty"`
	Approved     bool                           `json:"approved"`
	ManualAnswer bool                           `json:"manual_answer"`
	AnsweredBy   *asksclient.Answerer           `json:"answered_by,omitempty"`
	Answered     string                         `json:"answered,omitempty"`
	Outcome      projectwire.ProjectOutcome     `json:"outcome"`
	Completion   *projectwire.ProjectCompletion `json:"completion,omitempty"`
	Consumed     bool                           `json:"consumed"`
}

type openCodeLaunchRecord struct {
	Format           int                                `json:"format"`
	ID               string                             `json:"id"`
	Account          string                             `json:"account"`
	OwnerProgram     string                             `json:"owner_program"`
	OwnerProgramID   string                             `json:"owner_program_id"`
	OwnerPID         int                                `json:"owner_pid"`
	OwnerStart       time.Time                          `json:"owner_start"`
	Profile          string                             `json:"profile"`
	SID              string                             `json:"sid"`
	StateID          string                             `json:"state_id"`
	RuntimeEndpoint  string                             `json:"runtime_endpoint"`
	RuntimeProgramID string                             `json:"runtime_program_id"`
	GuestProgramID   string                             `json:"guest_program_id"`
	PackageRootID    string                             `json:"package_root_id"`
	Phase            string                             `json:"phase"`
	ChildPID         int                                `json:"child_pid,omitempty"`
	ChildStart       time.Time                          `json:"child_start,omitempty"`
	ClosedJob        bool                               `json:"closed_job"`
	Initial          *openCodeFolderApproval            `json:"initial"`
	Requests         map[string]*openCodeFolderApproval `json:"requests"`
	Failure          string                             `json:"failure,omitempty"`
}

type openCodeNativeSession struct {
	mu            sync.Mutex
	ctx           context.Context
	record        openCodeLaunchRecord
	recordPath    string
	installed     *installedOpenCode
	protected     []string
	questions     *asksclient.Client
	operator      *asksclient.Operator
	process       *supervision.Process
	resumeCommand string
}

func openCodeNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func openCodeWriteRecord(path string, value any) (err error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() {
		if err != nil {
			_ = temporary.Close()
		}
		_ = os.Remove(name)
	}()
	if _, err = temporary.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *openCodeNativeSession) save() error { return openCodeWriteRecord(s.recordPath, s.record) }

func openCodeSessionLock(dir string) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(dir, "session.lock"))
	if err != nil {
		return 0, err
	}
	// Share mode zero gives this existing app owner one session at a time.
	return windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

func openCodeRuntime(ctx context.Context, options openCodeSessionOptions, self identity.User) (bootstrap.Selection, error) {
	if options.endpoint == "" {
		return bootstrap.SelectInstalled(ctx)
	}
	program := identity.CanonicalProgramPath(options.runtimeProgram)
	if !filepath.IsAbs(program) || identity.LocalPath(program) != nil {
		return bootstrap.Selection{}, errors.New("OpenCode: absolute local runtime program required")
	}
	if err := enginefiles.CheckPlainPath(program); err != nil {
		return bootstrap.Selection{}, err
	}
	return bootstrap.Selection{Endpoint: options.endpoint, EndpointFromEnvironment: true,
		Server: listen.ServerExpectation{Principal: self, Program: program}}, nil
}

func (s *openCodeNativeSession) folder(request projectwire.ProjectRequest) (*openCodeFolderApproval, error) {
	if !openCodeWord(request.RequestKey) || !filepath.IsAbs(request.Folder) || identity.LocalPath(request.Folder) != nil {
		return nil, errors.New("OpenCode: an absolute existing local folder and bounded request key are required")
	}
	folder := identity.CanonicalProgramPath(request.Folder)
	stat, err := os.Lstat(folder)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("OpenCode: a regular local folder is required")
	}
	if err := rejectOpenCodeAuthorityOverlap(folder, s.protected); err != nil {
		return nil, err
	}
	id, err := identity.FileKey(folder)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute).Truncate(time.Second)
	digest := sha256.Sum256([]byte(s.record.ID + "\x00" + request.RequestKey))
	question := asksclient.ApplicationQuestion{RequestKey: "opencode-" + hex.EncodeToString(digest[:]), Key: "opencode.project.edit",
		Slots: map[string]string{"folder": folder, "code": s.record.ID[:8] + "/" + hex.EncodeToString(digest[:4]), "until": expires.Format(time.RFC3339)}}
	template, _ := asks.Find(question.Key)
	if _, err := template.Render(asks.Ask{Asker: asks.AskerDisplay(s.record.OwnerProgram), Slots: question.Slots}); err != nil {
		return nil, err
	}
	return &openCodeFolderApproval{Request: request, Folder: folder, FileID: id, Access: sandbox.Modify,
		Question: question, Created: now, Expires: expires, Outcome: projectwire.ProjectOutcomePending}, nil
}

// observe verifies attribution through the same Book's operator profile. It
// never answers a question and creates no persistent OA rights permit.
func (s *openCodeNativeSession) observe(ctx context.Context, approval *openCodeFolderApproval, first bool, wait int64) error {
	if !time.Now().Before(approval.Expires) {
		approval.Outcome = projectwire.ProjectOutcomeExpired
		return s.save()
	}
	var observation asksclient.QuestionObservation
	var err error
	if first {
		observation, err = s.questions.AskContext(ctx, approval.Question)
	} else {
		observation, err = s.questions.ObserveContext(ctx, approval.Question.RequestKey, wait)
	}
	if err != nil {
		return err
	}
	if answer := observation.Answer; answer != nil {
		if approval.QuestionID != "" && approval.QuestionID != answer.ID {
			return errors.New("OpenCode: question occurrence changed")
		}
		approval.QuestionID = answer.ID
	}
	switch observation.Outcome {
	case asksclient.ObservationOutcomePending:
		approval.Outcome = projectwire.ProjectOutcomePending
	case asksclient.ObservationOutcomeAnswered:
		attributedRead, err := s.operator.ReadAttributedQuestionContext(ctx, approval.QuestionID)
		read := attributedRead.Question
		if err != nil {
			return err
		}
		if approvedOpenCodeFolder(read, approval.QuestionID, s.record.OwnerProgram, approval.Question, approval.Created, approval.Expires, time.Now().UTC()) {
			approval.Approved, approval.ManualAnswer = true, true
			manual := *read.AnsweredBy
			approval.AnsweredBy, approval.Answered = &manual, read.Record.Answered
			approval.Outcome = projectwire.ProjectOutcomeApprovedPendingClose
			if approval.Receipt == "" {
				approval.Receipt, err = openCodeNonce()
				if err != nil {
					return err
				}
			}
		} else {
			approval.Outcome = projectwire.ProjectOutcomeDenied
		}
	default:
		return fmt.Errorf("OpenCode: folder question refused (%s)", observation.Outcome.String())
	}
	return s.save()
}

func (s *openCodeNativeSession) approveInitial(ctx context.Context, diagnostics io.Writer) error {
	approval := s.record.Initial
	if err := s.save(); err != nil {
		return err
	}
	if approval.Approved && time.Now().Before(approval.Expires) {
		attributedRead, err := s.operator.ReadAttributedQuestionContext(ctx, approval.QuestionID)
		read := attributedRead.Question
		if err != nil {
			return err
		}
		if !approvedOpenCodeFolder(read, approval.QuestionID, s.record.OwnerProgram, approval.Question, approval.Created, approval.Expires, time.Now().UTC()) {
			return errors.New("OpenCode: retained folder answer no longer matches its manual occurrence")
		}
		return nil
	}
	if err := s.observe(ctx, approval, true, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(diagnostics, "Approve question %s in OpenAbstractions Questions to read, run, create, edit and delete files in %s and its subfolders.\n", approval.QuestionID, approval.Folder); err != nil {
		return err
	}
	for approval.Outcome == projectwire.ProjectOutcomePending {
		if err := s.observe(ctx, approval, false, 30000); err != nil {
			return err
		}
	}
	if !approval.Approved || !approval.ManualAnswer {
		return errors.New("OpenCode: the selected folder was not approved; no sandbox job started")
	}
	return nil
}

func (s *openCodeNativeSession) authorize(call *listen.FramedCall) error {
	peer, err := call.Peer()
	if err != nil {
		return err
	}
	user, err := peer.User.AtLeast(identity.ProofKernel)
	if err != nil {
		return err
	}
	process, err := peer.Process.AtLeast(identity.ProofKernel)
	if err != nil {
		return err
	}
	program, err := peer.Path.AtLeast(identity.ProofBound)
	if err != nil {
		return err
	}
	if user.Kind != "windows" || user.SID != s.record.Account || process.Recycled || process.StartTime.IsZero() {
		return errors.New("OpenCode: native account/process instance refused")
	}
	id, err := identity.FileKey(program)
	if err != nil || id != s.installed.ProgramID {
		return errors.Join(errors.New("OpenCode: installed native program identity required"), err)
	}
	s.mu.Lock()
	owned := s.process
	s.mu.Unlock()
	if owned == nil {
		return errors.New("OpenCode: no contained job owns this request")
	}
	inJob, err := owned.Contains(process.PID, process.StartTime)
	if err != nil || !inJob {
		return errors.Join(errors.New("OpenCode: current native job membership required"), err)
	}
	return nil
}

type openCodeProjectCall struct {
	session *openCodeNativeSession
	call    *listen.FramedCall
}

func (c openCodeProjectCall) RequestProject(request projectwire.ProjectRequest) (projectwire.ProjectReceipt, error) {
	s := c.session
	if err := s.authorize(c.call); err != nil {
		return projectwire.ProjectReceipt{}, err
	}
	if !openCodeWord(request.SessionID) || !openCodeWord(request.MessageID) {
		return projectwire.ProjectReceipt{}, errors.New("OpenCode: actual conversation and message identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	approval := s.record.Requests[request.RequestKey]
	if approval != nil && approval.Request != request {
		return projectwire.ProjectReceipt{Outcome: projectwire.ProjectOutcomeConflict, RequestKey: request.RequestKey, Message: "The request key already binds another folder or conversation."}, nil
	}
	first := approval == nil
	if first {
		if len(s.record.Requests) >= 8 {
			return projectwire.ProjectReceipt{Outcome: projectwire.ProjectOutcomeUnavailable, RequestKey: request.RequestKey, Message: "This launch's folder request limit was reached."}, nil
		}
		var err error
		approval, err = s.folder(request)
		if err != nil {
			return projectwire.ProjectReceipt{}, err
		}
		s.record.Requests[request.RequestKey] = approval
		// Admission may commit even when the reply is lost. Save its immutable
		// request key and tuple before sending anything to the Book.
		if err := s.save(); err != nil {
			return projectwire.ProjectReceipt{}, err
		}
	}
	if !time.Now().Before(approval.Expires) {
		approval.Outcome = projectwire.ProjectOutcomeExpired
		if err := s.save(); err != nil {
			return projectwire.ProjectReceipt{}, err
		}
	}
	if first || approval.Outcome == projectwire.ProjectOutcomePending {
		if err := s.observe(c.call.WaitContext(), approval, first, 0); err != nil {
			return projectwire.ProjectReceipt{}, err
		}
	}
	if err := c.call.Recheck(); err != nil {
		return projectwire.ProjectReceipt{}, err
	}
	return s.receipt(approval), nil
}

func (s *openCodeNativeSession) receipt(approval *openCodeFolderApproval) projectwire.ProjectReceipt {
	message := openCodeProjectReceiptMessage(approval.Outcome)
	if approval.Approved && approval.Receipt != "" && !approval.Consumed && time.Now().Before(approval.Expires) {
		message += " After closing OpenCode, run: " + s.resumeCommand + " --resume-request '" + approval.Receipt + "'"
	}
	return projectwire.ProjectReceipt{Outcome: approval.Outcome, RequestKey: approval.Request.RequestKey, Receipt: approval.Receipt, QuestionID: approval.QuestionID, Message: message}
}

func openCodeResumeApproval(record openCodeLaunchRecord, receipt, account, ownerID, stateID, runtimeEndpoint, runtimeID, guestID, rootID string, now time.Time) (*openCodeFolderApproval, error) {
	if record.Format != 1 || record.Phase != "closed" || !record.ClosedJob || record.ChildPID <= 0 || record.ChildStart.IsZero() ||
		record.Account != account || record.OwnerProgramID != ownerID || record.StateID != stateID ||
		record.RuntimeEndpoint != runtimeEndpoint || record.RuntimeProgramID != runtimeID || record.GuestProgramID != guestID || record.PackageRootID != rootID || record.Initial == nil {
		return nil, errors.New("OpenCode: resume requires the same native owner, account, installation, state and runtime after confirmed whole-job exit")
	}
	for _, approval := range record.Requests {
		if approval.Receipt != receipt {
			continue
		}
		if !approval.Approved || !approval.ManualAnswer || approval.Consumed || approval.Completion == nil ||
			!openCodeWord(approval.Request.SessionID) || !validOpenCodeCompletion(*approval.Completion, approval.Request, receipt, approval.Created, approval.Expires, now) {
			return nil, errors.New("OpenCode: resume needs its unexpired unconsumed manual approval and exact committed-tool acknowledgement")
		}
		return approval, nil
	}
	return nil, errors.New("OpenCode: this closed launch has no matching approved receipt")
}

func (s *openCodeNativeSession) checkFolder(approval *openCodeFolderApproval) error {
	if approval.Access != sandbox.Modify || !approval.Approved || !approval.ManualAnswer || !time.Now().Before(approval.Expires) {
		return errors.New("OpenCode: a current manual Modify approval is required before its effect")
	}
	if err := rejectOpenCodeAuthorityOverlap(approval.Folder, s.protected); err != nil {
		return err
	}
	id, err := identity.FileKey(approval.Folder)
	if err != nil || id != approval.FileID {
		return errors.Join(errors.New("OpenCode: approved folder identity changed before its effect"), err)
	}
	attributedRead, err := s.operator.ReadAttributedQuestionContext(s.ctx, approval.QuestionID)
	read := attributedRead.Question
	if err != nil {
		return err
	}
	if !approvedOpenCodeFolder(read, approval.QuestionID, s.record.OwnerProgram, approval.Question, approval.Created, approval.Expires, time.Now().UTC()) {
		return errors.New("OpenCode: the retained human occurrence does not authorize this folder effect")
	}
	return nil
}

func (s *openCodeNativeSession) renewFolder(approval *openCodeFolderApproval, diagnostics io.Writer) error {
	id, err := identity.FileKey(approval.Folder)
	if err != nil || id != approval.FileID {
		return errors.Join(errors.New("OpenCode: the saved project folder identity changed"), err)
	}
	if time.Now().Before(approval.Expires) {
		return s.checkFolder(approval)
	}
	nonce, err := openCodeNonce()
	if err != nil {
		return err
	}
	refreshed, err := s.folder(projectwire.ProjectRequest{RequestKey: "renew-" + nonce, Folder: approval.Folder})
	if err != nil {
		return err
	}
	if refreshed.FileID != approval.FileID {
		return errors.New("OpenCode: the saved project folder identity changed before renewal")
	}
	approval.Question, approval.QuestionID, approval.Created, approval.Expires = refreshed.Question, "", refreshed.Created, refreshed.Expires
	approval.Approved, approval.ManualAnswer, approval.AnsweredBy, approval.Answered = false, false, nil, ""
	approval.Outcome = projectwire.ProjectOutcomePending
	if err := s.save(); err != nil {
		return err
	}
	if err := s.observe(s.ctx, approval, true, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(diagnostics, "Approve renewed folder question %s for %s before reopening.\n", approval.QuestionID, approval.Folder); err != nil {
		return err
	}
	for approval.Outcome == projectwire.ProjectOutcomePending {
		if err := s.observe(s.ctx, approval, false, 30000); err != nil {
			return err
		}
	}
	return s.checkFolder(approval)
}

func (c openCodeProjectCall) AcknowledgeReceipt(completion projectwire.ProjectCompletion) (projectwire.ProjectReceipt, error) {
	if err := c.session.authorize(c.call); err != nil {
		return projectwire.ProjectReceipt{}, err
	}
	s := c.session
	s.mu.Lock()
	defer s.mu.Unlock()
	approval := s.record.Requests[completion.RequestKey]
	if approval == nil || !approval.Approved || !approval.ManualAnswer || approval.Consumed ||
		!validOpenCodeCompletion(completion, approval.Request, approval.Receipt, approval.Created, approval.Expires, time.Now().UTC()) {
		return projectwire.ProjectReceipt{Outcome: projectwire.ProjectOutcomeConflict, RequestKey: completion.RequestKey, Message: "The completed tool does not match an unconsumed manual folder approval."}, nil
	}
	if approval.Completion != nil && *approval.Completion != completion {
		return projectwire.ProjectReceipt{Outcome: projectwire.ProjectOutcomeConflict, RequestKey: completion.RequestKey, Message: "Another completed-tool tuple already binds this receipt."}, nil
	}
	if err := c.call.Recheck(); err != nil {
		return projectwire.ProjectReceipt{}, err
	}
	previousCompletion, previousOutcome := approval.Completion, approval.Outcome
	approval.Completion = &completion
	approval.Outcome = projectwire.ProjectOutcomeReceiptRecorded
	if err := s.save(); err != nil {
		approval.Completion, approval.Outcome = previousCompletion, previousOutcome
		return projectwire.ProjectReceipt{}, err
	}
	// This acknowledgement has no idle claim and performs no OS effect.
	return s.receipt(approval), nil
}

func (s *openCodeNativeSession) listen(endpoint string) (func() error, error) {
	release, err := listen.ConfigureAdmission([]string{endpoint}, []string{s.record.SID})
	if err != nil {
		return nil, err
	}
	restore, err := identity.AdmitServerQueries(s.record.SID)
	if err != nil {
		release()
		return nil, err
	}
	listener, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		release()
		return nil, errors.Join(err, restore())
	}
	listener = listen.Sessions(listener, listen.SessionOptions{MaxSessions: 8})
	ctx, cancel := context.WithCancel(s.ctx)
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 64<<10)
				if err != nil {
					return
				}
				defer call.Close()
				if err := s.authorize(call); err != nil {
					return
				}
				dispatcher := projectwire.SessionProjectDispatcher{Handler: openCodeProjectCall{session: s, call: call}}
				frame, err := dispatcher.ExchangeFrame(call.Frame)
				if err == nil {
					_ = call.Reply(frame)
				}
			}()
		}
	}()
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			cancel()
			closeErr = listener.Close()
			<-acceptDone
			workers.Wait()
			// Every listener and in-flight call is closed before admission ends.
			closeErr = errors.Join(closeErr, restore())
			release()
		})
		return closeErr
	}, nil
}

func openCodeGuestEnvironment(installed *installedOpenCode, data, endpoint, ownerProgram, account, launchID string, runtime bootstrap.Selection) ([]string, error) {
	for _, name := range []string{"config", "data", "state", "cache", "temp", "local", "home"} {
		if err := os.MkdirAll(filepath.Join(data, name), 0700); err != nil {
			return nil, err
		}
	}
	raw, err := os.ReadFile(installed.Config)
	if err != nil {
		return nil, err
	}
	var configuration map[string]any
	if err := json.Unmarshal(raw, &configuration); err != nil {
		return nil, err
	}
	providers, ok := configuration["provider"].(map[string]any)
	if !ok {
		return nil, errors.New("OpenCode: installed OA provider configuration required")
	}
	var ownProviders []string
	for id, value := range providers {
		provider, ok := value.(map[string]any)
		if !ok {
			continue
		}
		npm, _ := provider["npm"].(string)
		if !openCodeCompatibleProvider(npm) {
			continue
		}
		provider["npm"] = openCodeFileURL(filepath.Join(installed.Plugin, "index.js"))
		options, _ := provider["options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
			provider["options"] = options
		}
		options["runtimeEndpoint"] = runtime.Endpoint
		options["server"] = map[string]any{"principalKind": 1, "principal": runtime.Server.Principal.SID, "program": runtime.Server.Program}
		ownProviders = append(ownProviders, id)
	}
	if len(ownProviders) == 0 {
		return nil, errors.New("OpenCode: configured OA provider was not found")
	}
	var plugins []any
	if existing, ok := configuration["plugin"].([]any); ok {
		for _, entry := range existing {
			raw, err := json.Marshal(entry)
			if err != nil {
				return nil, err
			}
			name, _, err := openCodePluginEntry(raw)
			if err != nil {
				return nil, err
			}
			if !openCodeRegisteredAdapter(name, "./server") {
				plugins = append(plugins, entry)
			}
		}
	}
	for _, id := range ownProviders {
		plugins = append(plugins, []any{installed.Plugin, map[string]string{"providerId": id}})
	}
	configuration["plugin"] = plugins
	config := filepath.Join(data, "config", "opencode.json")
	if err := openCodeWriteRecord(config, configuration); err != nil {
		return nil, err
	}
	// The guest installs its plugins' dependencies into its global config folder
	// (the config file's own name) at every start; record the peers the plugin
	// package carries so it does not wait on a registry the sandbox cannot reach.
	if _, err := seedGuestDependencies(strings.TrimSuffix(config, filepath.Ext(config)), pluginPackageRoot(installed.Plugin),
		filepath.Join(installed.Plugin, "package.json")); err != nil {
		return nil, err
	}
	// Keep platform/terminal variables. Credentials and unrelated application
	// settings have no reason to enter this selected project's guest.
	var env []string
	for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC", "PATH", "PATHEXT", "TERM", "COLORTERM", "WT_SESSION", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "XDG_CONFIG_HOME="+filepath.Join(data, "config"), "XDG_DATA_HOME="+filepath.Join(data, "data"),
		"XDG_STATE_HOME="+filepath.Join(data, "state"), "XDG_CACHE_HOME="+filepath.Join(data, "cache"),
		"LOCALAPPDATA="+filepath.Join(data, "local"), "USERPROFILE="+filepath.Join(data, "home"), "HOME="+filepath.Join(data, "home"),
		"TEMP="+filepath.Join(data, "temp"), "TMP="+filepath.Join(data, "temp"), "OPENCODE_CONFIG="+config,
		"OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"SHELL="+filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		"OA_OPENCODE_SESSION_ENDPOINT="+endpoint, "OA_OPENCODE_SESSION_PROGRAM="+ownerProgram,
		"OA_OPENCODE_SESSION_ACCOUNT="+account, "OA_OPENCODE_SESSION_ID="+launchID,
		"OA_OPENCODE_RUNTIME_PROGRAM="+runtime.Server.Program, "OA_OPENCODE_RUNTIME_ACCOUNT="+runtime.Server.Principal.SID)
	return env, nil
}

// sandboxInstallation is the official program, plugin and config the person
// or the adopter's launcher named. Discovery of the official installer's paths
// belongs to the adopter (adopters/<app>/).
func sandboxInstallation(options openCodeSessionOptions, diagnostics io.Writer) (*installedOpenCode, error) {
	given, err := checkGivenInstall(options.program, options.plugin, options.config, os.Stat)
	if err != nil {
		return nil, err
	}
	installed := &installedOpenCode{Program: identity.CanonicalProgramPath(given.Program),
		Plugin: identity.CanonicalProgramPath(given.Plugin), Config: identity.CanonicalProgramPath(given.Config)}
	installed.Root = filepath.Dir(installed.Program)
	for _, path := range []string{installed.Program, installed.Plugin, installed.Config} {
		if identity.LocalPath(path) != nil {
			return nil, errors.New("sandbox: local official installation paths required")
		}
	}
	if err := enginefiles.CheckPlainPath(installed.Program); err != nil {
		return nil, err
	}
	// The AppContainer profile the runtime's endpoint admission
	// (configureRuntimeAppSandbox) already covers.
	if installed.Profile, err = sandbox.ApplicationProfileName("opencode", ""); err != nil {
		return nil, err
	}
	if installed.SID, err = sandbox.ProfileSID(installed.Profile); err != nil {
		return nil, err
	}
	if installed.ProgramID, err = identity.FileKey(installed.Program); err != nil {
		return nil, err
	}
	if installed.RootID, err = identity.FileKey(installed.Root); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(diagnostics, "Starting the official program %s\n", installed.Program); err != nil {
		return nil, err
	}
	return installed, nil
}

func startOpenCodeSession(options openCodeSessionOptions, output, diagnostics io.Writer) (result error) {
	ownerProgram, err := os.Executable()
	if err != nil {
		return err
	}
	ownerProgram = identity.CanonicalProgramPath(ownerProgram)
	if options.newWindow {
		args := []string{"opencode", "open"}
		for _, pair := range [][2]string{{"--project", options.project}, {"--session", options.conversation}, {"--resume-request", options.resumeRequest}, {"--state-dir", options.state}, {"--endpoint", options.endpoint}, {"--runtime-program", options.runtimeProgram}, {"--program", options.program}, {"--plugin", options.plugin}, {"--config", options.config}} {
			if pair[1] != "" {
				args = append(args, pair[0], pair[1])
			}
		}
		return launchThroughShellVisible(ownerProgram, filepath.Dir(ownerProgram), args...)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	self, err := identity.Self()
	if err != nil {
		return err
	}
	selection, err := openCodeRuntime(ctx, options, self)
	if err != nil {
		return err
	}
	state := options.state
	if state == "" {
		state, err = runtimeStateDir("windows", os.Getenv, os.UserHomeDir)
		if err != nil {
			return err
		}
	}
	installed, err := sandboxInstallation(options, diagnostics)
	if err != nil {
		return err
	}
	authority := openCodeAuthorityDir(state)
	if err := privatedir.Ensure(authority); err != nil {
		return err
	}
	lock, err := openCodeSessionLock(authority)
	if err != nil {
		return fmt.Errorf("OpenCode session is active or its control lock is unavailable: %w", err)
	}
	defer windows.CloseHandle(lock)
	manifest := filepath.Join(authority, "sandbox.json")
	if _, err := sandbox.ReadManifest(manifest); !errors.Is(err, sandbox.ErrNotInstalled) {
		return errors.Join(errors.New("OpenCode: retained sandbox effects require explicit recovery; automatic replay is refused"), err)
	}
	recordPath := filepath.Join(authority, "session.json")
	var prior *openCodeLaunchRecord
	if raw, err := readOpenCodeControlFile(recordPath, 1<<20); err == nil {
		prior = &openCodeLaunchRecord{}
		if err := json.Unmarshal(raw, prior); err != nil || prior.Format != 1 || (prior.Phase != "finished" && prior.Phase != "closed") {
			return errors.New("OpenCode: previous launch has an ambiguous effect; retain its control state for explicit recovery")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := openCodeNonce()
	if err != nil {
		return err
	}
	ownerID, err := identity.FileKey(ownerProgram)
	if err != nil {
		return err
	}
	stateID, err := identity.FileKey(authority)
	if err != nil {
		return err
	}
	runtimeID, err := identity.FileKey(selection.Server.Program)
	if err != nil {
		return err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return err
	}
	machine := client.NewVerified(selection.Endpoint, selection.Server)
	questions, err := machine.ResolveAsks(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return err
	}
	questions, err = questions.WithTimeout(35 * time.Second)
	if err != nil {
		return err
	}
	operator, err := machine.ResolveAsksOperator(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return err
	}
	s := &openCodeNativeSession{ctx: ctx, installed: installed, recordPath: recordPath, questions: questions, operator: operator,
		protected: []string{installed.Root, pluginPackageRoot(installed.Plugin), installed.Config, authority, ownerProgram, selection.Server.Program}, record: openCodeLaunchRecord{Format: 1, ID: id, Account: self.SID,
			OwnerProgram: ownerProgram, OwnerProgramID: ownerID, OwnerPID: os.Getpid(), OwnerStart: time.Unix(0, created.Nanoseconds()).UTC(),
			Profile: installed.Profile, SID: installed.SID, StateID: stateID, RuntimeEndpoint: selection.Endpoint, RuntimeProgramID: runtimeID, GuestProgramID: installed.ProgramID, PackageRootID: installed.RootID,
			Phase: "approval", Requests: map[string]*openCodeFolderApproval{}}}
	quote := powershell.Literal
	s.resumeCommand = "& " + quote(ownerProgram) + " opencode open --state-dir " + quote(state) + " --endpoint " + quote(selection.Endpoint) + " --runtime-program " + quote(selection.Server.Program)
	// The official install is named again; the resume checks the same identities.
	s.resumeCommand += " --program " + quote(options.program) + " --plugin " + quote(options.plugin) + " --config " + quote(options.config)
	var resumed *openCodeFolderApproval
	if options.resumeRequest != "" {
		if prior == nil {
			return errors.New("OpenCode: there is no retained closed launch to resume")
		}
		resumed, err = openCodeResumeApproval(*prior, options.resumeRequest, self.SID, ownerID, stateID, selection.Endpoint, runtimeID, installed.ProgramID, installed.RootID, time.Now().UTC())
		if err != nil {
			return err
		}
		s.record = *prior
		s.record.OwnerPID, s.record.OwnerStart = os.Getpid(), time.Unix(0, created.Nanoseconds()).UTC()
		options.project, options.conversation = s.record.Initial.Folder, resumed.Request.SessionID
		if err := s.checkFolder(resumed); err != nil {
			return err
		}
		if err := s.renewFolder(s.record.Initial, diagnostics); err != nil {
			return err
		}
		for _, approval := range s.record.Requests {
			if approval.Consumed {
				if err := s.renewFolder(approval, diagnostics); err != nil {
					return err
				}
			}
		}
		s.record.ClosedJob, s.record.ChildPID, s.record.ChildStart, s.record.Phase = false, 0, time.Time{}, "approval"
	} else {
		// There is no OpenCode conversation yet. The initial request belongs to the
		// launch record; real guest SessionID/MessageID are required on later calls.
		s.record.Initial, err = s.folder(projectwire.ProjectRequest{RequestKey: "initial", Folder: options.project})
		if err != nil {
			return err
		}
		if err := s.approveInitial(ctx, diagnostics); err != nil {
			s.record.Phase, s.record.Failure = "finished", err.Error()
			return errors.Join(err, s.save())
		}
	}
	data := filepath.Join(state, "opencode-data")
	if err := privatedir.Ensure(data); err != nil {
		return err
	}
	endpoint := `\\.\pipe\oa-opencode-session-` + id
	env, err := openCodeGuestEnvironment(installed, data, endpoint, ownerProgram, self.SID, id, selection)
	if err != nil {
		return err
	}
	dataID, err := identity.FileKey(data)
	if err != nil {
		return err
	}
	grantedFolders := []*openCodeFolderApproval{s.record.Initial}
	for _, approval := range s.record.Requests {
		if approval.Consumed || approval == resumed {
			grantedFolders = append(grantedFolders, approval)
		}
	}
	for _, approval := range grantedFolders {
		if err := s.checkFolder(approval); err != nil {
			return err
		}
	}
	if resumed != nil {
		resumed.Consumed = true
	}
	s.record.Phase = "applying"
	if err := s.save(); err != nil {
		return err
	}
	setup, err := sandbox.Begin(manifest, installed.Profile)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_, removeErr := sandbox.Remove(manifest, false)
			result = errors.Join(result, removeErr)
			if removeErr == nil {
				s.record.Phase = "finished"
				if s.record.ClosedJob {
					s.record.Phase = "closed"
				}
				if result != nil {
					s.record.Failure = result.Error()
				}
				result = errors.Join(result, s.save())
			}
		}
	}()
	grantErr := setup.GrantBound(sandbox.Grant{Path: installed.Root, Access: sandbox.ReadExecute}, installed.RootID)
	// An official install keeps the plugin in the application's own cache, outside
	// the program's folder; the guest reads it there.
	pluginRoot := pluginPackageRoot(installed.Plugin)
	pluginInside, pluginErr := identity.Within(pluginRoot, installed.Root)
	if grantErr == nil && pluginErr != nil {
		grantErr = pluginErr
	}
	if grantErr == nil && !pluginInside {
		var pluginID string
		if pluginID, grantErr = identity.FileKey(pluginRoot); grantErr == nil {
			// Read-execute plus write-attributes: Bun 1.3.14 opens every module it
			// imports with FILE_WRITE_ATTRIBUTES (live run 2026-10-06). TEMPORARY
			// EXCEPTION, with the root attributes: TODO.md "Temporary exception:
			// sandbox root attributes".
			grantErr = setup.GrantBound(sandbox.Grant{Path: pluginRoot, Access: sandbox.ReadExecuteAttributes}, pluginID)
		}
	}
	if grantErr == nil {
		grantErr = setup.GrantBound(sandbox.Grant{Path: data, Access: sandbox.Modify}, dataID)
	}
	for _, approval := range grantedFolders {
		if grantErr == nil {
			grantErr = setup.GrantBound(sandbox.Grant{Path: approval.Folder, Access: sandbox.Modify}, approval.FileID)
		}
	}
	// The guest resolves its folders by reading every ancestor's attributes
	// from the drive root down (KIT-S7). The account's own ancestors are
	// granted for this session and recorded in the manifest Remove reads; the
	// drive root and the profiles folder carry the machine-wide installation's
	// entry for every OA sandbox. TEMPORARY EXCEPTION: TODO.md "Temporary
	// exception: sandbox root attributes".
	if grantErr == nil {
		var ancestors *sandbox.Ancestors
		if ancestors, grantErr = setup.GrantAncestorAttributes(); grantErr == nil && len(ancestors.Unreached) > 0 {
			_, grantErr = fmt.Fprintf(diagnostics, "The sandbox cannot read the attributes of %s. Only the machine-wide OpenAbstractions installation adds that entry; without it the program may stop at start.\n",
				strings.Join(ancestors.Unreached, ", "))
		}
	}
	if grantErr == nil {
		grantErr = setup.Commit()
	}
	setup.Close()
	if grantErr != nil {
		return grantErr
	}
	needs := []sandbox.Grant{{Path: installed.Program, Access: sandbox.ReadExecute}, {Path: data, Access: sandbox.Modify}}
	if !pluginInside {
		needs = append(needs, sandbox.Grant{Path: pluginRoot, Access: sandbox.ReadExecuteAttributes})
	}
	for _, approval := range grantedFolders {
		needs = append(needs, sandbox.Grant{Path: approval.Folder, Access: sandbox.Modify})
	}
	launch, err := sandbox.Prepare(manifest, needs)
	if err != nil {
		return err
	}
	defer launch.Close()
	closeEndpoint, err := s.listen(endpoint)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeEndpoint()) }()
	args := []string{s.record.Initial.Folder}
	if options.conversation != "" {
		args = append(args, "--session", options.conversation)
	}
	process, err := supervision.Start(ctx, supervision.Options{Executable: installed.Program, Args: args, Env: env, Dir: s.record.Initial.Folder,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Container: launch.SID, InheritConsole: true, ForceTimeout: 5 * time.Second})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.process = process
	s.record.ChildPID, s.record.ChildStart, s.record.Phase = process.PID(), process.StartTime(), "running"
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		_ = process.Stop()
	}
	code, waitErr := process.Wait()
	exit, known := process.Exit()
	if waitErr != nil || !known || exit.Unknown != "" {
		cleanup = false
		s.mu.Lock()
		s.record.Phase, s.record.Failure = "exit-unknown", fmt.Sprint(errors.Join(err, waitErr))
		saveErr := s.save()
		s.mu.Unlock()
		return errors.Join(errors.New("OpenCode: whole-job exit is unknown; grants and recovery state retained"), err, waitErr, saveErr)
	}
	s.mu.Lock()
	s.record.ClosedJob = true
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if code != 0 && !exit.Stopped {
		return fmt.Errorf("the sandboxed program exited with %s", exit.Status())
	}
	_, err = fmt.Fprintln(output, "The sandbox session exited; its recorded file grants are being restored.")
	return err
}
