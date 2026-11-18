package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// A model the person adds the way they already do, in another app that keeps
// a model folder on this computer, becomes available the way the shipped
// model is (owner, 2026-11-01): the model host loads it where it lies, under the card lease,
// named by its family. The runtime owns the model host's declaration, and
// this reconciler keeps its allowlist (MODELHOST-L2, exhaustive) in step with
// what the storage inventory holds:
//
//   - every chat-capable GGUF of a store on this machine that is no component
//     (a projector or VAE stays with its base, MODEL-C3) is one entry, named
//     by its descriptor family;
//   - one entry per family and per digest; an entry already declared, such as
//     Setup's verified copy, keeps its place and its name, and an entry
//     named by a file digest takes its family's name (plainHostedNames);
//   - an entry whose file left a store the inventory read completely is
//     dropped; a store the inventory could not read keeps its entries;
//   - a model the person removed from OA stays out until they or an app host
//     it again (VISION 2026-11-01): the registry records each entry a
//     declaration edit removes and clears it when an edit adds it back
//     (modelHostRemovals), and the reconciler skips what that record holds.
//
// Hosting is not loading: a declared model starts an engine only when a call
// asks for it, under the card lease, as before. Rewriting the declaration
// restarts the model host, so the reconciler waits while the model host
// holds its card (a loaded engine), and writes on a later refresh.

// discoveredModel is one chat-capable GGUF a store on this machine holds.
type discoveredModel struct {
	Family string
	Store  string
	Object string
	Digest string
	// catalogueDigest is present only for this listing's singleton weights
	// whose hashed digest and size match the curated catalogue exactly.
	catalogueDigest string
	// Rank orders the stores two copies of one family sit in: OA's own
	// folders first, then the order the inventory reports its stores in.
	Rank int
}

// nonChatArchitectures are GGUF architectures llama.cpp serves for
// something other than chat: encoders and embedding models, and the vision
// projector's own architecture.
var nonChatArchitectures = map[string]bool{
	"bert": true, "nomic-bert": true, "nomic-bert-moe": true, "jina-bert-v2": true, "jina-bert-v3": true,
	"t5encoder": true, "clip": true, "wavtokenizer-dec": true, "neo-bert": true, "modern-bert": true,
}

// discoveredModels reads the composed inventory: the stores it read
// completely, and the chat-capable GGUFs they hold. A store with errors, one
// that is absent or one on another machine is not read completely, and
// nothing it holds is discovered or dropped.
func discoveredModels(composed storageservice.ComposedInventory) (map[string]bool, []discoveredModel) {
	complete := map[string]bool{}
	for _, s := range composed.Stores {
		if s.Placement == content.PlacementLocal && s.Present && len(s.Errors) == 0 && s.Domain == "" {
			complete[s.Name] = true
		}
	}
	rank := storeRanks(composed.Stores)
	catalogue := codeCatalogue(context.Background())
	var found []discoveredModel
	for _, mh := range composed.Manifests {
		m := mh.Manifest
		if !complete[m.Store] || m.Kind != modelDescriptor || !chatGGUF(m.Descriptor) {
			continue
		}
		// A family read from a file named by its digest names no model to a
		// person; the descriptor's architecture and size label do, the
		// way `models host` names such an object (plainHostedName).
		d := discoveredModel{Family: plainHostedName(m.Descriptor["family"], m.Descriptor), Store: m.Store, Object: m.ID, Rank: rank[m.Store]}
		if entry, ok := catalogueModel(m, catalogue); ok {
			d.catalogueDigest = entry.Digest
		}
		for _, e := range m.Entries {
			if e.Evidence != content.EvidenceNone && e.Digest != "" {
				d.Digest = e.Digest
				break
			}
		}
		found = append(found, d)
	}
	return complete, found
}

// chatGGUF reports whether a descriptor names GGUF weights of a chat model:
// a family, an architecture its header named, no component role.
func chatGGUF(d map[string]string) bool {
	if d["format"] != "gguf" || d["family"] == "" || d["architecture"] == "" || nonChatArchitectures[d["architecture"]] {
		return false
	}
	role := d["role"]
	return role == "" || role == "weights"
}

// ownStorePrefix begins the names of OA's own model folders, the stores
// Setup registers ("oa-chat-<id>"), which hold Setup's verified copies.
const ownStorePrefix = "oa-"

// storeRanks orders the stores of a composition for a family two of them
// hold under different files: OA's own folders first, then the order the
// inventory reports its stores in.
func storeRanks(stores []content.Store) map[string]int {
	rank := map[string]int{}
	for i, s := range stores {
		rank[s.Name] = len(stores) + i
		if strings.HasPrefix(s.Name, ownStorePrefix) {
			rank[s.Name] = i
		}
	}
	return rank
}

// modelHostRemoval is one entry a declaration edit removed from the model
// host: the name callers asked for and the object it served.
type modelHostRemoval struct {
	Name   string `json:"name"`
	Store  string `json:"store"`
	Object string `json:"object"`
}

// excludes reports whether a removal keeps d out: the same family name, or
// the same object.
func (r modelHostRemoval) excludes(d discoveredModel) bool {
	return r.Name == d.Family || (r.Store == d.Store && r.Object == d.Object)
}

// reconcileHosted is the allowlist the inventory asks for: current, less the
// entries whose file left a store read completely, plus one entry per newly
// discovered family and digest the removals do not hold. listed is every
// object the inventory lists, by store/object, with its digest ("" when it
// published none).
func reconcileHosted(current []hostedModel, complete map[string]bool, found []discoveredModel, removed []modelHostRemoval, listed map[string]string) []hostedModel {
	current = plainHostedNames(preferCatalogueCopies(current, found, removed), found)
	var next []hostedModel
	names, objects, digests := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range current {
		key := m.Store + "/" + m.Object
		digest, still := listed[key]
		if complete[m.Store] && !still {
			// The file left a store the inventory read completely.
			continue
		}
		next = append(next, m)
		names[m.Name], objects[key] = true, true
		if digest != "" {
			digests[digest] = true
		}
	}
	candidates := slices.Clone(found)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Store != b.Store {
			return a.Store < b.Store
		}
		return a.Object < b.Object
	})
	for _, d := range candidates {
		if names[d.Family] || objects[d.Store+"/"+d.Object] || (d.Digest != "" && digests[d.Digest]) {
			continue
		}
		if slices.ContainsFunc(removed, func(r modelHostRemoval) bool { return r.excludes(d) }) {
			continue
		}
		next = append(next, hostedModel{Name: d.Family, Store: d.Store, Object: d.Object})
		names[d.Family], objects[d.Store+"/"+d.Object] = true, true
		if d.Digest != "" {
			digests[d.Digest] = true
		}
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Name < next[j].Name })
	return next
}

// hostedByID binds each entry to the manifest id the inventory lists for its
// object. `models host` before 2026-11-11 kept the object as the person
// named it, by a file name or an entry's locator; the store lists the
// manifest by id, and the reconciler read such an entry as a file that left
// and dropped the person's alias with it. An entry naming exactly one
// manifest of its store, by one of its names or locators, takes that id and
// keeps its name, content pin and place; any other entry stays as it is.
func hostedByID(current []hostedModel, composed storageservice.ComposedInventory) []hostedModel {
	ids := map[string]bool{}
	for _, mh := range composed.Manifests {
		ids[mh.Manifest.Store+"/"+mh.Manifest.ID] = true
	}
	next := slices.Clone(current)
	for i, m := range next {
		if ids[m.Store+"/"+m.Object] {
			continue
		}
		match := ""
		for _, mh := range composed.Manifests {
			if mh.Manifest.Store != m.Store || !hostedManifestNamed(mh.Manifest, m.Object) {
				continue
			}
			if match != "" && match != mh.Manifest.ID {
				match = ""
				break
			}
			match = mh.Manifest.ID
		}
		if match != "" {
			next[i].Object = match
		}
	}
	return next
}

// preferCatalogueCopies rebinds existing aliases only between proven copies
// of identical catalogue bytes. Every alias keeps its name and policy meaning.
// Named/unknown evidence and variants with different bytes keep their records.
func preferCatalogueCopies(current []hostedModel, found []discoveredModel, removed []modelHostRemoval) []hostedModel {
	byObject := map[string]discoveredModel{}
	best := map[string]discoveredModel{}
	for _, d := range found {
		if d.catalogueDigest == "" {
			continue
		}
		byObject[d.Store+"/"+d.Object] = d
		if slices.ContainsFunc(removed, func(r modelHostRemoval) bool { return r.excludes(d) }) {
			continue
		}
		previous, ok := best[d.catalogueDigest]
		if !ok || d.Rank < previous.Rank || d.Rank == previous.Rank && (d.Store < previous.Store || d.Store == previous.Store && d.Object < previous.Object) {
			best[d.catalogueDigest] = d
		}
	}
	next := slices.Clone(current)
	for i, m := range next {
		d, known := byObject[m.Store+"/"+m.Object]
		preferred, available := best[d.catalogueDigest]
		if known && available {
			next[i].Store, next[i].Object = preferred.Store, preferred.Object
		}
	}
	return next
}

// plainHostedNames repairs entries declared under a file digest, such as
// Setup's coding model before 2026-11-01 (55983c5a….gguf): callers, the
// listing and the engine's own replies then named a digest (R34 live test
// D4). An entry whose object another entry already serves under a plain
// name is dropped. Any other one takes the plain name the inventory's
// descriptor gives its object (plainHostedName), unless an entry already
// has that name. An entry with no plain name to take stays as it is.
func plainHostedNames(current []hostedModel, found []discoveredModel) []hostedModel {
	names, plain := map[string]bool{}, map[string]bool{}
	for _, m := range current {
		names[m.Name] = true
		if !digestModelName.MatchString(m.Name) {
			plain[m.Store+"/"+m.Object] = true
		}
	}
	next := make([]hostedModel, 0, len(current))
	for _, m := range current {
		key := m.Store + "/" + m.Object
		if digestModelName.MatchString(m.Name) {
			if plain[key] {
				continue
			}
			i := slices.IndexFunc(found, func(d discoveredModel) bool { return d.Store == m.Store && d.Object == m.Object })
			if i >= 0 && found[i].Family != "" && !digestModelName.MatchString(found[i].Family) && !names[found[i].Family] {
				m.Name = found[i].Family
				names[m.Name], plain[key] = true, true
			}
		}
		next = append(next, m)
	}
	return next
}

// listedObjects maps every manifest the inventory lists, by store/object,
// to its digest ("" when it published none), so an entry whose object is
// still listed is told from one whose file left.
func listedObjects(composed storageservice.ComposedInventory) map[string]string {
	out := map[string]string{}
	for _, mh := range composed.Manifests {
		m := mh.Manifest
		digest := ""
		for _, e := range m.Entries {
			if e.Evidence != content.EvidenceNone && e.Digest != "" {
				digest = e.Digest
				break
			}
		}
		out[m.Store+"/"+m.ID] = digest
	}
	return out
}

// sameHosted reports whether two allowlists name the same entries.
func sameHosted(a, b []hostedModel) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(list []hostedModel) []string {
		out := make([]string, 0, len(list))
		for _, m := range list {
			out = append(out, m.argument())
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

// modelHostRemovalsFile is the runtime's record of the models the person
// removed from the model host, beside its other declaration records.
const modelHostRemovalsFile = "modelhost-removed.json"

// modelHostRemovals reads the record and the bytes it was read from.
func (p *runtimeProviders) modelHostRemovals() ([]modelHostRemoval, []byte, error) {
	if p.removedPath == "" {
		return nil, nil, nil
	}
	raw, err := os.ReadFile(p.removedPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []modelHostRemoval
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, err
	}
	return out, raw, nil
}

// recordModelHostEdit keeps the record in step with one declaration edit a
// person or an app made through the registry: each entry it removed is
// recorded, and each entry it added clears the removals it matches. The
// reconciler's own edits never pass through here.
func (p *runtimeProviders) recordModelHostEdit(ctx context.Context, before, after []hostedModel) error {
	removed, raw, err := p.modelHostRemovals()
	if err != nil {
		return err
	}
	next := slices.Clone(removed)
	has := func(list []hostedModel, m hostedModel) bool {
		return slices.ContainsFunc(list, func(o hostedModel) bool { return o.Store == m.Store && o.Object == m.Object })
	}
	for _, m := range after {
		if has(before, m) {
			continue
		}
		next = slices.DeleteFunc(next, func(r modelHostRemoval) bool {
			return r.Name == m.Name || (r.Store == m.Store && r.Object == m.Object)
		})
	}
	for _, m := range before {
		if has(after, m) {
			continue
		}
		r := modelHostRemoval{Name: m.Name, Store: m.Store, Object: m.Object}
		if !slices.Contains(next, r) {
			next = append(next, r)
		}
	}
	if slices.Equal(next, removed) {
		return nil
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Name+"\x00"+next[i].Store < next[j].Name+"\x00"+next[j].Store })
	var value any = next
	if len(next) == 0 {
		value = nil
	}
	return providerMutator(ctx, nil)(p.removedPath, raw, value)
}

// hostedIn is the allowlist a declaration of the model host names; nil for
// any other declaration.
func hostedIn(d providerDeclaration) []hostedModel {
	if d.Name != modelHostName {
		return nil
	}
	_, models := readServeArguments(d.Arguments)
	return models
}

// reconcileModelHost brings the model host's declaration in step with what
// the inventory holds. It writes only to a model host already declared with
// an engine, never withdraws it, and waits while busy reports the model host
// holding its card. It answers what it wrote, for the report.
func (p *runtimeProviders) reconcileModelHost(ctx context.Context, composed storageservice.ComposedInventory, busy func(program, resource string) bool) (string, error) {
	p.mu.Lock()
	files, revision, err := p.read()
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(files, func(f providerFile) bool { return f.Declaration.Name == modelHostName })
	if i < 0 || files[i].Disabled || files[i].Source != declarationSourceOperator {
		return "", nil
	}
	existing := files[i]
	list := wire.DeclarationList{Outcome: wire.DeclarationListOutcomePage, Revision: revision}
	for _, f := range files {
		if !f.Disabled {
			list.Declarations = append(list.Declarations, wire.DeclarationState{Declaration: f.Declaration.wire()})
		}
	}
	settings, current := readHostDeclaration(list)
	if settings.engine == "" || settings.program == "" {
		return "", nil
	}
	removed, _, err := p.modelHostRemovals()
	if err != nil {
		return "", fmt.Errorf("model host removals: %w", err)
	}
	complete, found := discoveredModels(composed)
	next := reconcileHosted(hostedByID(current, composed), complete, found, removed, listedObjects(composed))
	if len(next) == 0 || sameHosted(current, next) {
		return "", nil
	}
	if busy != nil && busy(settings.program, ResourceCard(settings.cardNumber())) {
		return "", nil
	}
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		return "", err
	}
	settings.configs = hostStoreConfigs(list, settings.configs, next)
	d := declarationOf(modelHostDeclaration(settings, next, programs))
	d.SupportedExtensions = existing.Declaration.SupportedExtensions
	if field := validProviderDeclaration(d); field != "" {
		return "", fmt.Errorf("model host declaration: invalid %s", field)
	}
	file := providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: existing.DeclaredBy, DeclaredAt: time.Now().UnixMilli()}
	if existing.ProviderFacet != nil {
		// The runtime changes only the allowlist of the registered facet and
		// keeps the rest, config_keys, engines and recipes included; the
		// registry then carries the person's Keep (facade REG-CFG2).
		_, part, err := legacyRegistrationSpec(file)
		if err != nil || len(part.Providers) != 1 {
			return "", fmt.Errorf("model host declaration: allowlist: %w", err)
		}
		facet := allowlistFacet(*existing.ProviderFacet, part.Providers[0], settings.engine, next)
		file = providerFacetCompatibility(d.Program, facet)
		file.Declaration.SupportedExtensions = existing.Declaration.SupportedExtensions
		file.DeclaredBy, file.DeclaredAt = existing.DeclaredBy, time.Now().UnixMilli()
		file.runtimeAllowlist = true
		if field := validProviderDeclaration(file.Declaration); field != "" {
			return "", fmt.Errorf("model host declaration: invalid %s", field)
		}
	}
	change := p.replace(revision, file)
	if change.Outcome.String() != "applied" {
		return "", fmt.Errorf("model host declaration: %s %s", change.Outcome, change.Reason)
	}
	return hostedChange(current, next), nil
}

// hostedChange reports one reconcile for the log: the names added, the names
// dropped, and the names kept on another object.
func hostedChange(current, next []hostedModel) string {
	var added, dropped, rebound []string
	named := func(list []hostedModel, name string) bool {
		return slices.ContainsFunc(list, func(o hostedModel) bool { return o.Name == name })
	}
	for _, m := range next {
		switch {
		case slices.ContainsFunc(current, func(o hostedModel) bool { return o.argument() == m.argument() }):
		case named(current, m.Name):
			rebound = append(rebound, m.Name)
		default:
			added = append(added, m.Name)
		}
	}
	for _, m := range current {
		if !slices.ContainsFunc(next, func(o hostedModel) bool { return o.argument() == m.argument() }) && !named(next, m.Name) {
			dropped = append(dropped, m.Name)
		}
	}
	if len(rebound) > 0 {
		return fmt.Sprintf("added %v, dropped %v, rebound %v", added, dropped, rebound)
	}
	return fmt.Sprintf("added %v, dropped %v", added, dropped)
}
