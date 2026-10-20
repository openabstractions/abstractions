package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// The setup step (research/first-run/DECISION.md §4): one native question,
// then work that reports through notifications and resumes from journals.
// Which registered app gets which use, and the engine its pictures run in,
// come from Setup's catalog (Catalog.StepChoices) through StepApp; every
// question and notice names the apps by their registered titles. Step owns
// the order and the per-account step journal; every effect goes through an
// existing owner bound in StepOwners.

// The uses the step sets up for an integrated app: a coding model as the
// chat default, or pictures through a local picture engine. They are the
// use words of applications@3 RequestSetup.
const (
	UseCode  = "code"
	UseImage = "image"
)

// ChatHost is the inference host the coding model Setup includes runs on:
// llama.cpp's model host, the declaration and router host product.ModelHost.
// The chat permission an answer grants names it.
const ChatHost = product.ModelHost

// stepPhase is where the step stands for this account.
type stepPhase = string

const (
	// stepNew: no question was answered yet.
	stepNew stepPhase = ""
	// stepDeclined: the person chose Not now. Nothing else was written.
	stepDeclined stepPhase = "declined"
	// stepApproved: the person chose Set up; the work resumes from here.
	stepApproved stepPhase = "approved"
	// stepReady: the model is in place and the chat default selects it.
	stepReady stepPhase = "ready"
)

// StepState is the per-account step journal, beside Setup's other journals.
// Progress is "", "declined", "approved" or "ready".
type StepState struct {
	Progress stepPhase `json:"phase"`
	Use      string    `json:"use,omitempty"`
	// The approved pick, as the question showed it.
	Title          string `json:"title,omitempty"`
	Family         string `json:"family,omitempty"`
	Served         bool   `json:"served,omitempty"`
	CatalogVersion string `json:"catalog_version,omitempty"`
	// ModelPlanID is the reviewed download plan the answer approved; empty
	// when the pick was already served.
	ModelPlanID string   `json:"model_plan_id,omitempty"`
	Apps        []string `json:"apps,omitempty"`
	Hosted      bool     `json:"hosted,omitempty"`
	// Adopted is the copy already on this computer the answer adopted, in
	// place of the download; nil when the model was downloaded or served.
	Adopted  *StepLocal `json:"adopted,omitempty"`
	Selected bool       `json:"selected,omitempty"`
	// RemovedFromOA records the person's Remove from OA of this model (the
	// Models page writes it, RecordRemoval). Setup's Finish then leaves the
	// model removed; hosting it again clears the record.
	RemovedFromOA bool `json:"removed_from_oa,omitempty"`
	// ChatGrants are the chat permissions the answer covered (owner,
	// 2026-11-01): for each app it set up, the program and rules the
	// question stated, and whether they were written.
	ChatGrants []StepChatGrant `json:"chat_grants,omitempty"`
	// Announced is the job operation the download's start notice named, so
	// a resumed job posts nothing new and a new download posts its own; a
	// notice posted before the job had an operation records "plan:" and the
	// plan. Its key is new: slice 1 journals hold "announced": true, a
	// boolean, which a reader of this field ignores.
	Announced string `json:"announced_operation,omitempty"`
	// Pictures records that the answer included the picture part, and for
	// exactly which offer: the picture-model plan, Setup's engine package
	// plan, the rules' digest and the program they name.
	Pictures        bool   `json:"pictures,omitempty"`
	PicturePlanID   string `json:"picture_plan_id,omitempty"`
	PicturePackages string `json:"picture_packages,omitempty"`
	PictureRules    string `json:"picture_rules,omitempty"`
	PictureProgram  string `json:"picture_program,omitempty"`
	// Granted records that the picture rules were written once.
	Granted bool `json:"granted,omitempty"`
	// PicturesStopped records a picture part that stopped while the chat
	// part was set up: the step reads Ready for chat, and Settings offers to
	// try pictures again.
	PicturesStopped bool   `json:"pictures_stopped,omitempty"`
	At              string `json:"at,omitempty"`
	// RequestDeclinedAt retains an earlier Panel journal member for decoding.
	// The runtime owns the current request cooldown; this field decides nothing.
	RequestDeclinedAt string `json:"request_declined_at,omitempty"`
	// Stopped is why the last run stopped, kept until a later run asks or
	// finishes. It leaves the phase as it was.
	Stopped *StepStopped `json:"stopped,omitempty"`
	// Build is the Panel build (PanelBuild) whose run last worked on the
	// approved work. Sign-in resumes approved work only for that build: an
	// update stops it until the person's Set up.
	Build string `json:"build,omitempty"`
}

// StepChatGrant is one app's chat permission an answer covered: the app's
// registered name and program, and the digest of the rules (RulesDigest).
// Granted records that the rules are settled: written by Setup, or already
// held by a rule of the person's own, which Setup leaves as it is. Written
// lists the rules Setup itself wrote, the only ones it may remove later.
type StepChatGrant struct {
	App     string         `json:"app"`
	Program string         `json:"program"`
	Rules   string         `json:"rules"`
	Granted bool           `json:"granted,omitempty"`
	Written []ApprovedRule `json:"written,omitempty"`
}

// StepChat binds the chat permission (owner, 2026-11-01: "yes setup should
// also grant model that we included"): the one answered question grants
// each app the step sets up the use of the coding model Setup included for
// it, and nothing beyond it. Nil grants nothing; each app then asks at
// first use.
type StepChat struct {
	// Offer reads the rules app gets for the pick and the sentence that
	// states them; Reason says why app gets none here.
	Offer func(ctx context.Context, app StepApp, pick StepPick) (StepChatOffer, error)
	// Grant settles offer.Rules for app's confirmed registration of
	// offer.Program: it writes each rule no rule holds yet and leaves any
	// existing rule, permit or deny, as the person set it. previous are the
	// rules Setup wrote for the same answer before (StepChatGrant.Written);
	// one the offer no longer names is removed while it is still Setup's.
	// written are the rules Setup holds now. settled false: the registration
	// is not confirmed yet, and a later run settles the rules.
	Grant func(ctx context.Context, offer StepChatOffer, previous []ApprovedRule) (written []ApprovedRule, settled bool, err error)
}

// StepChatOffer is one app's chat permission as Offer read it.
type StepChatOffer struct {
	App     string
	Program string
	Rules   []ApprovedRule
	// Permission is the one sentence the question states: the app's catalogue
	// title, "can use", the model's title and "on this computer".
	Permission string
	// Reason says why the app gets no permission here; empty when it does.
	Reason string
}

// grant is the journal's record of offer before it is written.
func (o StepChatOffer) grant() StepChatGrant {
	return StepChatGrant{App: o.App, Program: o.Program, Rules: RulesDigest(o.Rules)}
}

// StepStopped is one stopped run: the stage it stopped at, the sentence its
// notice gave and the error behind it. Detail is for diagnosis; no
// question, notice or page shows it.
type StepStopped struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
}

// The stages a run stops at. Each has its own sentence (stoppedReason).
const (
	stageJournal  = "journal"
	stageApps     = "apps"
	stageFit      = "fit"
	stagePlan     = "plan"
	stageQuestion = "question"
	stageAnswer   = "answer"
	stageConfirm  = "confirm"
	stageDownload = "download"
	stageHost     = "host"
	stageAdopt    = "adopt"
	stageSelect   = "select"
	stagePictures = "pictures"
	stageGrant    = "grant"
	stageUpdate   = "update"
)

// stepStageError is an owner's error with the stage it stopped the run at.
type stepStageError struct {
	stage string
	err   error
}

func (e stepStageError) Error() string { return e.stage + ": " + e.err.Error() }
func (e stepStageError) Unwrap() error { return e.err }

// atStage marks err with stage, keeping a stage an inner call already set.
func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var marked stepStageError
	if errors.As(err, &marked) {
		return err
	}
	return stepStageError{stage: stage, err: err}
}

// stageOf is the stage err stopped the run at; "" when no owner marked it.
func stageOf(err error) string {
	var marked stepStageError
	switch {
	case errors.As(err, &marked):
		return marked.stage
	case errors.Is(err, errStepPictures):
		return stagePictures
	case errors.Is(err, errStepDownload):
		return stageDownload
	}
	return ""
}

// stoppedReason is the sentence a stopped stage gives the person.
func stoppedReason(stage string) string {
	switch stage {
	case stageJournal:
		return "Setup could not read its record of this step."
	case stageApps:
		return "OpenAbstractions could not read which apps are installed."
	case stageFit:
		return "OpenAbstractions cannot tell now which model fits this computer."
	case stagePlan:
		return "This Setup has no reviewed download for the model that fits this computer."
	case stageQuestion:
		return "The setup question could not be shown."
	case stageAnswer:
		return "Setup could not save your answer."
	case stageConfirm:
		return "OpenAbstractions could not ask whether to keep the app."
	case stageDownload:
		return "The model download stopped before it finished."
	case stageHost:
		return "The downloaded model could not be prepared to run."
	case stageAdopt:
		return "The model already on this computer could not be used."
	case stageSelect:
		return "The model could not be made the default chat model."
	case stagePictures:
		return "Pictures could not be set up."
	case stageGrant:
		return "The app could not be given the use of the model."
	case stageUpdate:
		return "Catalog stopped when OpenAbstractions updated."
	case stageVideo:
		return "Video with sound could not be set up."
	}
	return "Setup could not finish."
}

// Ready reports a finished step; Approved approved work still running;
// Declined the person's Not now.
func (s StepState) Ready() bool    { return s.Progress == stepReady }
func (s StepState) Approved() bool { return s.Progress == stepApproved }
func (s StepState) Declined() bool { return s.Progress == stepDeclined }

// StepPick is the catalogue's first entry that fits this computer or is
// already served, as catalog@1 Fit answered it.
type StepPick struct {
	Title          string
	Family         string
	Machine        string
	Served         bool
	DownloadBytes  int64
	NeedBytes      int64
	License        string
	Digest         string
	CatalogVersion string
	// Context is the catalogue's context for the use, in tokens: the
	// window Fit sized the pick's memory for (MODEL-K12).
	Context int64
	// Local is the pinned file already on this computer (Owners.Local);
	// Setup adopts it and downloads nothing.
	Local *StepLocal
	// Untrusted: a copy is on this computer, in a place other accounts can
	// change, so Setup downloads its own and Show details says why.
	Untrusted bool
	// Hosted: Fit read the pick as served because ModelHost hosts the model
	// the journal's answer set up. It stays Setup's own model; the question
	// says nothing needs downloading.
	Hosted bool
}

// StepHosting is what ModelHost's declaration records for the pick: Name is
// the name apps ask for, the catalogue's family, so the router lists the
// model under it and the chat default Setup writes selects it; Context is
// the window the engine is started with, the catalogue's context for the
// use. A file's own name never names the model: Setup stores its download
// under the file's SHA-256, which is no name for a person.
type StepHosting struct {
	Name    string
	Context int64
}

// Hosting is the pick's hosting record.
func (p StepPick) Hosting() StepHosting {
	return StepHosting{Name: p.Family, Context: p.Context}
}

// StepFit is Fit's answer reduced to what the step decides on. Pick is nil
// when no entry fits; Smallest then names the least the use needs.
type StepFit struct {
	Pick          *StepPick
	PoolBytes     int64
	SmallestNeed  int64
	SmallestTitle string
}

// StepQuestion is the one native question.
type StepQuestion struct {
	Title       string
	Instruction string
	Details     string
	Facts       map[string]string
	renderErr   error
}

// StepNotice is one notification the step posts through notification@1.
type StepNotice struct {
	Title string
	Body  string
	// Tag replaces an earlier notice with the same tag.
	Tag string
	// Operation binds the notice to the public job operation it announces.
	Operation string
}

// StepApp is one integrated app the bundle installed, read through
// Applications, with the use and engine its catalog entry gives it.
// Unconfirmed is a registration the person has not confirmed yet.
type StepApp struct {
	Name        string
	Title       string
	Unconfirmed bool
	Revision    string
	// Program is the registered program; the picture rules name it. No
	// question or notice shows it.
	Program string
	// Use is UseCode or UseImage; Engine is the picture engine choice of a
	// UseImage app.
	Use    string
	Engine AppID
	// Manifest is the application manifest an app's OpenAbstractions Setup
	// package installed, for an app that is installed but not registered
	// yet: its registration waits in Apps as Not connected. The answer
	// registers it from this manifest (Confirm), and the picture part waits
	// for the registration its rules name.
	Manifest string
}

// StepPictures binds the picture part (slice 2): the app whose use is
// UseImage makes pictures through the reviewed picture plan on its engine.
// Each function is the existing owner One Install drives. Every function
// honours ctx and is idempotent; only Offer runs before the person's
// answer. The part is offered only while Applications lists such an app
// whose engine is the reviewed plan's (ModelPlan.PictureEngine).
type StepPictures struct {
	// Offer reads what the part needs on this computer for app.
	Offer func(ctx context.Context, app StepApp) (StepPictureOffer, error)
	// Model is the picture-model controller: download journal and public
	// jobs for the reviewed picture plan.
	Model StepModel
	// Engine makes one transition of the engine package and its feature
	// through Setup's runner and answers true once both are installed.
	Engine func(ctx context.Context, offer StepPictureOffer) (bool, error)
	// Configure prepares the verified picture model in the engine.
	Configure func(ctx context.Context) error
	// Grant writes exactly offer.Rules for the app's confirmed program.
	Grant func(ctx context.Context, offer StepPictureOffer) error
}

// StepPictureOffer is the picture part on this computer, as Offer read it.
type StepPictureOffer struct {
	// Reason says why the part is not offered here; empty when it is.
	Reason string
	// Title names the picture model in sentences.
	Title string
	// App is the registered app the rules are written for; Engine is its
	// picture engine choice.
	App    string
	Engine AppID
	// PlanID is Setup's package plan for the engine, Packages its choices.
	PlanID   string
	Packages []AppID
	// PackageBytes and PackagePeakBytes are what the engine packages still
	// download and need on disk; PackageLicenses are their licences.
	PackageBytes     int64
	PackagePeakBytes int64
	PackageLicenses  []string
	// EngineReady: the packages and the feature are installed. Configured:
	// the engine holds the reviewed picture model.
	EngineReady bool
	Configured  bool
	// Rules are what the app gets; Permissions words each as a sentence.
	Rules       []ApprovedRule
	Permissions []string
	// Program is the app's registered program the rules name.
	Program string
	// Fallback is the one sentence Show details gives when this computer's
	// card fits a faster engine the step cannot use yet
	// (Runner.StepPictureEngine).
	Fallback string
}

// stepPictureWork is the offered picture part with its reviewed plan.
type stepPictureWork struct {
	app   StepApp
	offer StepPictureOffer
	plan  ModelPlan
}

// StepOwners binds the step to the owners it reuses. Every function honours
// ctx. None is called before the person's answer except Fit, Apps and the
// already-set-up check.
type StepOwners struct {
	// Fit reads abstraction.model/catalog@1 Fit for the use.
	Fit func(ctx context.Context, use string) (StepFit, error)
	// Apps reads the registered integrated apps the step serves, each with
	// its catalog use.
	Apps func(ctx context.Context) ([]StepApp, error)
	// Ask shows the one native question and answers true for Set up.
	Ask func(ctx context.Context, q StepQuestion) (bool, error)
	// Notify posts or replaces one notification.
	Notify func(ctx context.Context, n StepNotice) error
	// Confirm registers each named app that is installed but not registered
	// (StepApp.Manifest) through the Applications owner, as its installer's
	// request does; the runtime's reconciler then turns sign-in start on. An
	// app that added itself is kept by the runtime on the person's answer;
	// Confirm returns once those Keeps are written, and Ready waits for it.
	// The step writes no sign-in entry itself.
	Confirm func(ctx context.Context, apps []StepApp) error
	// Model is the existing coding-model controller (download journal and
	// public jobs).
	Model StepModel
	// Local asks the model inventory for the reviewed plan's pinned file
	// already on this computer (storage inventory Find by its SHA-256, size
	// checked), and whether a file of the pinned size is still being hashed;
	// nil, or an answer with neither, plans the download.
	Local func(ctx context.Context, plan ModelPlan) (StepLookup, error)
	// Adopt hosts that inventory object where it lies (ModelHost), in place
	// of the download and of Host, under hosting's name and context. It is
	// idempotent.
	Adopt func(ctx context.Context, local StepLocal, hosting StepHosting) error
	// Host stages the engine, registers the model folder and hosts the
	// downloaded model through the existing ModelHost operations, under
	// hosting's name and context. It is idempotent.
	Host func(ctx context.Context, hosting StepHosting) error
	// Hosting reports whether ModelHost hosts the step's model now: the
	// adopted copy, or the downloaded one. The person may have removed it
	// from OA since (the Models page's Remove from OA), so the journal's
	// own record of hosting proves nothing. Nil trusts the journal.
	Hosting func(ctx context.Context, state StepState) (bool, error)
	// Named reports whether ModelHost hosts the step's model under the
	// catalogue's family (state.Family), the name apps and the chat default
	// ask for. A model hosted under another name, such as the digest an
	// earlier Setup's file name gave it, is hosted again under the family.
	// A model ModelHost does not host reads as named: Hosting decides that.
	// Nil trusts the journal.
	Named func(ctx context.Context, state StepState) (bool, error)
	// Selected reports whether the chat account default already selects
	// family first; Select writes it through selection-operator@2.
	Selected func(ctx context.Context, family string) (bool, error)
	Select   func(ctx context.Context, family string) error
	// Pictures is the picture part; nil leaves the step to chat alone.
	Pictures *StepPictures
	// Video is the video part an app's request for use video sets up; nil
	// answers such a request with one sentence and no question.
	Video *StepVideo
	// Chat grants each app the use of the coding model the answer set up;
	// nil grants nothing.
	Chat *StepChat
	// Failure words an outcome the owners know (Fit unavailable or refused,
	// the Keep question unavailable, the question closed by a lock) as one
	// notice for the person about the apps titled who (empty when no app was
	// read); false leaves Setup's own wording.
	Failure func(err error, who string) (StepNotice, bool)
	// Record writes a stopped run to OA logging under the apps titled who;
	// nil records it in the journal alone.
	Record func(ctx context.Context, stopped StepStopped, who string) error
	// Wait sleeps between download observations; nil uses five seconds.
	Wait func(ctx context.Context) error
	Now  func() time.Time
}

// StepModel is the part of ModelController the step drives.
type StepModel interface {
	ReviewedPlan() ModelPlan
	StartAccepted(ctx context.Context, approval ModelApproval) (ModelStatus, error)
	Status(ctx context.Context) (ModelStatus, error)
	Advance(ctx context.Context, assetID string) (ModelStatus, error)
}

// ReviewedPlan is the controller's reviewed plan, for the step.
func (c ModelController) ReviewedPlan() ModelPlan { return c.plan() }

// stepOutcome is how one run of the step ended.
type stepOutcome string

const (
	stepSetUp          stepOutcome = "ready"
	stepAlreadySetUp   stepOutcome = "already_set_up"
	stepAnsweredNotNow stepOutcome = "declined"
	stepCannotRun      stepOutcome = "cannot_run"
	stepNoApp          stepOutcome = "no_app"
	// stepStoppedByUpdate: the installer's Finish found approved work and
	// left it stopped for the person's Set up.
	stepStoppedByUpdate stepOutcome = "stopped_by_update"
)

// SetupStep is one account's setup step.
type SetupStep struct {
	JournalPath string
	Owners      StepOwners
	// Requester is the app whose applications@3 RequestSetup started this
	// run, as the runtime handed it to the presenter; nil for the finish
	// page, Settings and sign-in.
	Requester *StepRequester
	// Finish is a run Setup's Finish started: when there is nothing to ask
	// or resume (already set up, or no app to set up) it posts no notice,
	// and its caller opens the Panel instead.
	Finish bool
	// Update is the installer's explicit upgrade fact. Older bundles omit it;
	// their recorded removal remains the fallback for an update's Finish.
	Update bool
}

// NothingToSetUp reports a run that found nothing to ask or resume.
func (o stepOutcome) NothingToSetUp() bool { return o == stepAlreadySetUp || o == stepNoApp }

// FinishOpensPanel reports whether Setup's Finish opens the Panel after this
// run: always, unless the person answered the question or approved work ran.
// A run that stopped has posted its notice, and the person who pressed
// Finish still gets OpenAbstractions on screen.
func (o stepOutcome) FinishOpensPanel(err error) bool {
	return err != nil || (o != stepSetUp && o != stepAnsweredNotNow)
}

// StepRequester is an app that asked for the step: its display name and the
// use it asked for. It changes no work; the question names it.
type StepRequester struct {
	App     string
	Use     string
	Request string
}

// sentence gives the Setup question's heading for the requesting app and use.
func (r StepRequester) sentence() string {
	return asks.SetupRequestSentence(r.App, r.Use)
}

// DefaultStepJournal is the step journal beside Setup's other journals.
func DefaultStepJournal() (string, error) {
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Config, "step.json"), nil
}

const maxStepBytes = 8192

// Load reads the step journal; a missing journal is stepNew.
func (s SetupStep) Load(ctx context.Context) (StepState, error) {
	raw, err := cas.ReadLimitContext(ctx, s.JournalPath, maxStepBytes)
	if err != nil || raw == nil {
		return StepState{}, err
	}
	var state StepState
	if err := json.Unmarshal(raw, &state); err != nil {
		return StepState{}, err
	}
	return state, nil
}

func (s SetupStep) save(ctx context.Context, change func(*StepState) error) (StepState, error) {
	if s.JournalPath == "" {
		return StepState{}, errors.New("setup step journal path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.JournalPath), 0o700); err != nil {
		return StepState{}, err
	}
	var out StepState
	err := cas.ChangeLimitContext(ctx, s.JournalPath, maxStepBytes, func(raw []byte) ([]byte, error) {
		var state StepState
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
		}
		if err := change(&state); err != nil {
			return nil, err
		}
		out = state
		return json.Marshal(state)
	})
	return out, err
}

func (s SetupStep) now() string {
	if s.Owners.Now != nil {
		return s.Owners.Now().UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// HostedModel reports whether the step hosted its coding model: the step
// finished, or approved work hosted the model and still prepares pictures.
func (s StepState) HostedModel() bool {
	return s.Family != "" && !s.Served && (s.Ready() || s.Approved() && s.Hosted)
}

// RecordRemoval records the person's Remove from OA of the step's hosted
// model. The caller checked that the object holds that model; a journal
// whose step hosted no model is left as it is.
func (s SetupStep) RecordRemoval(ctx context.Context) error {
	state, err := s.Load(ctx)
	if err != nil || !state.HostedModel() || state.RemovedFromOA {
		return err
	}
	_, err = s.save(ctx, func(st *StepState) error {
		if st.HostedModel() {
			st.RemovedFromOA = true
		}
		return nil
	})
	return err
}

// Pending reports whether approved work this Panel build was running is
// unfinished: the presenter resumes the step at sign-in when it is. Work an
// earlier build ran stopped with the update and waits for the person's Set
// up (owner, 2026-11-09).
func (s SetupStep) Pending(ctx context.Context) (bool, error) {
	state, err := s.Load(ctx)
	return state.Progress == stepApproved && state.Build == PanelBuild(), err
}

// stopForUpdate is the installer's Finish finding earlier answered work
// left to do: the installer's update or repair ended the run that drove it.
// The work stays as it is, stopped, with one notice, until the person's Set
// up.
func (s SetupStep) stopForUpdate(ctx context.Context) (stepOutcome, error) {
	stopped := StepStopped{Stage: stageUpdate, Reason: stoppedReason(stageUpdate), At: s.now()}
	// No build drives it now: sign-in leaves it, even after a reinstall of
	// the same build, until the person's Set up.
	if _, err := s.save(ctx, func(st *StepState) error {
		st.Stopped, st.Build = &stopped, ""
		return nil
	}); err != nil {
		return "", atStage(stageJournal, err)
	}
	return stepStoppedByUpdate, s.notify(ctx, StepNotice{Title: "Catalog stopped", Body: stopped.Reason + " Open Settings to continue.", Tag: "setup"})
}

// RunAndReport is one run of the step that reports its own failure through
// one notice. A run ended by its context (sign-out) reports nothing: the
// presenter resumes it at the next sign-in.
func (s SetupStep) RunAndReport(ctx context.Context) (stepOutcome, error) {
	outcome, subject, err := s.run(ctx)
	if err != nil && ctx.Err() == nil {
		notice := s.failureNotice(err, subject)
		recordErr := s.recordStopped(ctx, err, notice, subject)
		if notifyErr := s.notify(ctx, notice); notifyErr != nil || recordErr != nil {
			return outcome, errors.Join(err, recordErr, notifyErr)
		}
	}
	return outcome, err
}

// recordStopped keeps why the run stopped: in the step journal, which
// leaves the phase as it was, and through Owners.Record in OA logging. A
// logging service that is not ready loses nothing the journal keeps.
func (s SetupStep) recordStopped(ctx context.Context, err error, notice StepNotice, subject StepTitles) error {
	stopped := StepStopped{Stage: stageOf(err), Reason: strings.TrimSuffix(notice.Body, " Open Settings to try again."), Detail: err.Error(), At: s.now()}
	if stopped.Stage == "" {
		stopped.Stage = "unknown"
	}
	var saveErr error
	// The journal records the chat and picture parts; a video request's
	// stop goes to OA logging alone.
	if r := s.Requester; r == nil || r.Use != UseVideo {
		_, saveErr = s.save(ctx, func(st *StepState) error {
			st.Stopped = &stopped
			return nil
		})
	}
	if s.Owners.Record != nil {
		//unchecked: OA logging may not be ready; the journal holds the same record
		_ = s.Owners.Record(ctx, stopped, subject.All())
	}
	return saveErr
}

// errStepDownload is a download job that ended without the reviewed bytes.
var errStepDownload = errors.New("the download did not finish")

// errStepPictures marks a failure of the picture part, so its notice names
// the picture app and pictures.
var errStepPictures = errors.New("the picture part did not finish")

// StepTitles names the apps the step serves by registered title: Chat
// joins the titles of the apps that get the coding model, Pictures is the
// title of the app that gets pictures. Every question, notice and the
// Settings row name the apps this way.
type StepTitles struct {
	Chat     string
	Pictures string
}

// TitlesOf words apps by their uses.
func TitlesOf(apps []StepApp) StepTitles {
	var chat []string
	var subject StepTitles
	for _, app := range apps {
		switch app.Use {
		case UseCode:
			chat = append(chat, app.Title)
		case UseImage:
			if subject.Pictures == "" {
				subject.Pictures = app.Title
			}
		}
	}
	slices.Sort(chat)
	subject.Chat = joinWords(chat, "and")
	return subject
}

// All joins every app the subject names.
func (s StepTitles) All() string {
	var words []string
	for _, w := range []string{s.Chat, s.Pictures} {
		if w != "" {
			words = append(words, w)
		}
	}
	return joinWords(words, "and")
}

// stoppedTitle heads a failure notice about who.
func stoppedTitle(who string) string {
	if who == "" {
		return "Catalog stopped"
	}
	return who + " setup stopped"
}

// orApps is who, or "apps" when no title was read.
func orApps(who string) string {
	if who == "" {
		return "apps"
	}
	return who
}

// failureNotice words a failure for the person: the owners' mapping for
// the outcomes they know, Setup's own for a stopped download, and one
// general sentence otherwise. No raw error text, hash or path reaches it.
func (s SetupStep) failureNotice(err error, subject StepTitles) StepNotice {
	if errors.Is(err, errStepVideo) && s.Requester != nil {
		return StepNotice{Title: stoppedTitle(s.Requester.App + " video"), Body: stoppedReason(stageVideo) + " " + s.Requester.App + " can ask again.", Tag: "setup"}
	}
	if s.Owners.Failure != nil {
		if n, ok := s.Owners.Failure(err, subject.All()); ok {
			n.Tag = "setup"
			return n
		}
	}
	if errors.Is(err, errStepPictures) {
		if errors.Is(err, errStepDownload) {
			return StepNotice{Title: stoppedTitle(subject.Pictures), Body: "The picture model download stopped before it finished. Open Settings to try again.", Tag: "setup"}
		}
		return StepNotice{Title: stoppedTitle(subject.Pictures), Body: "Pictures could not be set up. Open Settings to try again.", Tag: "setup"}
	}
	who := subject.Chat
	if who == "" {
		who = subject.All()
	}
	return StepNotice{Title: stoppedTitle(who), Body: stoppedReason(stageOf(err)) + " Open Settings to try again.", Tag: "setup"}
}

// Run is one run of the step. A set-up account hears "Already set up" and
// nothing is downloaded. Otherwise Fit, this Setup's reviewed plans and the
// picture offer decide the work now: approved work resumes without a
// question only while the journal records an answer to exactly that work.
// The journal is a record of the person's answer and never its authority,
// since any program of this account can write it.
func (s SetupStep) Run(ctx context.Context) (stepOutcome, error) {
	outcome, _, err := s.run(ctx)
	return outcome, err
}

// run is Run that also answers who the work served, for its failure notice.
func (s SetupStep) run(ctx context.Context) (stepOutcome, StepTitles, error) {
	var subject StepTitles
	if r := s.Requester; r != nil && r.Use == UseVideo {
		// Notices and the stop record name the requesting app.
		subject.Chat = r.App
		outcome, err := s.runVideo(ctx)
		return outcome, subject, err
	}
	state, err := s.Load(ctx)
	if err != nil {
		return "", subject, atStage(stageJournal, err)
	}
	if s.Finish && state.Approved() {
		outcome, err := s.stopForUpdate(ctx)
		return outcome, subject, err
	}
	apps, err := s.Owners.Apps(ctx)
	if err != nil {
		return "", subject, atStage(stageApps, err)
	}
	subject = TitlesOf(apps)
	code, pictures, err := s.pictureWork(ctx, apps)
	if err != nil {
		return "", subject, err
	}
	// held: a picture part that stopped while chat was set up waits for the
	// person. Settings' Try pictures again and a run the person starts retry
	// it; an app's request leaves it as it is and posts nothing about it, so
	// a repeated failure never sends a notice per request.
	held := false
	if r := s.Requester; r != nil {
		// The runtime owns whether an app request may reach this question.
		// The app hears asked, never the person's native answer.
		// A request with no approved or finished work asks about the use
		// it named only; with no offer for that use the step says so in
		// one sentence and asks about nothing else.
		if state.Progress != stepApproved && state.Progress != stepReady {
			switch r.Use {
			case UseImage:
				if pictures == nil {
					return stepCannotRun, subject, s.notify(ctx, StepNotice{Title: r.App + " cannot get pictures here yet", Body: r.App + " asked for pictures, which this computer cannot set up yet.", Tag: "setup"})
				}
				code = nil
			case UseCode:
				pictures = nil
			}
		}
		if state.Ready() && state.PicturesStopped {
			held, pictures = true, nil
		}
	}
	// removed: the person took the coding model this computer hosted out of
	// OA (the Models page's Remove from OA). ModelHost's list decides, not
	// the journal. Nothing runs the step for it at sign-in (Pending), so the
	// removal holds until a run starts. An app's request for a coding model
	// hosts it again with no question (owner, 2026-11-01): the answer the
	// journal records covers it, and a copy of the pinned file found on this
	// computer is used where it lies, never downloaded again.
	checked, hostedNow, err := s.hostState(ctx, state)
	if err != nil {
		return "", subject, atStage(stageSelect, err)
	}
	removed, ours := checked && !hostedNow, checked && hostedNow
	rehost := removed && s.Requester != nil && s.Requester.Use == UseCode
	// keep: the removed model stays out of this run. Setup's Finish after
	// an explicit upgrade or the person's recorded removal is an update's
	// Finish (the latter supports older bundles), and a picture
	// app's request hosts only pictures (coordinator, 2026-11-01). Settings
	// and a Finish with no recorded removal host it again.
	keep := removed && (s.Finish && (s.Update || state.RemovedFromOA) || s.Requester != nil && s.Requester.Use == UseImage ||
		state.Approved() && state.RemovedFromOA && s.Requester == nil)
	if keep {
		code = nil
	}
	if state.RemovedFromOA && !removed {
		// Hosted again outside the step (the Models page's Set up).
		if state, err = s.save(ctx, func(st *StepState) error { st.RemovedFromOA = false; return nil }); err != nil {
			return "", subject, atStage(stageJournal, err)
		}
	}
	// misnamed: ModelHost hosts the coding model under another name than the
	// catalogue's family, as an earlier Setup hosted its download under the
	// file's SHA-256. The run hosts the same copy again under the family,
	// with no question: the journal's answer covers it.
	misnamed, err := s.misnamed(ctx, state, removed)
	if err != nil {
		return "", subject, atStage(stageHost, err)
	}
	if state.Progress == stepReady {
		if done, err := s.inPlace(ctx, state, code, pictures, held, removed, misnamed, keep); err != nil {
			return "", subject, atStage(stageSelect, err)
		} else if done && (held || s.Finish) {
			return stepAlreadySetUp, subject, nil
		} else if done && keep && !state.Pictures {
			r := s.Requester
			return stepCannotRun, subject, s.notify(ctx, StepNotice{Title: r.App + " cannot get pictures here yet", Body: r.App + " asked for pictures, which this computer cannot set up yet.", Tag: "setup"})
		} else if done {
			title := state.Title
			if keep {
				title = ""
			}
			return stepAlreadySetUp, subject, s.notify(ctx, StepNotice{Title: "Already set up", Body: readySentence(title, subject, state.Pictures), Tag: "setup"})
		}
	}
	if len(code) == 0 && pictures == nil && s.Finish {
		return stepNoApp, subject, nil
	}
	if len(code) == 0 && pictures == nil {
		return stepNoApp, subject, s.notify(ctx, StepNotice{Title: "Nothing to set up", Body: "Install an app with OpenAbstractions Setup, then set it up from Settings.", Tag: "setup"})
	}
	var pick *StepPick
	var plan ModelPlan
	download := false // a removed model that only a new download hosts again
	if len(code) > 0 {
		fit, err := s.Owners.Fit(ctx, UseCode)
		if err != nil {
			return "", subject, atStage(stageFit, err)
		}
		if pick = localPick(fit); pick == nil {
			body := asks.SetupCannotRunSentence(fit.PoolBytes, fit.SmallestTitle, fit.SmallestNeed)
			return stepCannotRun, subject, s.notify(ctx, StepNotice{Title: subject.Chat + " cannot run a model here", Body: body, Tag: "setup"})
		}
		plan = s.Owners.Model.ReviewedPlan()
		// Fit reads the model the answer set up as served once ModelHost
		// hosts it. It stays Setup's own model, with nothing to download.
		if pick.Served && ours && pick.Family == state.Family {
			pick.Served, pick.Hosted, pick.DownloadBytes = false, true, plan.DownloadBytes
			if state.Adopted != nil {
				adopted := *state.Adopted
				pick.Local = &adopted
			}
		}
		if !pick.Served && !planDownloads(plan, *pick) {
			return "", subject, atStage(stagePlan, errors.New("the catalogue's pick has no reviewed download plan in this Setup"))
		}
		if misnamed && !pick.Served && !pick.Hosted && pick.Family == state.Family {
			// The copy the journal records is the one hosted again.
			pick.Local = state.Adopted
		} else if !pick.Served && !pick.Hosted && s.Owners.Local != nil {
			found, err := s.findLocal(ctx, plan, subject)
			if err != nil {
				return "", subject, atStage(stageAdopt, err)
			}
			pick.Local, pick.Untrusted = found.Local, found.Untrusted
		}
		// Hosting again after an app's request: Setup's own verified
		// download is hosted where Setup stored it; a copy the inventory
		// found is adopted only when that download is not verified. With
		// neither, hosting it again needs a download, which the person
		// answers first.
		if rehost && !pick.Served && state.Adopted == nil {
			verified, err := s.downloadVerified(ctx, state)
			if err != nil {
				return "", subject, atStage(stageHost, err)
			}
			switch {
			case verified:
				pick.Local = nil
			case pick.Local == nil:
				download = true
			}
		}
	}
	chat, err := s.chatOffers(ctx, code, pick)
	if err != nil {
		return "", subject, atStage(stageGrant, err)
	}
	askedNow := false
	if (state.Progress != stepApproved && state.Progress != stepReady) || download || !answered(state, pick, plan, pictures, held, rehost, keep, chat) {
		var outcome stepOutcome
		askedNow = true
		state, outcome, err = s.ask(ctx, apps, code, pick, plan, pictures, keep, chat)
		if err != nil || outcome != "" {
			return outcome, subject, err
		}
	}
	// The installer's Finish runs work only for the answer the person gave
	// in this run (owner, 2026-11-09). Earlier answered work that is left,
	// approved or Ready but no longer complete, waits for their Set up.
	if s.Finish && !askedNow {
		outcome, err := s.stopForUpdate(ctx)
		return outcome, subject, err
	}
	outcome, err := s.finish(ctx, state, subject, pick, plan, pictures, chat)
	return outcome, subject, err
}

// chatOffers reads the chat permission of each app the pick serves: none for
// a model this computer already serves another way, since Setup included no
// model for it, and none without Owners.Chat.
func (s SetupStep) chatOffers(ctx context.Context, code []StepApp, pick *StepPick) ([]StepChatOffer, error) {
	if s.Owners.Chat == nil || pick == nil || pick.Served {
		return nil, nil
	}
	var offers []StepChatOffer
	for _, app := range code {
		offer, err := s.Owners.Chat.Offer(ctx, app, *pick)
		if err != nil {
			return nil, err
		}
		if offer.Reason == "" {
			offers = append(offers, offer)
		}
	}
	return offers, nil
}

// chatAnswered reports whether the journal's answer covered exactly the
// apps and programs offered a chat permission now. The person approved that
// each app can use the model; the rules that carry it follow the model's
// host, so a changed rule set is renewed without a question (grantChat).
func chatAnswered(granted []StepChatGrant, offers []StepChatOffer) bool {
	if len(granted) != len(offers) {
		return false
	}
	for _, offer := range offers {
		if !slices.ContainsFunc(granted, func(g StepChatGrant) bool { return g.App == offer.App && g.Program == offer.Program }) {
			return false
		}
	}
	return true
}

// chatCurrent reports whether g holds offer's rules as written.
func chatCurrent(g StepChatGrant, offer StepChatOffer) bool {
	return g.Granted && g.Rules == RulesDigest(offer.Rules)
}

// StepLookup is the model inventory's answer for a plan's pinned file.
type StepLookup struct {
	// Local is the object holding the file, byte for byte.
	Local *StepLocal
	// Hashing: the inventory lists a weights file of the pinned size with no
	// digest yet, and was asked to hash it, so it may still be the file.
	Hashing bool
	// Untrusted: the inventory holds the pinned file, but other accounts can
	// change it or a folder above it (trustedpath, LS-12's check), so it is
	// not adopted.
	Untrusted bool
}

// localCheckBound is how long the step waits for the inventory to finish
// hashing a candidate of size bytes before it offers the download: at
// least half a minute, and about three minutes for 25 GB.
func localCheckBound(size int64) time.Duration {
	const rate = 140_000_000 // bytes a second the bound assumes
	bound := time.Duration(size/rate) * time.Second
	if bound < 30*time.Second {
		bound = 30 * time.Second
	}
	return bound
}

// findLocal asks Owners.Local for the plan's pinned file. A same-size
// candidate still being hashed is waited for, within localCheckBound, with
// one notice; the step never offers the download while it may be the file.
// A store that cannot be read, or no candidate, leaves the download: the
// person loses only the saved time.
func (s SetupStep) findLocal(ctx context.Context, plan ModelPlan, subject StepTitles) (StepLookup, error) {
	clock := time.Now
	if s.Owners.Now != nil {
		clock = s.Owners.Now
	}
	deadline := clock().Add(localCheckBound(plan.DownloadBytes))
	announced := false
	for {
		found, err := s.Owners.Local(ctx, plan)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return StepLookup{}, ctxErr
			}
			return StepLookup{}, nil
		}
		if found.Untrusted {
			return StepLookup{Untrusted: true}, nil
		}
		if found.Local != nil || !found.Hashing || !clock().Before(deadline) {
			return StepLookup{Local: found.Local}, nil
		}
		if !announced {
			if err := s.notify(ctx, StepNotice{Title: "Setting up " + orApps(subject.Chat), Body: "Checking a copy already on this computer.", Tag: "setup"}); err != nil {
				return StepLookup{}, err
			}
			announced = true
		}
		if err := s.wait(ctx); err != nil {
			return StepLookup{}, err
		}
	}
}

// pictureWork splits the apps into those the chat part serves and the
// picture part. The part serves the first app whose use is pictures; it is
// offered only while that app's engine is the reviewed picture plan's and
// its owners can prepare it here.
func (s SetupStep) pictureWork(ctx context.Context, apps []StepApp) ([]StepApp, *stepPictureWork, error) {
	p := s.Owners.Pictures
	var code []StepApp
	var found *StepApp
	for _, app := range apps {
		switch {
		case app.Use == UseCode:
			code = append(code, app)
		case app.Use == UseImage && found == nil && app.Manifest == "":
			app := app
			found = &app
		}
	}
	if found == nil || p == nil {
		return code, nil, nil
	}
	plan := p.Model.ReviewedPlan()
	if found.Engine != plan.PictureEngine() {
		return code, nil, nil
	}
	offer, err := p.Offer(ctx, *found)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStepPictures, err)
	}
	if offer.Reason != "" {
		return code, nil, nil
	}
	return code, &stepPictureWork{app: *found, offer: offer, plan: plan}, nil
}

// localPick is Fit's pick when this computer runs or serves it. Slice 1
// writes a local chat candidate only, so a model a paired machine serves is
// no pick yet (slice 2 offers it as a choice with the binding's credential).
func localPick(fit StepFit) *StepPick {
	if fit.Pick == nil || fit.Pick.Machine != "" {
		return nil
	}
	pick := *fit.Pick
	return &pick
}

// answered reports whether the journal records an answer to exactly this
// pick, reviewed plan and picture offer. A held picture part keeps its
// answer. With rehost (an app's request for a removed coding model), a copy
// the inventory found also answers: it holds the reviewed plan's pinned file
// byte for byte, so using it downloads nothing the answer did not approve.
// With keep, the removed coding model is left out of the run and its answer
// stands. The answer must also have covered exactly the chat permissions
// offered now (chat): another app or program asks again.
func answered(state StepState, pick *StepPick, plan ModelPlan, pictures *stepPictureWork, held, rehost, keep bool, chat []StepChatOffer) bool {
	if pick == nil {
		if !keep && (state.Family != "" || state.Title != "") {
			return false
		}
	} else {
		if !chatAnswered(state.ChatGrants, chat) {
			return false
		}
		if state.Family != pick.Family || state.Title != pick.Title || state.Served != pick.Served {
			return false
		}
		if pick.Served && state.ModelPlanID != "" || !pick.Served && state.ModelPlanID != plan.ID {
			return false
		}
		if !(rehost && pick.Local != nil) && ((state.Adopted == nil) != (pick.Local == nil) || pick.Local != nil && *state.Adopted != *pick.Local) {
			return false
		}
	}
	if pictures == nil {
		return !state.Pictures || held
	}
	return state.Pictures && state.PicturePlanID == pictures.plan.ID && state.PicturePackages == pictures.offer.PlanID &&
		state.PictureRules == RulesDigest(pictures.offer.Rules) && state.PictureProgram == pictures.offer.Program
}

// ask asks the one question for the work and records the answer. A new
// answer replaces whatever the journal held, so a changed plan asks once
// and resumes from the new answer. With keep, a Set up to the picture part
// keeps the answer for the removed coding model, so an app's later request
// hosts it again without a question.
// The answer records the chat permissions the question stated (chat); they
// are written after the model is in place, never on Not now.
func (s SetupStep) ask(ctx context.Context, apps, code []StepApp, pick *StepPick, plan ModelPlan, pictures *stepPictureWork, keep bool, chat []StepChatOffer) (StepState, stepOutcome, error) {
	q := stepQuestion(code, pick, plan, pictures, chat)
	if q.renderErr != nil {
		return StepState{}, "", atStage(stageQuestion, q.renderErr)
	}
	if s.Requester != nil && s.Requester.App != "" {
		facts, err := asks.SetupFactsFromSlots(q.Facts)
		if err != nil {
			return StepState{}, "", atStage(stageQuestion, err)
		}
		facts.Requester, facts.RequestUse = s.Requester.App, s.Requester.Use
		q, err = stepQuestionFromFacts(facts)
		if err != nil {
			return StepState{}, "", atStage(stageQuestion, err)
		}
	}
	yes, err := s.Owners.Ask(ctx, q)
	if err != nil {
		return StepState{}, "", atStage(stageQuestion, err)
	}
	if !yes {
		// Not now writes only the setup state: no rule, selection or download.
		_, err := s.save(ctx, func(st *StepState) error {
			*st = StepState{Progress: stepDeclined, Use: UseCode, At: s.now()}
			return nil
		})
		return StepState{}, stepAnsweredNotNow, atStage(stageAnswer, err)
	}
	// The answer names every listed app except a picture app whose part was
	// not offered.
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if app.Use != UseImage || pictures != nil && app.Name == pictures.app.Name {
			names = append(names, app.Name)
		}
	}
	state, err := s.save(ctx, func(st *StepState) error {
		prior := *st
		*st = StepState{Progress: stepApproved, Use: UseCode, Apps: names, At: s.now()}
		if keep {
			st.Title, st.Family, st.Served, st.CatalogVersion = prior.Title, prior.Family, prior.Served, prior.CatalogVersion
			// The kept model was removed: the resumed run leaves it out too.
			st.ModelPlanID, st.Adopted, st.Hosted, st.Selected, st.RemovedFromOA = prior.ModelPlanID, prior.Adopted, prior.Hosted, prior.Selected, true
			st.ChatGrants = prior.ChatGrants
		}
		if pick != nil {
			st.Title, st.Family, st.Served, st.CatalogVersion = pick.Title, pick.Family, pick.Served, pick.CatalogVersion
			if !pick.Served {
				st.ModelPlanID, st.Adopted = plan.ID, pick.Local
			}
			for _, offer := range chat {
				st.ChatGrants = append(st.ChatGrants, offer.grant())
			}
		}
		if pictures != nil {
			st.Pictures, st.PicturePlanID, st.PicturePackages = true, pictures.plan.ID, pictures.offer.PlanID
			st.PictureRules, st.PictureProgram = RulesDigest(pictures.offer.Rules), pictures.offer.Program
		}
		return nil
	})
	if err != nil {
		return StepState{}, "", atStage(stageAnswer, err)
	}
	// An app that added itself is confirmed only when the question named it
	// as a target: the runtime keeps exactly those on this answer.
	targeted := func(name string) bool {
		return slices.ContainsFunc(code, func(app StepApp) bool { return app.Name == name }) || pictures != nil && pictures.app.Name == name
	}
	var pending []StepApp
	for _, app := range apps {
		if (app.Unconfirmed && targeted(app.Name) || app.Manifest != "") && slices.Contains(names, app.Name) {
			pending = append(pending, app)
		}
	}
	if len(pending) > 0 {
		if err := s.Owners.Confirm(ctx, pending); err != nil {
			return StepState{}, "", atStage(stageConfirm, err)
		}
	}
	return state, "", nil
}

// finish runs the approved work in order for the work this run computed:
// the chat part, then the picture part. Each owner's journal decides what
// is already done, so a rerun after sign-out continues the same jobs.
func (s SetupStep) finish(ctx context.Context, state StepState, subject StepTitles, pick *StepPick, plan ModelPlan, pictures *stepPictureWork, chat []StepChatOffer) (stepOutcome, error) {
	var err error
	// This build drives the approved work now; sign-in resumes it only for
	// this build (Pending).
	if build := PanelBuild(); state.Build != build {
		if state, err = s.save(ctx, func(st *StepState) error { st.Build = build; return nil }); err != nil {
			return "", atStage(stageJournal, err)
		}
	}
	if pick != nil {
		if state, err = s.finishChat(ctx, state, subject, *pick, plan); err != nil {
			return "", err
		}
		if state, err = s.grantChat(ctx, state, chat); err != nil {
			return "", atStage(stageGrant, err)
		}
	}
	if pictures != nil {
		var picturesErr error
		if state, picturesErr = s.finishPictures(ctx, state, *pictures); picturesErr != nil {
			picturesErr = fmt.Errorf("%w: %w", errStepPictures, picturesErr)
			// A chat part that works stays Ready: pictures keep their own
			// state, sentence and retry (architect review 25, A3). A run
			// ended by sign-out resumes both at the next sign-in.
			if pick == nil || ctx.Err() != nil {
				return "", picturesErr
			}
			if state, err = s.save(ctx, func(st *StepState) error {
				st.Progress, st.PicturesStopped, st.At = stepReady, true, s.now()
				return nil
			}); err != nil {
				return "", errors.Join(picturesErr, err)
			}
			stopped := s.failureNotice(picturesErr, subject)
			if err := s.recordStopped(ctx, picturesErr, stopped, subject); err != nil {
				return "", errors.Join(picturesErr, err)
			}
			return stepSetUp, s.notify(ctx, StepNotice{Title: orApps(subject.Chat) + " is ready; " + stoppedTitle(subject.Pictures),
				Body: readySentence(state.Title, subject, false) + " " + stopped.Body, Tag: "setup"})
		}
	}
	state, err = s.save(ctx, func(st *StepState) error {
		// A held picture part keeps its stopped state for the person's retry.
		st.Progress, st.PicturesStopped, st.At = stepReady, st.PicturesStopped && pictures == nil, s.now()
		if !st.PicturesStopped {
			st.Stopped = nil
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// A run that left a removed coding model out names pictures alone.
	title := state.Title
	if pick == nil {
		title = ""
	}
	return stepSetUp, s.notify(ctx, StepNotice{Title: readyTitle(subject, pick != nil, pictures != nil),
		Body: readySentence(title, subject, state.Pictures && !state.PicturesStopped), Tag: "setup"})
}

// finishChat downloads and hosts the coding model unless it is served
// here, then makes it the chat default. Hosting runs on every pass: Adopt
// and Host are idempotent, and a model the person removed from OA is
// hosted again.
func (s SetupStep) finishChat(ctx context.Context, state StepState, subject StepTitles, pick StepPick, plan ModelPlan) (StepState, error) {
	var err error
	if !pick.Served {
		switch {
		case pick.Local != nil && s.Owners.Adopt != nil:
			// The copy the inventory found is hosted where it lies. The
			// journal records it, so Hosting checks the copy hosted now
			// (an app's request may host another copy than the answer did).
			local := *pick.Local
			if err := s.Owners.Adopt(ctx, local, pick.Hosting()); err != nil {
				return state, atStage(stageAdopt, err)
			}
			if state, err = s.save(ctx, func(st *StepState) error { st.Hosted, st.Adopted, st.RemovedFromOA = true, &local, false; return nil }); err != nil {
				return state, atStage(stageAdopt, err)
			}
		default:
			if state, err = s.download(ctx, state, s.Owners.Model, plan, "Setting up "+orApps(subject.Chat), pick.Title); err != nil {
				return state, atStage(stageDownload, err)
			}
			if err := s.Owners.Host(ctx, pick.Hosting()); err != nil {
				return state, atStage(stageHost, err)
			}
			if state, err = s.save(ctx, func(st *StepState) error { st.Hosted, st.RemovedFromOA = true, false; return nil }); err != nil {
				return state, atStage(stageHost, err)
			}
		}
	}
	selected, err := s.Owners.Selected(ctx, pick.Family)
	if err != nil {
		return state, atStage(stageSelect, err)
	}
	if !selected {
		if err := s.Owners.Select(ctx, pick.Family); err != nil {
			return state, atStage(stageSelect, err)
		}
	}
	state, err = s.save(ctx, func(st *StepState) error { st.Selected = true; return nil })
	return state, atStage(stageSelect, err)
}

// grantChat writes each chat permission the answer covered, once the model
// is in place: the rules of the offer for that app and program. Rules once
// written are not written again while they stay the same, so a person's
// later change to them in Saved permissions stands, and hosting the model
// again after a removal keeps them. Rules that changed with the model's host
// are written again: the answer approved the use, which follows the host.
// An app whose registration is not confirmed yet waits for a later run.
func (s SetupStep) grantChat(ctx context.Context, state StepState, chat []StepChatOffer) (StepState, error) {
	if s.Owners.Chat == nil {
		return state, nil
	}
	for _, offer := range chat {
		i := slices.IndexFunc(state.ChatGrants, func(g StepChatGrant) bool { return g.App == offer.App && g.Program == offer.Program })
		if i < 0 || chatCurrent(state.ChatGrants[i], offer) {
			continue
		}
		written, settled, err := s.Owners.Chat.Grant(ctx, offer, state.ChatGrants[i].Written)
		if err != nil {
			return state, err
		}
		if !settled {
			continue
		}
		if state, err = s.save(ctx, func(st *StepState) error {
			for j := range st.ChatGrants {
				if st.ChatGrants[j].App == offer.App && st.ChatGrants[j].Program == offer.Program {
					st.ChatGrants[j].Rules, st.ChatGrants[j].Granted, st.ChatGrants[j].Written = RulesDigest(offer.Rules), true, written
				}
			}
			return nil
		}); err != nil {
			return state, err
		}
	}
	return state, nil
}

// finishPictures installs the engine package and feature, downloads and
// prepares the picture model, and writes the app's picture rules once.
func (s SetupStep) finishPictures(ctx context.Context, state StepState, work stepPictureWork) (StepState, error) {
	p := s.Owners.Pictures
	for {
		done, err := p.Engine(ctx, work.offer)
		if err != nil {
			return state, err
		}
		if done {
			break
		}
		if err := s.wait(ctx); err != nil {
			return state, err
		}
	}
	if !work.offer.Configured {
		var err error
		// A run the person started after the picture part stopped is their
		// retry: a download the job service ended as failed starts again.
		// An app's request holds the stopped part before it gets here.
		if state.PicturesStopped && s.Requester == nil {
			if err := retryFailed(ctx, p.Model); err != nil {
				return state, err
			}
		}
		if state, err = s.download(ctx, state, p.Model, work.plan, "Setting up "+work.app.Title, work.offer.Title); err != nil {
			return state, err
		}
		if err := p.Configure(ctx); err != nil {
			return state, err
		}
	}
	if state.Granted {
		return state, nil
	}
	if err := p.Grant(ctx, work.offer); err != nil {
		return state, err
	}
	return s.save(ctx, func(st *StepState) error { st.Granted = true; return nil })
}

// retryFailed resubmits each download of model the job service ended as
// failed, through the controller's own Retry.
func retryFailed(ctx context.Context, model StepModel) error {
	retrier, ok := model.(interface {
		Retry(context.Context, string) (ModelStatus, error)
	})
	if !ok {
		return nil
	}
	status, err := model.Status(ctx)
	if err != nil {
		return err
	}
	for _, asset := range status.Assets {
		if asset.Phase == Failed {
			if _, err := retrier.Retry(ctx, asset.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

const (
	// unnamedRounds is how many observations a download waits for its job's
	// operation before announcing itself without one.
	unnamedRounds = 3
	// plannedAnnouncement prefixes the plan a notice without an operation
	// announced, in StepState.Announced.
	plannedAnnouncement = "plan:"
)

// download starts or resumes the approved plan's public jobs and waits for
// every asset to be verified. It posts one notice when the download starts,
// bound to the job's operation so the presenter and Activity follow its
// progress; the journal keeps that notice from repeating after sign-in.
func (s SetupStep) download(ctx context.Context, state StepState, model StepModel, plan ModelPlan, heading, title string) (StepState, error) {
	approval := ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: plan.Licenses}
	status, err := model.StartAccepted(ctx, approval)
	if err != nil {
		return state, err
	}
	rounds, announcedHere := 0, false
	for {
		verified, operation := true, ""
		for _, asset := range status.Assets {
			switch asset.Phase {
			case Verified:
			case Failed:
				return state, fmt.Errorf("%w: %s: %s", errStepDownload, title, asset.Reason)
			default:
				verified = false
				if asset.Transfer != nil && operation == "" {
					operation = asset.Transfer.Operation
				}
			}
		}
		if verified {
			return state, nil
		}
		// The start notice waits for the job's operation, so the presenter
		// and Activity follow that job; each new operation gets one notice.
		// A job still unnamed after a few rounds is announced without one,
		// so a long download never runs unannounced; the operation that
		// appears later in this run is recorded without a second notice.
		unnamed := plannedAnnouncement + plan.ID
		switch {
		case operation != "" && (state.Announced == operation || announcedHere):
			if state.Announced != operation {
				if state, err = s.save(ctx, func(st *StepState) error { st.Announced = operation; return nil }); err != nil {
					return state, err
				}
			}
		case operation != "" || rounds >= unnamedRounds && state.Announced != unnamed:
			if err := s.notify(ctx, StepNotice{Title: heading, Body: fmt.Sprintf("Downloading %s, about %s. Activity shows its progress.", title, asks.SetupGigabytes(plan.DownloadBytes)),
				Tag: "setup", Operation: operation}); err != nil {
				return state, err
			}
			mark := operation
			if mark == "" {
				mark = unnamed
			}
			if state, err = s.save(ctx, func(st *StepState) error { st.Announced = mark; return nil }); err != nil {
				return state, err
			}
			announcedHere = true
		}
		rounds++
		if err := s.wait(ctx); err != nil {
			return state, err
		}
		for _, asset := range status.Assets {
			if asset.Phase != Verified {
				if status, err = model.Advance(ctx, asset.ID); err != nil {
					return state, err
				}
			}
		}
	}
}

// inPlace re-reads the owners: the chat download is verified (or the pick
// is served here already) and the chat default still selects it; the
// picture part, when offered, is answered, installed, prepared and granted.
// A held picture part is left out. removed is the run's read of ModelHost's
// list (SetupStep.hostState), and misnamed its read of the hosted name
// (SetupStep.misnamed); with keep the removed coding model is left out. The
// chat permissions the answer covered for code's apps are written for
// every app whose registration is confirmed.
func (s SetupStep) inPlace(ctx context.Context, state StepState, code []StepApp, pictures *stepPictureWork, held, removed, misnamed, keep bool) (bool, error) {
	if pictures != nil {
		if !state.Pictures || !state.Granted || !pictures.offer.EngineReady || !pictures.offer.Configured ||
			state.PicturePlanID != pictures.plan.ID || state.PictureRules != RulesDigest(pictures.offer.Rules) || state.PictureProgram != pictures.offer.Program {
			return false, nil
		}
	}
	if keep {
		return true, nil
	}
	if state.Family == "" {
		return !held && (pictures != nil || state.Pictures), nil
	}
	// An adopted copy has no download job; Host and the chat default are
	// its record.
	if !state.Served && state.Adopted == nil {
		if verified, err := s.downloadVerified(ctx, state); err != nil || !verified {
			return false, err
		}
	}
	// A model this computer hosts must still be hosted: ModelHost's list
	// decides, not the journal.
	if removed || misnamed {
		return false, nil
	}
	if !state.Served {
		offers, err := s.chatOffers(ctx, code, &StepPick{Title: state.Title, Family: state.Family})
		if err != nil {
			return false, err
		}
		if !chatAnswered(state.ChatGrants, offers) {
			return false, nil
		}
		for _, offer := range offers {
			confirmed := slices.ContainsFunc(code, func(app StepApp) bool { return app.Name == offer.App && !app.Unconfirmed && app.Manifest == "" })
			i := slices.IndexFunc(state.ChatGrants, func(g StepChatGrant) bool { return g.App == offer.App && g.Program == offer.Program })
			if confirmed && !chatCurrent(state.ChatGrants[i], offer) {
				return false, nil
			}
		}
	}
	return s.Owners.Selected(ctx, state.Family)
}

// downloadVerified reports whether the coding-model controller's journal
// holds the answer's reviewed plan approved and every file verified.
func (s SetupStep) downloadVerified(ctx context.Context, state StepState) (bool, error) {
	status, err := s.Owners.Model.Status(ctx)
	if err != nil {
		return false, err
	}
	if !status.Approved || len(status.Assets) == 0 || status.Plan.ID != state.ModelPlanID {
		return false, nil
	}
	for _, asset := range status.Assets {
		if asset.Phase != Verified {
			return false, nil
		}
	}
	return true, nil
}

// hostState reads ModelHost's list for a step whose coding model this
// computer hosted: checked is false when there is nothing to read (the step
// hosted no model yet, or nil Hosting trusts the journal). A checked model
// that is not hosted was removed by the person (Remove from OA); one that is
// hosted is Setup's own. The step is finished, or approved picture work kept
// the removed model's answer (ask).
func (s SetupStep) hostState(ctx context.Context, state StepState) (checked, hosted bool, err error) {
	if !(state.Ready() || state.Approved() && state.RemovedFromOA && state.Hosted) || !state.HostedModel() || s.Owners.Hosting == nil {
		return false, false, nil
	}
	hosted, err = s.Owners.Hosting(ctx, state)
	return err == nil, hosted, err
}

// misnamed reports a step whose coding model ModelHost hosts under another
// name than the catalogue's family (Owners.Named). A removed model is
// hosted again by the removal's own path; nil Named trusts the journal.
func (s SetupStep) misnamed(ctx context.Context, state StepState, removed bool) (bool, error) {
	if removed || !state.HostedModel() || s.Owners.Named == nil {
		return false, nil
	}
	named, err := s.Owners.Named(ctx, state)
	return err == nil && !named, err
}

func (s SetupStep) wait(ctx context.Context) error {
	if s.Owners.Wait != nil {
		return s.Owners.Wait(ctx)
	}
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s SetupStep) notify(ctx context.Context, n StepNotice) error {
	if s.Owners.Notify == nil {
		return nil
	}
	return s.Owners.Notify(ctx, n)
}

// planDownloads reports whether plan downloads exactly the pick's file.
func planDownloads(plan ModelPlan, pick StepPick) bool {
	return len(plan.Assets) == 1 && "sha256:"+plan.Assets[0].Download.SHA256 == pick.Digest && plan.DownloadBytes == pick.DownloadBytes
}

// readyTitle and readySentence word the end of the step and a rerun with
// the apps' registered titles.
func readyTitle(subject StepTitles, chat, pictures bool) string {
	var open []string
	if chat {
		open = append(open, orApps(subject.Chat))
	}
	if pictures {
		open = append(open, orApps(subject.Pictures))
	}
	return "Ready. Open " + joinWords(open, "or") + "."
}

func readySentence(title string, subject StepTitles, pictures bool) string {
	switch {
	case title != "" && pictures:
		return title + " answers " + orApps(subject.Chat) + " and " + orApps(subject.Pictures) + " makes pictures on this computer."
	case pictures:
		return orApps(subject.Pictures) + " makes pictures on this computer."
	}
	return title + " answers " + orApps(subject.Chat) + " on this computer."
}

// stepQuestion words the one question: what each app gets, the download and
// the free space it needs. When every app gets the chat permission (chat),
// the question says each can use the model: the answer grants that use.
// Licences, sources and the picture app's permissions sit behind Show
// details.
func stepQuestionFromFacts(f asks.SetupQuestionFacts) (StepQuestion, error) {
	slots, err := asks.SetupFactSlots(f)
	if err != nil {
		return StepQuestion{}, err
	}
	body, err := asks.SetupQuestionPresentation(slots)
	if err != nil {
		return StepQuestion{}, err
	}
	return StepQuestion{Title: body.Title, Instruction: body.Instruction, Details: body.Details, Facts: slots}, nil
}

func stepQuestion(code []StepApp, pick *StepPick, plan ModelPlan, pictures *stepPictureWork, chat []StepChatOffer) StepQuestion {
	f := asks.SetupQuestionFacts{}
	offers := map[string]StepChatOffer{}
	for _, offer := range chat {
		offers[offer.App] = offer
	}
	for _, app := range code {
		offer, ok := offers[app.Name]
		f.Targets = append(f.Targets, asks.SetupTargetFact{Name: app.Name, Title: app.Title, Program: app.Program, Revision: app.Revision, Rules: RulesDigest(offer.Rules), Use: UseCode, Chat: ok})
	}
	if pick != nil {
		state := "download"
		if pick.Served {
			state = "served"
		}
		if pick.Hosted {
			state = "hosted"
		}
		if pick.Local != nil {
			state = "local"
		}
		f.Code = &asks.SetupCodeFact{Title: pick.Title, License: pick.License, Source: plan.Source, State: state, Untrusted: pick.Untrusted, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Ref: asks.SetupModelFact{Family: pick.Family, CatalogVersion: pick.CatalogVersion, Digest: pick.Digest, Plan: plan.ID, Context: pick.Context}}
	}
	if p := pictures; p != nil {
		fallback, err := asks.SetupFallbackFact(p.offer.Fallback)
		if err != nil {
			return StepQuestion{renderErr: err}
		}
		var permissions []string
		for _, sentence := range p.offer.Permissions {
			found := ""
			for _, kind := range []string{"make_pictures", "write_pictures", "read_pictures"} {
				if asks.SetupPermissionWords(kind) == sentence {
					found = kind
					break
				}
			}
			if found == "" {
				return StepQuestion{renderErr: errors.New("setup: unknown picture permission")}
			}
			permissions = append(permissions, found)
		}
		f.Targets = append(f.Targets, asks.SetupTargetFact{Name: p.app.Name, Title: p.app.Title, Program: p.app.Program, Revision: p.app.Revision, Rules: RulesDigest(p.offer.Rules), Use: UseImage})
		f.Picture = &asks.SetupPictureFact{Title: p.offer.Title, Configured: p.offer.Configured, Fallback: fallback, DownloadBytes: p.plan.DownloadBytes, PeakBytes: p.plan.PeakBytes, PackageBytes: p.offer.PackageBytes, PackagePeakBytes: p.offer.PackagePeakBytes, Licenses: p.plan.Licenses, PackageLicenses: p.offer.PackageLicenses, Permissions: permissions, Ref: asks.SetupModelFact{Family: p.plan.Model, Plan: p.plan.ID, Packages: p.offer.PlanID}}
	}
	q, err := stepQuestionFromFacts(f)
	if err != nil {
		q.renderErr = err
	}
	return q
}

func pictureDetails(work *stepPictureWork) string {
	return stepQuestion(nil, nil, ModelPlan{}, work, nil).Details
}

func joinWords(words []string, conj string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return fmt.Sprintf("%s %s %s", strings.Join(words[:len(words)-1], ", "), conj, words[len(words)-1])
}
