package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	logging "github.com/openabstractions/abstraction-logging/go"
	model "github.com/openabstractions/abstraction-model/go/client"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The setup step's Panel side (research/first-run/DECISION.md §4): the
// --setup mode runs Setup's Step with the effects below, each an existing
// owner: catalog@1 Fit, Applications, the coding-model controller, the
// ModelHost Setup operations, selection-operator@2 and notification@1.

// chatPolicy is the account default selection policy the step writes.
const chatPolicy = "chat"

const titlePolicy = "title"

// Fit supplies the resource book's free amount and curated per-model Need.
// Running models are already charged by that book; no weights are added twice.
func stepTitleFamily(main string, offers []routerclient.Offer, models []libraryModel, chat, code model.FitResult) string {
	if chat.Outcome != model.FitOutcomeFitted || code.Outcome != model.FitOutcomeFitted || chat.PoolBytes < 0 || code.PoolBytes < 0 {
		return ""
	}
	free := min(chat.PoolBytes, code.PoolBytes)
	needs := map[string]int64{}
	for _, entry := range chat.Entries {
		if entry.Machine == "" && entry.NeedBytes > 0 {
			needs[entry.Family] = entry.NeedBytes
		}
	}
	var mainNeed int64
	for _, entry := range code.Entries {
		if entry.Family == main && entry.Machine == "" {
			mainNeed = entry.NeedBytes
		}
	}
	byFamily := map[string]libraryModel{}
	for _, m := range models {
		byFamily[m.Key] = m
	}
	primary, found := byFamily[main]
	if !found || mainNeed <= 0 {
		return ""
	}
	if !primary.Running {
		if mainNeed > free {
			return ""
		}
		free -= mainNeed
	}
	chosen, smallest := "", int64(0)
	for _, offer := range offers {
		if offer.Family == main || offer.Context < 4096 || len(offer.Servers) == 0 ||
			(offer.ContextSource != routerclient.ContextSourceEffective && offer.ContextSource != routerclient.ContextSourceMaximum) {
			continue
		}
		m, ok := byFamily[offer.Family]
		need := needs[offer.Family]
		if !ok || !m.Ready || m.Size <= 0 || need <= 0 || !m.Running && need > free {
			continue
		}
		if chosen == "" || need < smallest || need == smallest && offer.Family < chosen {
			chosen, smallest = offer.Family, need
		}
	}
	return chosen
}

var stepReadTitleFamily = func(ctx context.Context, main string) (string, error) {
	router, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", err
	}
	offers, err := router.OfferContext(ctx, routerclient.OfferRequest{Profile: "chat", Placement: routerclient.PlacementLocal, Fresh: true})
	if err != nil {
		return "", err
	}
	catalog, err := router.ModelsContext(ctx, true)
	if err != nil {
		return "", err
	}
	files, err := readLibraryFiles(ctx)
	if err != nil {
		return "", err
	}
	fit, err := panelMachine().ResolveModelCatalog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", err
	}
	chat, err := fit.FitContext(ctx, model.CatalogUseChat)
	if err != nil {
		return "", err
	}
	code, err := fit.FitContext(ctx, model.CatalogUseCode)
	if err != nil {
		return "", err
	}
	models, _ := projectLibrary(catalog, files)
	return stepTitleFamily(main, offers.Offers, models, chat, code), nil
}

func stepRefreshTitle(ctx context.Context, main string) error {
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	read, err := operator.ReadAccountDefault(ctx, titlePolicy)
	if err != nil {
		return err
	}
	return stepWriteTitle(ctx, main, read, operator.PutAccountDefault)
}

func stepWriteTitle(ctx context.Context, main string, read iclient.SelectionReadV2, put func(context.Context, string, iclient.SelectionPolicyV2) (iclient.SelectionChange, error)) error {
	// A found policy is the account's choice. Setup has no creator witness
	// permitting an automatic replacement of it.
	if read.Outcome == iwire.SelectionReadOutcomeFound {
		return nil
	}
	if read.Outcome != iwire.SelectionReadOutcomeUnknown {
		return fmt.Errorf("the title preference cannot be read: %s", read.Outcome)
	}
	family, err := stepReadTitleFamily(ctx, main)
	if err != nil || family == "" {
		return err
	}
	change, err := put(ctx, read.Revision, iclient.SelectionPolicyV2{Name: titlePolicy, Candidates: []iclient.SelectionCandidateV2{{Model: family, Placement: iwire.CandidatePlacementLocal}}})
	if err != nil {
		return err
	}
	if change.Outcome != iwire.EditOutcomeApplied && change.Outcome != iwire.EditOutcomeConflict {
		return fmt.Errorf("the title preference was not written: %s %s", change.Outcome, change.Reason)
	}
	return nil
}

func (p *servicePanel) setupStep(ask func(context.Context, setup.StepQuestion) (bool, error)) (setup.SetupStep, error) {
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		return setup.SetupStep{}, err
	}
	controller, err := p.codingModelController()
	if err != nil {
		return setup.SetupStep{}, err
	}
	// The picture engine this computer runs: the catalog's optimized engine
	// where it fits, its fallback elsewhere. Without the picture-model
	// controller the step sets up chat alone.
	engine, fallback := p.stepPictureEngine(setup.NewCatalog().StepChoices())
	// Installed provider records may need reconciliation before any offer.
	if err := setup.ReconcileInstalledPictureProviderRecords(context.Background()); err != nil {
		log.Printf("setup step: %v", err)
	}
	pictures, err := p.stepPictures(engine, fallback)
	if err != nil {
		log.Printf("setup step: pictures are not offered: %v", err)
		pictures = nil
	}
	return setup.SetupStep{JournalPath: journal, Owners: setup.StepOwners{
		Pictures: pictures,
		Video:    p.stepVideo(),
		Chat:     p.stepChat(),
		Fit:      stepFit,
		Apps: func(ctx context.Context) ([]setup.StepApp, error) {
			apps, err := p.setupApplicationsForPanel(ctx)
			if err != nil {
				return nil, err
			}
			page, err := apps.Observe(ctx, "", 0)
			if err != nil {
				return nil, err
			}
			if page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
				return nil, fmt.Errorf("the applications directory answered %s", page.Outcome)
			}
			choices := setup.NewCatalog().StepChoices()
			found := stepApps(page, choices, engine)
			binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				return nil, err
			}
			rows, err := selectionRegistrationRows(applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Observe)
			if err != nil {
				return nil, err
			}
			if err := bindSetupTargetRevisions(found, rows); err != nil {
				return nil, err
			}
			fresh, err := apps.Observe(ctx, "", 0)
			if err != nil || fresh.Outcome != fwire.ApplicationOutcomePage || fresh.Cursor != page.Cursor {
				return nil, errors.New("setup target directory changed")
			}
			return append(found, stepInstalledApps(choices, engine, found, installedApplicationManifest, readApplicationManifest,
				setupAppFolder, func(name string) string { return p.stepRecordedProgram(ctx, name) })...), nil
		},
		Ask:    ask,
		Notify: stepNotify,
		Confirm: func(ctx context.Context, pending []setup.StepApp) error {
			stepConfirm(ctx, pending, p.registerInstalledManifest)
			// The answer keeps the self-registered apps it named; Ready
			// waits for those Keeps (setup_step_keep.go).
			return p.confirmSetupKeeps(ctx, pending)
		},
		Model: controller,
		// The model inventory finds the pinned coding model already on this
		// computer by its SHA-256; ModelHost hosts it where it lies.
		Local: func(ctx context.Context, plan setup.ModelPlan) (setup.StepLookup, error) {
			inventory, err := stepResolveInventory(ctx)
			if err != nil {
				return setup.StepLookup{}, err
			}
			return stepFindPinned(ctx, plan, inventory)
		},
		Adopt: func(ctx context.Context, local setup.StepLocal, hosting setup.StepHosting) error {
			return p.stepHostInPlace(ctx, local, controller.ReviewedPlan().EngineArgs, hosting.Name)
		},
		Host: func(ctx context.Context, hosting setup.StepHosting) error {
			return p.stepHost(ctx, controller, hosting)
		},
		Hosting: func(ctx context.Context, state setup.StepState) (bool, error) {
			return p.stepHosting(ctx, controller, state)
		},
		Named: func(ctx context.Context, state setup.StepState) (bool, error) {
			return p.stepNamed(ctx, controller, state)
		},
		Selected: func(ctx context.Context, family string) (bool, error) {
			_, read, err := stepChatDefault(ctx)
			if err != nil {
				return false, err
			}
			selected := stepSelects(read, family)
			if selected {
				if err := stepRefreshTitle(ctx, family); err != nil {
					log.Printf("setup title preference: %v", err)
				}
			}
			return selected, nil
		},
		Select: func(ctx context.Context, family string) error {
			operator, read, err := stepChatDefault(ctx)
			if err != nil {
				return err
			}
			change, err := operator.PutAccountDefault(ctx, read.Revision, stepChatPolicy(read, family))
			if err != nil {
				return err
			}
			if change.Outcome != iwire.EditOutcomeApplied {
				return fmt.Errorf("the chat default was not written: %s %s", change.Outcome, change.Reason)
			}
			if err := stepRefreshTitle(ctx, family); err != nil {
				log.Printf("setup title preference: %v", err)
			}
			return nil
		},
	}}, nil
}

func bindSetupTargetRevisions(targets []setup.StepApp, rows []registrationwire.Registration) error {
	for i := range targets {
		for _, row := range rows {
			_, _, found := nativeApplicationFacet(&row, targets[i].Name)
			if !found {
				continue
			}
			if !identity.SameSubjectProgram(row.Spec.Program, targets[i].Program) {
				return errors.New("setup target program changed")
			}
			targets[i].Revision = row.Revision
		}
	}
	return nil
}

// stepRecordedProgram is the program Setup's own install record gives name
// (Journal.RecordedProgram), "" when Setup installed none.
func (p *servicePanel) stepRecordedProgram(ctx context.Context, name string) string {
	_, journal, err := p.setupRunnerForPanel()
	if err != nil {
		return ""
	}
	return journal.RecordedProgram(ctx, setup.AppID(name))
}

// stepTitle names an app in the question and notices by Setup's reviewed
// catalog title, never by a title the app's own manifest or registration
// supplies (review 39).
func stepTitle(choice setup.Choice) string {
	if name := choice.Name(); name != "" {
		return name
	}
	return string(choice.ID)
}

// stepApps lists the registered apps Setup's catalog gives a use (choices,
// Catalog.StepChoices), each with that use, its catalog title and, for
// pictures, the choice's engine given that engine fits this computer
// (Choice.PictureEngine). A self-registered entry still waits for the
// person's confirmation.
func stepApps(page fwire.ApplicationPage, choices []setup.Choice, engine setup.AppID) []setup.StepApp {
	var found []setup.StepApp
	for _, entry := range page.Applications {
		for _, choice := range choices {
			name := entry.Descriptor.Name
			if name != string(choice.ID) {
				continue
			}
			found = append(found, setup.StepApp{Name: name, Title: stepTitle(choice), Unconfirmed: entry.SelfRegistered, Program: entry.Descriptor.Program,
				Use: choice.Use, Engine: choice.PictureEngine(engine)})
		}
	}
	return found
}

// stepInstalledApps lists the catalog's apps that their OpenAbstractions
// Setup package installed and that are not registered yet: the package
// records its application manifest (installed, installer/apps/app.wxs) and
// starts the Panel's registration, which the runtime may have refused; the
// app then waits in Apps as Not connected. Each comes with its catalog
// title, its manifest's program, and Manifest set, so the answer registers
// it from that manifest.
//
// Any process of this account can write the record, so a manifest is used
// only when it lies inside the folder Setup's package installs the app into
// (folder), or names the same program Setup recorded for the app (recorded).
// Every other record, a manifest that cannot be read and one for another app
// are left out.
func stepInstalledApps(choices []setup.Choice, engine setup.AppID, registered []setup.StepApp, installed func(string) (string, bool),
	read func(string) (fwire.ApplicationDescriptor, error), folder, recorded func(string) string) []setup.StepApp {
	var found []setup.StepApp
	for _, choice := range choices {
		name := string(choice.ID)
		if slices.ContainsFunc(registered, func(app setup.StepApp) bool { return app.Name == name }) {
			continue
		}
		manifest, ok := installed(name)
		if !ok {
			continue
		}
		descriptor, err := read(manifest)
		if err != nil || descriptor.Name != name {
			log.Printf("setup step: the installed manifest for %s is not used: %v", name, err)
			continue
		}
		if !trustedInstalledManifest(name, manifest, descriptor, folder, recorded) {
			log.Printf("setup step: the installed manifest record for %s lies outside its Setup folder and names another program; it is not used", name)
			continue
		}
		found = append(found, setup.StepApp{Name: name, Title: stepTitle(choice), Program: descriptor.Program, Use: choice.Use, Engine: choice.PictureEngine(engine), Manifest: manifest})
	}
	return found
}

// trustedInstalledManifest is the one trust rule for a Setup package's
// manifest record (installedApplicationManifest), which any process of this
// account can write: the manifest lies inside the folder the package installs
// name into (folder, setupAppFolder), or names the program Setup's own
// receipt-checked install record gives name (recorded,
// Journal.RecordedProgram). Setup's step and Apps' waiting rows
// (application_pending.go) both use it.
func trustedInstalledManifest(name, manifest string, descriptor fwire.ApplicationDescriptor, folder, recorded func(string) string) bool {
	root := folder(name)
	if root != "" && filepath.IsAbs(manifest) && identity.Inside(manifest, root) {
		return true
	}
	program := recorded(name)
	return program != "" && descriptor.Program != "" && identity.SameSubjectProgram(descriptor.Program, program)
}

// stepConfirm connects each app Setup installed and that is not registered
// yet, by its installer's manifest (register), as its installer's own
// request does: no question, and a refused registration waits in Apps as Not
// connected. An app that added itself and that the question named is kept by
// the runtime on the person's Set up answer (confirmSetupKeeps).
func stepConfirm(ctx context.Context, pending []setup.StepApp, register func(ctx context.Context, manifest string)) {
	for _, app := range pending {
		if app.Manifest != "" {
			register(ctx, app.Manifest)
		}
	}
}

// registerInstalledManifest applies an installed app's manifest as its
// installer's --register-application run does (application_native.go).
func (p *servicePanel) registerInstalledManifest(ctx context.Context, manifest string) {
	descriptor, err := readApplicationManifest(manifest)
	if err != nil {
		log.Printf("setup step: the installed manifest %s is not used: %v", manifest, err)
		return
	}
	//unchecked: applyApplicationRegistration logs every outcome and leaves an unconnected request waiting in Apps
	_ = p.applyApplicationRegistration(ctx, applicationStart{register: &descriptor, manifest: manifest})
}

// stepFit reads catalog@1 Fit for use and keeps its pick: the first entry
// this computer serves or fits.
func stepFit(ctx context.Context, use string) (setup.StepFit, error) {
	catalog, err := panelMachine().ResolveModelCatalog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return setup.StepFit{}, fmt.Errorf("%w: %v", errStepFit, err)
	}
	word := model.CatalogUseCode
	if use != setup.UseCode {
		return setup.StepFit{}, fmt.Errorf("the setup step has no use %q", use)
	}
	result, err := catalog.FitContext(ctx, word)
	if err != nil {
		return setup.StepFit{}, fmt.Errorf("%w: %v", errStepFit, err)
	}
	return stepFitOf(result)
}

func stepFitOf(result model.FitResult) (setup.StepFit, error) {
	if result.Outcome != model.FitOutcomeFitted {
		return setup.StepFit{}, fmt.Errorf("%w: %s", errStepFit, result.Outcome)
	}
	// The one pick rule (model.LocalPick): an entry a paired machine
	// serves is no pick and its need says nothing about this computer.
	fit := setup.StepFit{PoolBytes: result.PoolBytes}
	pick, smallest := model.LocalPick(result)
	if smallest >= 0 {
		e := result.Entries[smallest]
		fit.SmallestTitle, fit.SmallestNeed = e.Title, e.NeedBytes
	}
	if pick >= 0 {
		e := result.Entries[pick]
		fit.Pick = &setup.StepPick{Title: e.Title, Family: e.Family, Machine: e.Machine, Served: e.Availability == model.AvailabilityServed,
			DownloadBytes: e.DownloadBytes, NeedBytes: e.NeedBytes, License: e.License, Digest: e.Digest, CatalogVersion: result.CatalogVersion,
			Context: e.Context}
	}
	return fit, nil
}

var stepNoticeSerial atomic.Uint64

// stepNotify posts one notification@1 notice; the presenter shows it.
func stepNotify(ctx context.Context, n setup.StepNotice) error {
	notifications, err := panelMachine().ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	now := time.Now()
	until := now.Add(24 * time.Hour).UnixMilli()
	key := "oa-setup-" + n.Tag + "-" + strconv.FormatInt(now.UnixNano(), 36) + "-" + strconv.FormatUint(stepNoticeSerial.Add(1), 36)
	q := askclient.NotificationRequest{RequestRef: askclient.RequestRef{Key: key, ReplayUntilMs: until}, Title: n.Title, Body: n.Body,
		Tag: n.Tag, Delivery: askclient.NotificationDeliveryNow, ExpiresMs: until, Actions: []askclient.NotificationAction{}, OperationID: n.Operation}
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

func stepChatDefault(ctx context.Context) (*iclient.SelectionOperatorV2, iclient.SelectionReadV2, error) {
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, iclient.SelectionReadV2{}, err
	}
	read, err := operator.ReadAccountDefault(ctx, chatPolicy)
	if err != nil {
		return nil, iclient.SelectionReadV2{}, err
	}
	if read.Outcome != iwire.SelectionReadOutcomeFound && read.Outcome != iwire.SelectionReadOutcomeUnknown {
		return nil, iclient.SelectionReadV2{}, fmt.Errorf("the chat default cannot be read: %s", read.Outcome)
	}
	return operator, read, nil
}

// stepSelects reports whether the read policy puts family on this
// computer first.
func stepSelects(read iclient.SelectionReadV2, family string) bool {
	return read.Outcome == iwire.SelectionReadOutcomeFound && read.Policy != nil && len(read.Policy.Candidates) > 0 &&
		read.Policy.Candidates[0].Model == family && read.Policy.Candidates[0].Placement == iwire.CandidatePlacementLocal
}

// stepChatPolicy puts family on this computer first and keeps the person's
// other candidates after it, up to the policy's eight.
func stepChatPolicy(read iclient.SelectionReadV2, family string) iclient.SelectionPolicyV2 {
	return localFirstPolicy(chatPolicy, read, family)
}

// imagePolicy is the account default selection policy read by picture apps
// and written by the Models page's Use for pictures action.
const imagePolicy = "oa-image-default"

// setupImageDefault makes model on this computer the first candidate of the
// account's picture policy, keeping the person's other candidates after it.
// Setup calls it once, when the starter picture model the Install click
// approved becomes ready. Tests replace it.
var setupImageDefault = func(ctx context.Context, model string) error {
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	read, err := operator.ReadAccountDefault(ctx, imagePolicy)
	if err != nil {
		return err
	}
	if read.Outcome != iwire.SelectionReadOutcomeFound && read.Outcome != iwire.SelectionReadOutcomeUnknown {
		return fmt.Errorf("the picture default cannot be read: %s", read.Outcome)
	}
	if stepSelects(read, model) {
		return nil
	}
	change, err := operator.PutAccountDefault(ctx, read.Revision, localFirstPolicy(imagePolicy, read, model))
	if err != nil {
		return err
	}
	if change.Outcome != iwire.EditOutcomeApplied {
		return fmt.Errorf("the picture default was not written: %s %s", change.Outcome, change.Reason)
	}
	return nil
}

// localFirstPolicy is policy name with model on this computer first and the
// person's other candidates after it, up to the policy's eight.
func localFirstPolicy(name string, read iclient.SelectionReadV2, family string) iclient.SelectionPolicyV2 {
	policy := iclient.SelectionPolicyV2{Name: name, Candidates: []iclient.SelectionCandidateV2{{Model: family, Placement: iwire.CandidatePlacementLocal}}}
	if read.Policy != nil {
		for _, c := range read.Policy.Candidates {
			if len(policy.Candidates) == 8 {
				break
			}
			if c.Model == family && c.Placement == iwire.CandidatePlacementLocal && c.Provider == "" && c.Credential == "" {
				continue
			}
			policy.Candidates = append(policy.Candidates, c)
		}
	}
	return policy
}

// stepInventory is the runtime's storage inventory as the step reads it:
// Find by digest, List for same-size candidates, and Holders, which
// re-reads one manifest through its source's Verify and so asks the source
// to hash that manifest's weights (LS-13). Tests replace it.
type stepInventory interface {
	Find(ctx context.Context, digest string) (content.InventoryPage, error)
	List(ctx context.Context, continuation string, limit int64) (content.InventoryPage, error)
	Holders(ctx context.Context, target string) (content.InventoryPage, error)
}

var stepResolveInventory = func(ctx context.Context) (stepInventory, error) {
	return panelMachine().ResolveStorageInventory(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// stepFindPinned answers the step's lookup for plan's pinned file: the
// inventory object holding it, or, when the inventory lists weights files of
// the pinned size with no digest yet, Hashing after asking it to hash
// exactly those (Holders on each id). An inventory that cannot be read is an
// error, which leaves the download.
func stepFindPinned(ctx context.Context, plan setup.ModelPlan, inventory stepInventory) (setup.StepLookup, error) {
	if len(plan.Assets) != 1 {
		return setup.StepLookup{}, nil
	}
	page, err := inventory.Find(ctx, "sha256:"+plan.Assets[0].Download.SHA256)
	if err != nil {
		return setup.StepLookup{}, err
	}
	if page.Outcome != content.InventoryOutcomePage {
		return setup.StepLookup{}, fmt.Errorf("storage inventory is %s", page.Outcome.String())
	}
	if local, locator := setup.PinnedEntry(plan, page.Manifests); local != nil {
		// Adopted only where no other account can change it (trustedpath,
		// LS-12's check); otherwise Setup downloads its own copy.
		path, err := stepStorePath(ctx, inventory, local.Store, locator)
		if err != nil || stepTrustFile(path) != nil {
			return setup.StepLookup{Untrusted: true}, nil
		}
		return setup.StepLookup{Local: local}, nil
	}
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		page, err := inventory.List(ctx, continuation, 256)
		if err != nil {
			return setup.StepLookup{}, err
		}
		if page.Outcome != content.InventoryOutcomePage {
			return setup.StepLookup{}, fmt.Errorf("storage inventory is %s", page.Outcome.String())
		}
		if candidates := setup.SameSizeCandidates(plan, page.Manifests); len(candidates) > 0 {
			for _, id := range candidates {
				//unchecked: Holders only asks the source to hash; a refusal leaves the candidate unhashed and the bounded wait offers the download
				_, _ = inventory.Holders(ctx, id)
			}
			return setup.StepLookup{Hashing: true}, nil
		}
		if page.Complete || page.Continuation == "" || page.Continuation == continuation {
			break
		}
		continuation = page.Continuation
	}
	return setup.StepLookup{}, nil
}

// stepTrustFile is the trust rule an adopted file must pass: it and every
// folder above it are owned by this account or the machine's own
// principals, and no other account can change them (trustedpath, as LS-12
// reads ASUS's folders). Tests replace it.
var stepTrustFile = func(path string) error {
	owners := trustedpath.MachineOwners()
	owners.Account = true
	return trustedpath.CheckFile(path, owners)
}

// stepStorePath is the file locator names in store, from the store
// locations the inventory's first List page carries.
func stepStorePath(ctx context.Context, inventory stepInventory, store, locator string) (string, error) {
	page, err := inventory.List(ctx, "", 1)
	if err != nil {
		return "", err
	}
	for _, s := range page.Stores {
		if s.Name == store && s.Locator != "" && filepath.IsAbs(s.Locator) {
			return filepath.Join(s.Locator, filepath.FromSlash(locator)), nil
		}
	}
	return "", fmt.Errorf("the inventory names no folder for store %s", store)
}

// stepHostInPlace hosts the inventory object the answer adopted, where it
// lies, under name: the bundled engine is staged once, and ModelHost
// declares that object, as it declares one in Setup's own model folder.
// Nothing is copied. The object hosted under another name is declared
// again under name.
func (p *servicePanel) stepHostInPlace(ctx context.Context, local setup.StepLocal, engineArgs []string, name string) error {
	approved := func(string) bool { return true }
	home, programs, pin, err := p.modelHostInstalled(ctx, true)
	if err != nil {
		return err
	}
	if !p.modelHostEngineState(ctx, home, programs.ModelHost, pin).Staged {
		if _, _, err := p.modelHostStageBundled(ctx, approved); err != nil {
			return err
		}
	}
	engine := p.modelHostEngineState(ctx, home, programs.ModelHost, pin)
	if !engine.Staged {
		return errors.New("the bundled engine is not staged")
	}
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return err
	}
	if stepHostedAs(hosted, local.Store, local.Object, name) {
		return nil
	}
	if _, err := p.modelHostOpenAbstractions(ctx, modelHostHostArgs(local.Store, local.Object, engine.Path, programs.ModelHost, engineArgs, name)...); err != nil {
		return fmt.Errorf("the model host declaration was not written: %w", err)
	}
	if hosted, err = p.modelHostHosted(ctx); err != nil {
		return err
	}
	for _, item := range hosted {
		if item.Store == local.Store && item.Object == local.Object {
			return nil
		}
	}
	return errors.New("the runtime did not retain the hosted model declaration")
}

// stepHost runs the existing ModelHost Setup operations for the downloaded
// coding model: stage the bundled engine, register its folder, host it
// under hosting's name and context. The person's one answer approved them;
// each is idempotent.
func (p *servicePanel) stepHost(ctx context.Context, controller setup.ModelController, hosting setup.StepHosting) error {
	approved := func(string) bool { return true }
	home, programs, pin, err := p.modelHostInstalled(ctx, true)
	if err != nil {
		return err
	}
	if !p.modelHostEngineState(ctx, home, programs.ModelHost, pin).Staged {
		if _, _, err := p.modelHostStageBundled(ctx, approved); err != nil {
			return err
		}
	}
	root, err := p.modelHostRegisterRoot(ctx, controller.StageDir, approved)
	if err != nil {
		return err
	}
	plan := controller.ReviewedPlan()
	return stepHostDownloaded(ctx, plan, root, hosting, p.stepHostedContent,
		func(model modelHostChatModel, digest string) error {
			return p.modelHostHostModel(ctx, root.Store, model.ID, approved, plan.EngineArgs, hosting.Name, digest)
		})
}

func stepHostDownloaded(ctx context.Context, plan setup.ModelPlan, root modelHostRootState, hosting setup.StepHosting,
	hosted func(context.Context, string, string, string, string) (bool, error),
	host func(modelHostChatModel, string) error) error {
	if len(plan.Assets) != 1 {
		return errors.New("the coding model plan names no single file")
	}
	want := "sha256:" + plan.Assets[0].Download.SHA256
	selected := map[string]bool{}
	for _, object := range stepDownloadedObjects(plan, root.Store, root.Models) {
		selected[object.Object] = true
	}
	for _, m := range root.Models {
		if !selected[m.ID] {
			continue
		}
		if m.Hosted {
			pinned, err := hosted(ctx, root.Store, m.ID, hosting.Name,
				want+":"+strconv.FormatInt(plan.Assets[0].Download.Bytes, 10))
			if err != nil || pinned {
				return err
			}
		}
		return host(m, want)
	}
	if root.Reason != "" {
		return errors.New(root.Reason)
	}
	return errors.New("the downloaded model is not in the model folder's inventory yet")
}

func (p *servicePanel) stepHostedContent(ctx context.Context, store, object, name, content string) (bool, error) {
	raw, err := p.modelHostOpenAbstractions(ctx, "models", "hosts", "--json", "--timeout", "30s")
	if err != nil {
		return false, err
	}
	var hosted []struct{ Store, Object, Name, Content string }
	if err := json.Unmarshal(raw, &hosted); err != nil {
		return false, err
	}
	for _, model := range hosted {
		if model.Store == store && model.Object == object && (name == "" || model.Name == name) && model.Content == content {
			return true, nil
		}
	}
	return false, nil
}

// stepHosting reports whether ModelHost hosts the step's model now, read
// from its hosted list: the adopted copy by its inventory object, or the
// downloaded file by the same digest or pinned name stepHost hosts. The
// Models page's Remove from OA unhosts either one.
func (p *servicePanel) stepHosting(ctx context.Context, controller setup.ModelController, state setup.StepState) (bool, error) {
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return false, err
	}
	objects, err := p.stepModelObjects(ctx, controller, state)
	if err != nil {
		return false, err
	}
	for _, object := range objects {
		if stepHostedObject(hosted, object.Store, object.Object) {
			return true, nil
		}
	}
	return false, nil
}

// stepModelObjects lists the inventory objects that hold the step's model:
// the adopted copy, or the downloaded file in Setup's model folder.
func (p *servicePanel) stepModelObjects(ctx context.Context, controller setup.ModelController, state setup.StepState) ([]setup.StepLocal, error) {
	if state.Adopted != nil {
		return []setup.StepLocal{*state.Adopted}, nil
	}
	if p.stepModelFolder != nil {
		store, models, err := p.stepModelFolder(ctx)
		if err != nil {
			return nil, err
		}
		return stepDownloadedObjects(controller.ReviewedPlan(), store, models), nil
	}
	home, programs, pin, err := p.modelHostInstalled(ctx, false)
	if err != nil {
		return nil, err
	}
	engine := p.modelHostEngineState(ctx, home, programs.ModelHost, pin)
	root, err := p.modelHostSavedRoot(ctx, home, programs, pin, engine.Path)
	if err != nil || root == nil {
		return nil, err
	}
	return stepDownloadedObjects(controller.ReviewedPlan(), root.Store, root.Models), nil
}

// stepDownloadedObjects is the downloaded file among models of Setup's model
// folder store: the pinned digest, or the pinned file name stepHost hosts.
func stepDownloadedObjects(plan setup.ModelPlan, store string, models []modelHostChatModel) []setup.StepLocal {
	if len(plan.Assets) != 1 {
		return nil
	}
	want := "sha256:" + plan.Assets[0].Download.SHA256
	staged := plan.Assets[0].DigestStageName()
	var objects []setup.StepLocal
	for _, m := range models {
		if m.Digest == want || identity.SamePathName(m.Name, staged) {
			objects = append(objects, setup.StepLocal{Store: store, Object: m.ID})
		}
	}
	return objects
}

// recordStepRemoval keeps the person's Remove from OA in the step journal
// when the object holds the model the step hosted, so Setup's Finish after
// an update leaves it removed (owner, 2026-11-01). The Models page records
// before it unhosts: a record whose unhost then fails is cleared by the
// step's next run, which finds the model still hosted. An error means the
// record could not be written, and the page removes nothing. A Panel whose
// Setup keeps no state for its runtime (setup.UseNoState) has no step to
// record for.
func (p *servicePanel) recordStepRemoval(ctx context.Context, store, object string) error {
	if setup.NoState() {
		return nil
	}
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		return err
	}
	step := setup.SetupStep{JournalPath: journal}
	state, err := step.Load(ctx)
	if err != nil || !state.HostedModel() {
		return err
	}
	controller, err := p.codingModelController()
	if err != nil {
		return err
	}
	objects, err := p.stepModelObjects(ctx, controller, state)
	if err != nil {
		return err
	}
	if !slices.Contains(objects, setup.StepLocal{Store: store, Object: object}) {
		return nil
	}
	return step.RecordRemoval(ctx)
}

// stepNamed reports whether ModelHost hosts the step's model under the
// catalogue's family the journal records: every hosted copy of the model
// carries that name. A model hosted nowhere reads as named; stepHosting
// reports that case.
func (p *servicePanel) stepNamed(ctx context.Context, controller setup.ModelController, state setup.StepState) (bool, error) {
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return false, err
	}
	objects, err := p.stepModelObjects(ctx, controller, state)
	if err != nil {
		return false, err
	}
	for _, object := range objects {
		if stepHostedObject(hosted, object.Store, object.Object) && !stepHostedAs(hosted, object.Store, object.Object, state.Family) {
			return false, nil
		}
	}
	return true, nil
}

// stepHostedAs reports whether hosted declares the object under name; an
// empty name accepts the object under any name.
func stepHostedAs(hosted []struct {
	Name   string `json:"Name"`
	Store  string `json:"Store"`
	Object string `json:"Object"`
}, store, object, name string) bool {
	for _, item := range hosted {
		if item.Store == store && item.Object == object && (name == "" || item.Name == name) {
			return true
		}
	}
	return false
}

func stepHostedObject(hosted []struct {
	Name   string `json:"Name"`
	Store  string `json:"Store"`
	Object string `json:"Object"`
}, store, object string) bool {
	for _, item := range hosted {
		if item.Store == store && item.Object == object {
			return true
		}
	}
	return false
}

// stepView is the Settings row's established state. Every field reads as a
// sentence; none is empty (owner rule: no empty fields). Label names the
// registered apps the row sets up.
type stepView struct {
	Label       string `json:"label"`
	Model       string `json:"model"`
	SignIn      string `json:"sign_in"`
	ChatDefault string `json:"chat_default"`
	Action      string `json:"action"`
	Ready       bool   `json:"ready"`
}

func stepViewOf(state setup.StepState, subject setup.StepTitles, signIn *applications.SignInStart, signInErr error, chatSelected bool, chatErr error) stepView {
	v := stepView{Label: stepLabel(subject), Model: "Not set up yet", SignIn: "Starts when an app is set up", ChatDefault: "Apps choose their own model", Action: "Set up"}
	// The answer's work in one phrase: the chat model, the picture app's
	// pictures or both.
	pictures := "pictures"
	if subject.Pictures != "" {
		pictures = "pictures for " + subject.Pictures
	}
	work := state.Title
	if state.Pictures {
		work = "P" + pictures[1:]
		if state.Title != "" {
			work = state.Title + " and " + pictures
		}
	}
	switch {
	case state.Ready() && state.PicturesStopped:
		// Chat works; pictures keep their own sentence and retry (A3).
		v.Model, v.Action, v.Ready = state.Title+" on this computer; "+pictures+" stopped", "Try pictures again", true
	case state.Ready():
		v.Model, v.Action, v.Ready = work+" on this computer", "Check", true
	case state.Approved():
		v.Model, v.Action = work+": setting up", "Show progress"
	case state.Declined():
		v.Model = "Not set up; you chose Not now"
	}
	switch {
	case signInErr != nil || signIn == nil:
		v.SignIn = "Sign-in start cannot be read now"
	case signIn.Runtime && signIn.Presenter:
		v.SignIn = "Starts at sign-in"
	case signIn.Wanted:
		v.SignIn = "Starting at sign-in is being turned on"
	}
	switch {
	case chatErr != nil:
		v.ChatDefault = "The chat default cannot be read now"
	case chatSelected:
		v.ChatDefault = "Chat default for apps"
	case state.Ready() && state.Title != "":
		v.ChatDefault = "Not the chat default for apps"
		v.Action = "Make default"
	}
	return v
}

// stepLabel names what the row sets up for the registered apps.
func stepLabel(subject setup.StepTitles) string {
	switch {
	case subject.Chat != "" && subject.Pictures != "":
		return "Coding model for " + subject.Chat + " and pictures for " + subject.Pictures
	case subject.Chat != "":
		return "Coding model for " + subject.Chat
	case subject.Pictures != "":
		return "Pictures for " + subject.Pictures
	}
	return "Coding model and pictures for apps"
}

// Outcomes the step's owners know, which the person reads as sentences.
var (
	errStepFit = errors.New("the model catalogue did not answer")
	// errStepQuestionClosed: the session locked while the question was
	// open, which dismissed it with no answer.
	errStepQuestionClosed = errors.New("the question closed when this computer locked")
)

// stepFailureNotice words the known outcomes for the person about the apps
// titled who, with no raw error text, hash or path. A lock is a neutral
// pointer to Settings.
func stepFailureNotice(err error, who string) (setup.StepNotice, bool) {
	stopped := "Couldn't finish setup"
	if who != "" {
		stopped = "Couldn't finish setting up " + who
	}
	switch {
	case errors.Is(err, errStepQuestionClosed):
		title := "Set up from Models"
		if who != "" {
			title = "Set up " + who + " from Models"
		}
		return setup.StepNotice{Title: title, Body: "The question closed when this computer locked. Open Models and choose Set up."}, true
	case errors.Is(err, errStepFit):
		return setup.StepNotice{Title: stopped, Body: "OpenAbstractions cannot tell now which model fits this computer. Open Models and choose Set up."}, true
	}
	return setup.StepNotice{}, false
}

// recordStepStopped writes a stopped run to OA logging as the Panel, with
// the apps it served, its stage, its sentence and the error behind it.
func (p *servicePanel) recordStepStopped(ctx context.Context, stopped setup.StepStopped, who string) error {
	return p.log.record(ctx, logging.LevelWarn, "setup step stopped", map[string]string{"panel.action": "setup.step",
		"setup.step.apps": who, "setup.step.stage": stopped.Stage, "setup.step.reason": stopped.Reason, "setup.step.detail": stopped.Detail})
}

// stepClickSentence is the one sentence the Settings row writes on click,
// chosen from the row's own action as it was shown before the launch.
func stepClickSentence(action string, open bool) string {
	switch {
	case open:
		return "Catalog is already open."
	case action == "Show progress":
		return "Continuing…"
	case action == "Make default":
		return "Making it the chat default…"
	case action == "Check":
		return "Checking…"
	case action == "Try pictures again":
		return "Trying pictures again…"
	}
	return "Answer the question in the window that opened."
}

// stepInstalledOnly is the row's sentence and the click's answer on a Panel
// that serves an explicit runtime: the --setup process binds only the
// installed runtime and this account's own step journal.
const stepInstalledOnly = "Setup runs only on the installed OpenAbstractions."

// setupStepLaunch starts the --setup process; a test replaces it so no step
// process starts.
var setupStepLaunch = launchSetupStep

// setupStepState answers the Settings row (GET) and starts the step (POST).
// The step runs in its own --setup process, so the Panel can close. A Panel
// serving an explicit runtime starts nothing (as the presenter's resume and
// request watch, tray_windows.go).
func (p *servicePanel) setupStepState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		view := p.readStepView(ctx)
		// The row says Setup runs only on the installed OpenAbstractions where
		// Setup has no state to run with: an explicit runtime without its
		// state folder. With that folder the row reads its own step journal
		// there, as the Setup page does; the click below still starts nothing.
		if panelExplicit != nil && panelExplicit.StateDir == "" {
			view.Model, view.Action, view.Ready = stepInstalledOnly, "Set up", false
		}
		panelJSON(w, view)
	case http.MethodPost:
		if panelExplicit != nil {
			panelJSON(w, struct {
				Message string `json:"message"`
			}{stepInstalledOnly})
			return
		}
		// The row is read once, before the launched step can change it.
		action := p.readStepView(ctx).Action
		release, free, err := setupStepClaim()
		if err != nil {
			http.Error(w, "Setup cannot start now.", http.StatusConflict)
			return
		}
		if free {
			release()
			if err := setupStepLaunch(); err != nil {
				http.Error(w, "Setup cannot start now.", http.StatusConflict)
				return
			}
		}
		panelJSON(w, struct {
			Message string `json:"message"`
		}{stepClickSentence(action, !free)})
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

func (p *servicePanel) readStepState(ctx context.Context) setup.StepState {
	var state setup.StepState
	if journal, err := setup.DefaultStepJournal(); err == nil {
		state, _ = setup.SetupStep{JournalPath: journal}.Load(ctx)
	}
	return state
}

func (p *servicePanel) readStepView(ctx context.Context) stepView {
	state := p.readStepState(ctx)
	signIn, signInErr := readSignInStart(ctx)
	_, read, chatErr := stepChatDefault(ctx)
	return stepViewOf(state, p.readStepTitles(ctx), signIn, signInErr, chatErr == nil && state.Family != "" && stepSelects(read, state.Family), chatErr)
}

// readStepTitles reads the registered apps' titles for the row; an
// unreadable Applications leaves the row's general words.
func (p *servicePanel) readStepTitles(ctx context.Context) setup.StepTitles {
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return setup.StepTitles{}
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return setup.StepTitles{}
	}
	return setup.TitlesOf(stepApps(page, setup.NewCatalog().StepChoices(), ""))
}

func readSignInStart(ctx context.Context) (*applications.SignInStart, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	result, err := applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadSignInStart()
	if err != nil {
		return nil, err
	}
	if result.Outcome != applications.OutcomePage || result.State == nil {
		return nil, fmt.Errorf("sign-in start: %s %s", result.Outcome, result.Reason)
	}
	return result.State, nil
}

// launchSetupStep starts the Panel's --setup mode in the person's own
// session, detached from the caller.
func launchSetupStep() error {
	self, err := panelExecutable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "--setup")
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runSetupStep is the --setup mode: one run of the step with the native
// question, then exit. One step runs per session. Its failure is one
// notice, never an error box at each sign-in.
// setupStepClaim is the session's one-step claim the run takes; a test
// replaces it so no run reaches the account's installed runtime.
var setupStepClaim = claimSetupStep

// runSetupStep runs one step. With finish (Setup's Finish), open reports that
// the caller opens the Panel window: after every run in which the person
// answered no question and no approved work ran, including a run that stopped
// or could not start and a step another process already runs (preview-1031l:
// a Finish run ended with nothing on screen).
func runSetupStep(ask func(context.Context, setup.StepQuestion) (bool, error), requester *setup.StepRequester, finish bool, update ...bool) (open bool, err error) {
	release, acquired, err := setupStepClaim()
	if err != nil {
		return false, err
	}
	if !acquired {
		return finish, nil
	}
	defer release()
	p := new(servicePanel)
	step, err := p.setupStep(ask)
	step.Requester = requester
	step.Finish = finish
	step.Update = finish && len(update) != 0 && update[0]
	if err != nil {
		return finish, stepNotify(context.Background(), setup.StepNotice{Title: "Couldn't finish setup", Body: "Setup couldn't start. Open Models and choose Set up.", Tag: "setup"})
	}
	step.Owners.Notify = presenterFirst(launchPresenter, stepNotify)
	step.Owners.Failure = stepFailureNotice
	step.Owners.Record = p.recordStepStopped
	outcome, err := step.RunAndReport(context.Background())
	p.maintainSetupExtensions(context.Background())
	drain, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.log.close(drain)
	return finish && outcome.FinishOpensPanel(err), err
}

// presenterFirst starts the background presenter once before the first
// notice, so the first session of a per-machine install shows progress and
// Ready. A running presenter keeps its claim and the new one exits. It
// writes no sign-in entry.
func presenterFirst(start func() error, notify func(context.Context, setup.StepNotice) error) func(context.Context, setup.StepNotice) error {
	var once sync.Once
	return func(ctx context.Context, n setup.StepNotice) error {
		once.Do(func() {
			if err := start(); err != nil {
				log.Printf("start the background presenter: %v", err)
			}
		})
		return notify(ctx, n)
	}
}

// resumeSetupStep starts the step again when approved work is unfinished:
// the background presenter calls it at sign-in.
func resumeSetupStep(ctx context.Context, launch func() error) error {
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		return err
	}
	pending, err := (setup.SetupStep{JournalPath: journal}).Pending(ctx)
	if err != nil || !pending {
		return err
	}
	return launch()
}

// setupStepRow is the row of the established setup (VISION L1339): the
// model, sign-in start and the chat default in one caption, and Set up or
// Change, which starts the same step. It shows state on arrival. It sits in
// the Models page's Used by your apps (backlog/P-J1a.md, Models page).
var setupStepRow = `<div class="oa-row"><div class="oa-row-text"><span class="oa-row-label" id="setup-step-label">Coding model and pictures for apps</span><span class="oa-row-caption" id="setup-step-caption">Checking…</span></div><div class="oa-row-control"><button id="setup-step-action" type="button">Set up</button></div>` + oaStatusLine("setup-step-status", false) + `</div>`

// setupStepSection is the Settings row of the established setup: the same
// label and caption, and a link to Models, where Set up and Change are.
var setupStepSection = `<section id="setup-step" class="oa-group"><h2>Models for apps</h2><div class="oa-row"><div class="oa-row-text"><span class="oa-row-label" id="setup-step-label">Coding model and pictures for apps</span><span class="oa-row-caption" id="setup-step-caption">Checking…</span></div><div class="oa-row-control"><a id="setup-step-models" href="/inference">Open Models</a></div>` + oaStatusLine("setup-step-status", false) + `</div></section>`

// setupStepRead reads /setup/step into the row's label and caption; it
// needs the page's call and el.
const setupStepRead = `
async function setupStepLoad(){try{const v=await call('/setup/step');el('setup-step-label').textContent=v.label;el('setup-step-caption').textContent=String(v.model).replace(/[.]$/,'');if(el('setup-step-action'))el('setup-step-action').textContent=v.action;oaSetStatus('setup-step-status','idle','')}catch(e){oaShowError('setup-step-status',e)}}
var setupStepReady=setupStepLoad();
`

// setupStepScript reads /setup/step on arrival and starts the step on
// click (the Models page).
const setupStepScript = setupStepRead + `el('setup-step-action').onclick=async()=>{try{const r=await call('/setup/step',{});oaSetStatus('setup-step-status','ok',r.message)}catch(e){oaShowError('setup-step-status',e)}};
`

// setupStepLinkScript reads the row on arrival and points its link at the
// Models page's Used by your apps (Settings).
const setupStepLinkScript = setupStepRead + `el('setup-step-models').href='/inference?k='+encodeURIComponent(key||'')+'#selection-section';
`
