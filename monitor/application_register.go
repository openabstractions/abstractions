package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

// An application's own installer starts the Panel with the application's
// Facade application manifest (`--register-application <file>`) or its name
// (`--remove-application <name>`); the person confirms on this generic page
// and the Panel registers or removes the entry with its own
// application.manage (VISION 2026-10-06). Nothing changes without the click,
// and every text the page shows comes from the manifest file or the
// registered entry, escaped as untrusted text.

// applicationRequest is the one change an installer asked this Panel run for.
type applicationRequest struct {
	mu sync.Mutex
	// register is the manifest to register; remove is the name to remove.
	// Exactly one is set until the person confirms or cancels.
	register *fwire.ApplicationDescriptor
	remove   string
	// done is the sentence the confirm or cancel wrote; a request is settled
	// once.
	done string
}

// readApplicationManifest reads and checks the installer's manifest file
// before anything is shown.
func readApplicationManifest(path string) (fwire.ApplicationDescriptor, error) {
	file, err := os.Open(path)
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("the application manifest could not be read: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, client.MaxApplicationDescriptorBytes+1))
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("the application manifest could not be read: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fwire.ApplicationDescriptor{}, err
	}
	localAppData, pictures, err := accountFolders()
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("this account's folders could not be read: %w", err)
	}
	folders := manifestFolders{"app": filepath.Dir(absolute), "local_app_data": localAppData, "pictures": pictures}
	return client.ParseApplicationDescriptorResolving(data, folders.resolve)
}

// reservedTitleRefusal is the one sentence the Panel gives an installer's
// manifest whose title is a product name (reservedTitle): an app may not
// read as OpenAbstractions in the Panel or the native prompt.
const reservedTitleRefusal = "This app's title is a name OpenAbstractions uses for itself, so the Panel does not register it."

// applicationDigest identifies one manifest the person saw; "" is none.
func applicationDigest(descriptor *fwire.ApplicationDescriptor) string {
	if descriptor == nil {
		return ""
	}
	raw, _ := json.Marshal(descriptor)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// registeredApplication reads the entry registered under name. readable is
// false when the Panel cannot list the directory.
func registeredApplication(ctx context.Context, apps setupApplications, name string) (entry *fwire.ApplicationDescriptor, readable bool) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return nil, false
	}
	for _, listed := range page.Applications {
		if listed.Descriptor.Name == name {
			found := listed.Descriptor
			return &found, true
		}
	}
	return nil, true
}

// applicationDetails renders one manifest as escaped rows.
func applicationDetails(heading string, d *fwire.ApplicationDescriptor) string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<div class="oa-app-manifest"><h2>` + e(heading) + `</h2><dl>`)
	row := func(label, value string) {
		b.WriteString(`<dt>` + e(label) + `</dt><dd>` + value + `</dd>`)
	}
	row("Title", e(d.Title))
	row("Name", `<code>`+e(d.Name)+`</code>`)
	row("Program", `<code>`+e(d.Program)+`</code>`)
	if d.StartGuidance != "" {
		row("Start guidance", e(d.StartGuidance))
	}
	if d.Activation == nil {
		row("Start", "OpenAbstractions cannot start it.")
	} else {
		var args strings.Builder
		for _, argument := range d.Activation.Arguments {
			args.WriteString(`<code>` + e(argument) + `</code> `)
		}
		if len(d.Activation.Arguments) == 0 {
			args.WriteString("none")
		}
		row("Start arguments", args.String())
		r := d.Activation.Readiness
		row("Ready when it announces", `<code>`+e(r.Name)+`</code> (`+e(r.Protocol)+`, `+e(r.Contract)+`) within `+
			strconv.FormatInt(d.Activation.ReadinessTimeoutMs, 10)+` ms`)
	}
	b.WriteString(`</dl></div>`)
	return b.String()
}

// applicationButtons are the confirm and cancel buttons of one request. The
// confirm button carries the digest of the registered entry the page showed;
// the Panel refuses the click when the entry changed since.
func applicationButtons(base, verb, shown string) string {
	return `<p><button class="app-action" data-path="` + base + `/confirm" data-confirm="true" data-shown="` +
		html.EscapeString(shown) + `">` + verb + `</button> <button class="app-action" data-path="` + base +
		`/cancel" data-confirm="false" data-shown="">Cancel</button></p>`
}

const applicationPageScript = `<script>
const key=new URLSearchParams(location.search).get('k');
const buttons=document.querySelectorAll('.app-action');
async function settle(button){const status=document.getElementById('app-status');
for(const b of buttons)b.disabled=true;
try{const r=await fetch(button.dataset.path,{method:'POST',headers:{'X-Panel-Key':key,'Content-Type':'application/json'},
body:JSON.stringify({confirm:button.dataset.confirm==='true',shown:button.dataset.shown})});
const text=await r.text();if(!r.ok)throw Error(text);status.textContent=JSON.parse(text).message}
catch(e){oaShowError('app-status',e);for(const b of buttons)b.disabled=false}}
for(const b of buttons)b.addEventListener('click',()=>settle(b));
</script>`

// applicationRegisterPage is the confirmation for the installer's manifest.
func (p *servicePanel) applicationRegisterPage(key string) http.HandlerFunc {
	return guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		request := &p.appRequest
		request.mu.Lock()
		pending, done := request.register, request.done
		request.mu.Unlock()
		var b strings.Builder
		b.WriteString(`<h1>Register an application</h1>`)
		switch {
		case done != "":
			b.WriteString(`<p>` + html.EscapeString(done) + `</p>`)
		case pending == nil:
			b.WriteString(`<p>No installer asked this window to register an application.</p>`)
		case reservedTitle(pending.Title):
			b.WriteString(`<p>` + html.EscapeString(reservedTitleRefusal) + `</p>`)
			b.WriteString(applicationDetails("Application", pending))
			b.WriteString(`<p><button class="app-action" data-path="/application/register/cancel" data-confirm="false" data-shown="">Close</button></p>`)
		default:
			ctx, cancel := panelCall(r)
			defer cancel()
			var existing *fwire.ApplicationDescriptor
			readable := false
			if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
				existing, readable = registeredApplication(ctx, apps, pending.Name)
			}
			b.WriteString(`<p>An installer asks to add this application to OpenAbstractions. Registering grants it no permissions; it asks for those when it needs them.</p>`)
			if existing != nil && applicationDigest(existing) != applicationDigest(pending) {
				b.WriteString(`<p>An application with this name is already registered. Registering replaces it.</p>`)
				b.WriteString(applicationDetails("Registered now", existing))
				b.WriteString(applicationDetails("Replacement", pending))
			} else {
				b.WriteString(applicationDetails("Application", pending))
			}
			if !readable {
				b.WriteString(`<p>The Panel cannot list the registered applications, so it cannot show an entry this would replace.</p>`)
			}
			b.WriteString(applicationButtons("/application/register", "Register", applicationDigest(existing)))
		}
		b.WriteString(`<p id="app-status" role="status"></p>` + applicationPageScript)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, panelPage(r, key, "application", "OpenAbstractions application", b.String()))
	})
}

// applicationRemovePage is the confirmation for the installer's removal.
func (p *servicePanel) applicationRemovePage(key string) http.HandlerFunc {
	return guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		request := &p.appRequest
		request.mu.Lock()
		name, done := request.remove, request.done
		request.mu.Unlock()
		var b strings.Builder
		b.WriteString(`<h1>Remove an application</h1>`)
		switch {
		case done != "":
			b.WriteString(`<p>` + html.EscapeString(done) + `</p>`)
		case name == "":
			b.WriteString(`<p>No installer asked this window to remove an application.</p>`)
		default:
			ctx, cancel := panelCall(r)
			defer cancel()
			var existing *fwire.ApplicationDescriptor
			readable := false
			if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
				existing, readable = registeredApplication(ctx, apps, name)
			}
			b.WriteString(`<p>An uninstaller asks to remove this application's entry from OpenAbstractions. Removing the entry changes no file; the application's own files are its uninstaller's to remove.</p>`)
			if existing != nil {
				b.WriteString(applicationDetails("Registered now", existing))
			} else {
				b.WriteString(`<p>Name: <code>` + html.EscapeString(name) + `</code></p>`)
				if readable {
					b.WriteString(`<p>No application is registered under this name.</p>`)
				}
			}
			b.WriteString(applicationButtons("/application/remove", "Remove", applicationDigest(existing)))
		}
		b.WriteString(`<p id="app-status" role="status"></p>` + applicationPageScript)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, panelPage(r, key, "application", "OpenAbstractions application", b.String()))
	})
}

type applicationSettle struct {
	Confirm bool   `json:"confirm"`
	Shown   string `json:"shown"`
}

type applicationSettled struct {
	Message string `json:"message"`
}

func decodeApplicationSettle(w http.ResponseWriter, r *http.Request) (applicationSettle, bool) {
	var body applicationSettle
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return body, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "one confirmation is required", http.StatusBadRequest)
		return body, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one confirmation is required", http.StatusBadRequest)
		return body, false
	}
	return body, true
}

// settle records the request's one outcome and clears what was pending.
func (a *applicationRequest) settle(message string) applicationSettled {
	a.register, a.remove, a.done = nil, "", message
	return applicationSettled{Message: message}
}

func (p *servicePanel) applicationRegisterCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := decodeApplicationSettle(w, r); !ok {
		return
	}
	request := &p.appRequest
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.register == nil {
		http.Error(w, "no registration is waiting", http.StatusConflict)
		return
	}
	panelJSON(w, request.settle("Nothing was registered."))
}

func (p *servicePanel) applicationRemoveCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := decodeApplicationSettle(w, r); !ok {
		return
	}
	request := &p.appRequest
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.remove == "" {
		http.Error(w, "no removal is waiting", http.StatusConflict)
		return
	}
	panelJSON(w, request.settle("Nothing was removed."))
}

// applicationConflict is a confirmation the Panel refused, in one sentence:
// the request stays as it was. Any other error is a service failure.
type applicationConflict string

func (c applicationConflict) Error() string { return string(c) }

// applicationReviewAgain is the conflict that keeps the registration waiting
// for a fresh review of an entry the Panel could not list before.
const applicationReviewAgain applicationConflict = "an application is already registered under this name; reload to review it before replacing it"

// applicationChanged refuses a confirmation whose registered entry changed
// after the person saw it; the request waits for a fresh review.
const applicationChanged applicationConflict = "the registered entry changed since this page was shown; reload and review it again"

// writeApplicationOutcome answers one confirmation over HTTP.
func writeApplicationOutcome(w http.ResponseWriter, settled applicationSettled, err error) {
	var conflict applicationConflict
	switch {
	case errors.As(err, &conflict):
		http.Error(w, string(conflict), http.StatusConflict)
	case err != nil:
		panelError(w, err)
	default:
		panelJSON(w, settled)
	}
}

// applicationRegisterConfirm registers the manifest after the person's click.
func (p *servicePanel) applicationRegisterConfirm(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeApplicationSettle(w, r)
	if !ok {
		return
	}
	if !body.Confirm {
		http.Error(w, "the Register click is the confirmation; it was not sent", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	settled, err := p.confirmApplicationRegister(ctx, body.Shown)
	writeApplicationOutcome(w, settled, err)
}

// confirmApplicationRegister registers the waiting manifest after the
// person's confirmation, in the Panel page or the native question panel. The
// entry the person saw (shown, its digest) must still be the one registered;
// a replacement removes it and registers the manifest.
func (p *servicePanel) confirmApplicationRegister(ctx context.Context, shown string) (applicationSettled, error) {
	request := &p.appRequest
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.register == nil {
		return applicationSettled{}, applicationConflict("no registration is waiting")
	}
	pending := *request.register
	if reservedTitle(pending.Title) {
		return applicationSettled{}, applicationConflict(reservedTitleRefusal)
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return applicationSettled{}, err
	}
	existing, _ := registeredApplication(ctx, apps, pending.Name)
	if applicationDigest(existing) != shown {
		return applicationSettled{}, applicationChanged
	}
	if existing != nil && applicationDigest(existing) == applicationDigest(&pending) {
		return request.settle(pending.Title + " is already registered."), nil
	}
	if existing != nil {
		change, err := apps.Remove(ctx, pending.Name)
		if err != nil {
			return applicationSettled{}, err
		}
		if change.Outcome != fwire.ApplicationOutcomeApplied {
			return applicationSettled{}, applicationConflict("the registered entry was not replaced (" + change.Outcome.String() + "); it was kept")
		}
	}
	change, err := apps.Register(ctx, pending)
	if err == nil && change.Outcome == fwire.ApplicationOutcomeApplied {
		// The person's click also lets the Panel list what they registered
		// (application_read.go); the registration grants nothing itself.
		message := "Registered " + pending.Title + "."
		if err := p.applicationReads().allowPanelRead(ctx, pending.Name); err != nil {
			message += " The Panel could not record that it may list it, so Apps does not show it: " + err.Error()
		}
		return request.settle(message), nil
	}
	reason := "the application was not registered"
	if err != nil {
		reason += ": " + err.Error()
	} else {
		reason += " (" + change.Outcome.String() + ")"
	}
	if existing == nil {
		if err == nil && change.Outcome == fwire.ApplicationOutcomeConflict {
			// An entry the Panel may not list holds the name: one registered
			// before the Panel wrote read rules, or one whose read the person
			// revoked. The person's click asked for this name in the Panel, so
			// the Panel lets itself list it and keeps the request waiting for
			// a review of both entries.
			if p.applicationReads().allowPanelRead(ctx, pending.Name) == nil {
				return applicationSettled{}, applicationReviewAgain
			}
			reason = "another application is registered under this name and the Panel cannot read it; nothing was changed"
		}
		return applicationSettled{}, applicationConflict(reason)
	}
	// The earlier entry was removed for the replacement; put it back.
	restored, restoreErr := apps.Register(ctx, *existing)
	if restoreErr == nil && restored.Outcome == fwire.ApplicationOutcomeApplied {
		return applicationSettled{}, applicationConflict(reason + "; the earlier entry " + existing.Title + " is registered again")
	}
	return applicationSettled{}, applicationConflict(reason + "; the earlier entry " + existing.Title + " was removed and could not be registered again")
}

// applicationRemoveConfirm removes the named entry after the person's click.
func (p *servicePanel) applicationRemoveConfirm(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeApplicationSettle(w, r)
	if !ok {
		return
	}
	if !body.Confirm {
		http.Error(w, "the Remove click is the confirmation; it was not sent", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	settled, err := p.confirmApplicationRemove(ctx, body.Shown)
	writeApplicationOutcome(w, settled, err)
}

// confirmApplicationRemove removes the named entry after the person's
// confirmation, through the same Applications client registration uses.
func (p *servicePanel) confirmApplicationRemove(ctx context.Context, shown string) (applicationSettled, error) {
	request := &p.appRequest
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.remove == "" {
		return applicationSettled{}, applicationConflict("no removal is waiting")
	}
	name := request.remove
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return applicationSettled{}, err
	}
	existing, _ := registeredApplication(ctx, apps, name)
	if applicationDigest(existing) != shown {
		return applicationSettled{}, applicationChanged
	}
	change, err := apps.Remove(ctx, name)
	if err != nil {
		return applicationSettled{}, err
	}
	var message string
	switch change.Outcome {
	case fwire.ApplicationOutcomeApplied:
		label := name
		if existing != nil {
			label = existing.Title
		}
		message = "Removed " + label + "."
	case fwire.ApplicationOutcomeUnknown:
		message = "No application was registered under this name."
	default:
		return applicationSettled{}, applicationConflict("the application was not removed (" + change.Outcome.String() + ")")
	}
	// The confirmed removal also clears the Panel's read rule on the name.
	if err := p.applicationReads().clearPanelRead(ctx, name); err != nil {
		message += " The Panel's permission to list it could not be removed: " + err.Error()
	}
	return request.settle(message), nil
}

func (p *servicePanel) applicationRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/application/register", p.applicationRegisterPage(key))
	mux.HandleFunc("/application/register/confirm", guard(key, p.applicationRegisterConfirm))
	mux.HandleFunc("/application/register/cancel", guard(key, p.applicationRegisterCancel))
	mux.HandleFunc("/application/remove", p.applicationRemovePage(key))
	mux.HandleFunc("/application/remove/confirm", guard(key, p.applicationRemoveConfirm))
	mux.HandleFunc("/application/remove/cancel", guard(key, p.applicationRemoveCancel))
}

// applicationStart is what the command line asked of this run: a manifest to
// register or a name to remove, already checked.
type applicationStart struct {
	register *fwire.ApplicationDescriptor
	remove   string
}

// parseApplicationStart checks --register-application and
// --remove-application before any window opens, and names the start page
// they select. A malformed manifest is refused here and never shown.
func parseApplicationStart(manifest, remove, page string) (applicationStart, string, error) {
	switch {
	case manifest == "" && remove == "":
		if page == "register-application" || page == "remove-application" {
			return applicationStart{}, "", errors.New("the application page needs --register-application <file> or --remove-application <name>")
		}
		return applicationStart{}, page, nil
	case manifest != "" && remove != "":
		return applicationStart{}, "", errors.New("give --register-application or --remove-application, not both")
	case manifest != "":
		descriptor, err := readApplicationManifest(manifest)
		if err != nil {
			return applicationStart{}, "", err
		}
		return applicationStart{register: &descriptor}, "register-application", nil
	default:
		if !validApplicationName(remove) {
			return applicationStart{}, "", errors.New("the application name to remove is not a registered-name shape")
		}
		return applicationStart{remove: remove}, "remove-application", nil
	}
}

// applicationProbeProgram is a clean absolute path on this platform, used only
// to check a name against the manifest rule.
var applicationProbeProgram = func() string {
	if runtime.GOOS == "windows" {
		return `C:\probe.exe`
	}
	return "/probe"
}()

// validApplicationName holds a removal's name to the manifest's name rule.
func validApplicationName(name string) bool {
	probe := fwire.ApplicationDescriptor{Name: name, Program: applicationProbeProgram, Title: "x"}
	return client.ValidApplicationDescriptor(probe) == nil
}
