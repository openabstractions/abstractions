package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rclient "github.com/openabstractions/abstraction-router/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// The Models page's model list (backlog/P-J1a.md, Models page, 2026-11-01):
// one row per model on this computer, joined from the two owners that know
// about models. The storage inventory knows the files: their size and the
// folder that holds them. The router knows what can run them: which engine
// serves a name, for which uses, and which names an engine has loaded. The
// route reads both with the panel's own identity and reports each owner's
// refusal separately, so a slow or refused inventory still leaves the
// router's models on the page and the other way round.

// libraryInventoryWait bounds the inventory read. The first read after the
// runtime starts walks every model folder; a read that outlasts it answers
// Reading, and the page asks again. Tests shorten it.
var libraryInventoryWait = 15 * time.Second

// libraryReadCatalog reads the router's model families with stored-only
// files. Tests replace it.
var libraryReadCatalog = func(ctx context.Context) (rclient.ModelsSnapshot, error) {
	router, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return rclient.ModelsSnapshot{}, err
	}
	return router.ModelsContext(ctx, true)
}

// libraryReadCard reads the resource table the Device page shows, for the
// graphics memory each running engine holds. Tests replace it.
var libraryReadCard = func(ctx context.Context) ([]cardResourceState, error) {
	return readCardResources(ctx, false)
}

// libraryReadPrograms names each declared provider's programs by the
// provider's name: the declared program, which holds a native model
// server's card lease, then the engine its "--engine" argument starts, which
// the resource table measures. Tests replace it.
var libraryReadPrograms = func(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	for _, state := range readProviderDeclarations(ctx) {
		d := state.Declaration
		programs := []string{d.Program}
		for i := 0; i+1 < len(d.Arguments); i++ {
			if d.Arguments[i] == "--engine" && d.Arguments[i+1] != "" {
				programs = append(programs, d.Arguments[i+1])
			}
		}
		out[d.Name] = programs
	}
	return out
}

// libraryModel is one model on this computer: its family, the uses an
// engine serves it for, and every copy the inventory found.
type libraryModel struct {
	// EngineGuide is the router's engine guide in words for the row's
	// Technical details; RunsOn names each engine it reports, which the
	// row's caption reads by upstream name (architect ruling 2026-11-09,
	// second block).
	EngineGuide []string           `json:"engine_guide"`
	RunsOn      []string           `json:"runs_on,omitempty"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Title       string             `json:"title,omitempty"`
	Names       []string           `json:"names"`
	Uses        []string           `json:"uses"`
	Ready       bool               `json:"ready"`
	Running     bool               `json:"running"`
	Engines     []string           `json:"engines"`
	Size        int64              `json:"size"`
	Places      []libraryPlace     `json:"places"`
	Objects     []libraryObject    `json:"objects"`
	Components  []libraryComponent `json:"components,omitempty"`
	// Context is the Context chooser of a model OA's model host runs.
	Context *libraryContext `json:"context,omitempty"`
}

type libraryComponent struct {
	Name  string       `json:"name"`
	Role  string       `json:"role"`
	Place libraryPlace `json:"place"`
}

// libraryPlace is a folder that holds a copy: a short name for page text,
// and the full location for the row's title.
type libraryPlace struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// libraryObject names one inventory object of the model, for the actions
// that act on that exact copy (ModelHost host and unhost).
type libraryObject struct {
	Store  string `json:"store"`
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	Hosted bool   `json:"hosted,omitempty"`
}

// libraryRunning is one name an engine has loaded now, and the graphics
// memory the resource table reports for that engine (0 when it reports
// none).
type libraryRunning struct {
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Title  string   `json:"title,omitempty"`
	Engine string   `json:"engine"`
	Uses   []string `json:"uses"`
	Memory int64    `json:"memory"`
}

// libraryView answers /inference/library. Reading is true while the
// inventory's first read of the model folders has not answered. FilesError
// and CatalogError carry each owner's refusal; FilesRefused and
// CatalogRefused mark a permission refusal, which the page states as such.
type libraryView struct {
	Models         []libraryModel   `json:"models"`
	Running        []libraryRunning `json:"running"`
	Reading        bool             `json:"reading"`
	FilesKnown     bool             `json:"files_known"`
	FilesError     string           `json:"files_error,omitempty"`
	FilesRefused   bool             `json:"files_refused,omitempty"`
	CatalogKnown   bool             `json:"catalog_known"`
	CatalogError   string           `json:"catalog_error,omitempty"`
	CatalogRefused bool             `json:"catalog_refused,omitempty"`
}

// libraryFile is one model manifest the inventory listed.
type libraryFile struct {
	Family string
	Name   string
	Store  string
	ID     string
	Size   int64
}

// libraryFiles is one complete inventory listing: the model files and the
// folders (stores) that hold them. Hosted names, for each store/object
// ModelHost hosts, the name apps ask for it by: the router lists the model
// under that name, so a file whose own name says nothing about the model,
// such as Setup's download stored under its SHA-256, joins the family it is
// served as.
type libraryFiles struct {
	Files  []libraryFile
	Stores map[string]content.Store
	Hosted map[string]string
}

// libraryHostedKey is the Hosted key of one inventory object.
func libraryHostedKey(store, id string) string { return store + "/" + id }

// libraryReadHosted reads ModelHost's hosted list for the join. Tests
// replace it; an error leaves every file under its own family.
var libraryReadHosted = func(ctx context.Context, p *servicePanel) (map[string]string, error) {
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(hosted))
	for _, item := range hosted {
		out[libraryHostedKey(item.Store, item.Object)] = item.Name
	}
	return out, nil
}

func (p *servicePanel) inferenceLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	view := libraryView{Models: []libraryModel{}, Running: []libraryRunning{}}
	catalogDone := make(chan struct{})
	var catalog rclient.ModelsSnapshot
	var catalogErr error
	var card []cardResourceState
	var hosted map[string]string
	var programs map[string][]string
	go func() {
		defer close(catalogDone)
		ctx, cancel := panelCall(r)
		defer cancel()
		catalog, catalogErr = libraryReadCatalog(ctx)
		if catalogErr == nil {
			//unchecked: an unreadable resource table leaves the memory unreported; Running now still lists the models
			card, _ = libraryReadCard(ctx)
			//unchecked: without ModelHost's list each file keeps its own family
			hosted, _ = libraryReadHosted(ctx, p)
			programs = libraryReadPrograms(ctx)
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), libraryInventoryWait)
	files, filesErr := readLibraryFiles(ctx)
	cancel()
	<-catalogDone
	files.Hosted = hosted
	if r.Context().Err() != nil {
		return
	}
	switch {
	case filesErr == nil:
		view.FilesKnown = true
	case errors.Is(filesErr, context.DeadlineExceeded):
		view.Reading = true
	default:
		var refused libraryInventoryRefusal
		view.FilesError = filesErr.Error()
		view.FilesRefused = errors.As(filesErr, &refused) && refused.outcome == content.InventoryOutcomeForbidden
	}
	if catalogErr == nil {
		view.CatalogKnown = true
	} else {
		view.CatalogError, view.CatalogRefused = catalogErr.Error(), libraryRefusal(catalogErr.Error())
	}
	view.Models, view.Running = projectLibrary(catalog, files)
	attachLibraryContexts(view.Models, card)
	for i := range view.Running {
		view.Running[i].Memory = libraryEngineMemory(card, view.Running[i].Engine, programs[view.Running[i].Engine])
	}
	panelJSON(w, view)
}

// libraryEngineMemory is the largest graphics-card amount the table reports
// for the engine's holder: the model server's claim (inference.ResourceHost
// names it) or, for a native provider, its declared programs. A measured
// amount the table attributes to one of them is preferred; otherwise the
// largest reported amount, the lease, stands. Verified and reported
// amounts, and two cards, are never added up.
func libraryEngineMemory(resources []cardResourceState, engine string, programs []string) int64 {
	holder := inference.ResourceHost(engine)
	var most, measured int64
	for _, r := range resources {
		if !strings.HasPrefix(r.Resource, "card:") {
			continue
		}
		for _, h := range r.Holders {
			if !libraryHolderIs(h.Program, holder, programs) {
				continue
			}
			most = max(most, h.Amount)
			if h.Evidence == cardEvidenceVerified {
				measured = max(measured, h.Amount)
			}
		}
	}
	if measured > 0 {
		return measured
	}
	return most
}

func libraryHolderIs(program, holder string, programs []string) bool {
	if program == holder {
		return true
	}
	for _, p := range programs {
		if p != "" && identity.SameSubjectProgram(program, p) {
			return true
		}
	}
	return false
}

// libraryInventoryRefusal is the storage inventory's typed answer other
// than a page.
type libraryInventoryRefusal struct{ outcome content.InventoryOutcome }

func (r libraryInventoryRefusal) Error() string { return "storage inventory " + r.outcome.String() }

// libraryRefusal reports whether the router's error is a permission
// refusal. The router client carries a refusal as the remote error's text,
// with no typed outcome, so its outcome words decide.
func libraryRefusal(text string) bool {
	return strings.Contains(text, "forbidden") || strings.Contains(text, "not permitted") || strings.Contains(text, "not_permitted")
}

// readLibraryFiles lists every model manifest the inventory holds, page by
// page within one snapshot.
func readLibraryFiles(ctx context.Context) (libraryFiles, error) {
	inventory, err := stepResolveInventory(ctx)
	if err != nil {
		return libraryFiles{}, libraryDeadline(ctx, err)
	}
	result := libraryFiles{Stores: map[string]content.Store{}}
	continuation := ""
	for pages := 0; pages < 64; pages++ {
		page, err := inventory.List(ctx, continuation, 256)
		if err != nil {
			return libraryFiles{}, libraryDeadline(ctx, err)
		}
		if page.Outcome != content.InventoryOutcomePage {
			return libraryFiles{}, libraryInventoryRefusal{page.Outcome}
		}
		for _, store := range page.Stores {
			result.Stores[store.Name] = store
		}
		for _, item := range page.Manifests {
			if file, ok := libraryFileOf(item.Manifest); ok {
				result.Files = append(result.Files, file)
			}
		}
		if page.Complete || page.Continuation == "" || page.Continuation == continuation {
			return result, nil
		}
		continuation = page.Continuation
	}
	return result, nil
}

// libraryDeadline reports a read that ran out of time as the deadline, so
// the page says the folders are still being read.
func libraryDeadline(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.DeadlineExceeded
	}
	return err
}

// libraryFileOf reads one manifest as a model file: a model descriptor
// whose weights the store holds. A projector, VAE or other part is folded
// into its model by the router and is not a row of its own.
func libraryFileOf(m content.Manifest) (libraryFile, bool) {
	if m.Kind != "abstraction.model/descriptor@1" || m.SupersededBy != "" {
		return libraryFile{}, false
	}
	if role := m.Descriptor["role"]; role != "" && role != "weights" {
		return libraryFile{}, false
	}
	var size int64
	for _, entry := range m.Entries {
		if entry.Role == "weights" || entry.Role == "" {
			size += entry.Size
		}
	}
	name := ""
	if len(m.Names) > 0 {
		name = m.Names[0].Name
	}
	family := m.Descriptor["family"]
	if family == "" {
		family = name
	}
	if family == "" {
		return libraryFile{}, false
	}
	return libraryFile{Family: family, Name: name, Store: m.Store, ID: m.ID, Size: size}, true
}

// projectLibrary joins the router's families with the inventory's files by
// family. A family is on this computer when a store holds it or an engine
// on this computer serves it; a model only a hosted API serves is not.
func projectLibrary(catalog rclient.ModelsSnapshot, files libraryFiles) ([]libraryModel, []libraryRunning) {
	models := []libraryModel{}
	running := []libraryRunning{}
	index := map[string]int{}
	for _, family := range catalog.Models {
		m := libraryModel{Key: family.Family, Name: family.Family, Names: []string{}, Uses: []string{}, Engines: []string{}, Places: []libraryPlace{}, Objects: []libraryObject{}}
		if family.Title != "" {
			m.Name, m.Title = family.Title, family.Title
		}
		for _, object := range family.Objects {
			for _, engine := range object.Engines {
				m.EngineGuide = appendNew(m.EngineGuide, engine.Host+" / "+engine.Engine)
				if engine.Engine != "" {
					m.RunsOn = appendNew(m.RunsOn, engine.Engine)
				} else if engine.Host != "" {
					m.RunsOn = appendNew(m.RunsOn, engine.Host)
				}
			}
			if object.Why != "" {
				m.EngineGuide = appendNew(m.EngineGuide, object.Why)
			}
		}
		if len(m.EngineGuide) == 0 {
			m.EngineGuide = []string{"Engine association is unknown."}
		}
		stores := slices.Clone(family.HeldIn)
		for _, component := range family.Components {
			m.Components = append(m.Components, libraryComponent{Name: component.Name, Role: component.Role, Place: libraryPlaceOf(component.Store, files.Stores[component.Store])})
		}
		local := false
		for _, alias := range family.Names {
			stores = append(stores, alias.HeldIn...)
			m.Names = appendNew(m.Names, alias.Name)
			if m.Name == "" {
				m.Name = alias.Name
			}
			if !alias.Servable || alias.Host == "" {
				continue
			}
			m.Ready = true
			local = local || !alias.Hosted
			m.Engines = appendNew(m.Engines, alias.Host)
			for _, use := range alias.Profiles {
				m.Uses = appendNew(m.Uses, use)
			}
			if alias.Resident {
				m.Running = true
				running = append(running, libraryRunning{Key: family.Family, Name: m.Name, Title: m.Title, Engine: alias.Host, Uses: slices.Clone(alias.Profiles)})
			}
		}
		if m.Key == "" || (len(stores) == 0 && !local) {
			continue
		}
		for _, store := range stores {
			m.Places = appendPlace(m.Places, files.Stores, store)
		}
		index[m.Key] = len(models)
		models = append(models, m)
	}
	// The family the router lists each served name under.
	served := map[string]string{}
	for _, family := range catalog.Models {
		for _, alias := range family.Names {
			if alias.Host != "" {
				served[alias.Name] = family.Family
			}
		}
	}
	for _, file := range files.Files {
		if family, ok := served[files.Hosted[libraryHostedKey(file.Store, file.ID)]]; ok && family != "" {
			file.Family = family
		}
		i, ok := index[file.Family]
		if !ok {
			i = len(models)
			index[file.Family] = i
			models = append(models, libraryModel{Key: file.Family, Name: file.Family, Names: []string{}, Uses: []string{}, Engines: []string{}, Places: []libraryPlace{}, Objects: []libraryObject{}})
		}
		m := &models[i]
		m.Names = appendNew(m.Names, file.Name)
		_, hosted := files.Hosted[libraryHostedKey(file.Store, file.ID)]
		m.Objects = append(m.Objects, libraryObject{Store: file.Store, ID: file.ID, Size: file.Size, Hosted: hosted})
		m.Size = max(m.Size, file.Size)
		m.Places = appendPlace(m.Places, files.Stores, file.Store)
	}
	slices.SortStableFunc(models, func(a, b libraryModel) int { return strings.Compare(a.Key, b.Key) })
	return models, running
}

func appendNew(list []string, value string) []string {
	if value == "" || slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

// appendPlace adds the folder a store names, once.
func appendPlace(places []libraryPlace, stores map[string]content.Store, name string) []libraryPlace {
	if name == "" {
		return places
	}
	place := libraryPlaceOf(name, stores[name])
	for _, p := range places {
		if p == place {
			return places
		}
	}
	return append(places, place)
}

// libraryPlaceOf names a store's folder for page text: its last two folder
// names ("OpenAbstractions › models"), since a full path is never page
// text; the full location stays on the row's title. A store another
// computer holds reads as such. A store the inventory listed without a
// location reads by its own name.
func libraryPlaceOf(name string, store content.Store) libraryPlace {
	if store.Placement == content.PlacementRemote {
		return libraryPlace{Name: "another computer", Path: store.Domain}
	}
	locator := strings.TrimRight(store.Locator, `\/`)
	if locator == "" || !strings.ContainsAny(locator, `\/`) {
		return libraryPlace{Name: name, Path: store.Locator}
	}
	parts := strings.FieldsFunc(locator, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) > 0 && strings.HasSuffix(parts[0], ":") {
		parts = parts[1:]
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	if len(parts) == 0 {
		return libraryPlace{Name: name, Path: store.Locator}
	}
	return libraryPlace{Name: strings.Join(parts, " › "), Path: store.Locator}
}
