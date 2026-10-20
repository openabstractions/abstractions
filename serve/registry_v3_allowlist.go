package main

import (
	"bytes"
	"slices"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	hostdeclaration "github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
)

// allowlistServeFlags are the model host serve flags the runtime derives from
// its own inventory: the hosted-model allowlist (MODELHOST-L2), each model's
// content digest and the store configurations that read those models. facade
// REG-CFG2 lets the runtime's reconcile change only these.
var allowlistServeFlags = map[string]bool{argModel: true, argModelContent: true, argConfig: true}

// splitServeAllowlist reads a model host serve line through walkServeLine
// with every flag the runtime writes (serveFlags), so a value such as an
// --engine-arg that itself looks like a flag stays that flag's value. It
// returns the line without its allowlist entries, the entries as flag and
// value pairs in order, and the index in rest where they belong: where the
// first entry stood, so a line hostArguments wrote keeps its order (the
// video flags follow the models), or after the flags when the line has none.
func splitServeAllowlist(args []string) (rest, entries []string, at int) {
	var known []string
	first := -1
	tail := walkServeLine(args, serveFlags, argEmbed, func(name, value string, inline bool) {
		switch {
		case allowlistServeFlags[name]:
			if first < 0 {
				first = len(known)
			}
			entries = append(entries, name, value)
		case name == argEmbed && inline:
			known = append(known, name+"="+value)
		case name == argEmbed:
			known = append(known, name)
		default:
			known = append(known, name, value)
		}
	})
	if len(args) > 0 && args[0] == "serve" {
		rest = append(rest, "serve")
	}
	at = len(rest) + len(known)
	if first >= 0 {
		at = len(rest) + first
	}
	rest = append(rest, known...)
	return append(rest, tail...), entries, at
}

// allowlistValues is the values entries gives flag.
func allowlistValues(entries []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(entries); i += 2 {
		if entries[i] == flag {
			out = append(out, entries[i+1])
		}
	}
	return out
}

// allowlistFacet is the kept facet with only its allowlist taken from next:
// the allowlist arguments, the scoped models and their recipes. A kept recipe
// stays only while its model object is still the object that model names;
// any other model gets the recipe its configuration derives when the kept
// facet carries engine facts. Every other field, config_keys, engines,
// activation and required extensions included, stays as the kept facet has
// it (REG-CFG2).
func allowlistFacet(kept, next registration.ProviderFacet, engine string, models []hostedModel) registration.ProviderFacet {
	out := cloneProviderFacet(kept)
	rest, _, at := splitServeAllowlist(out.Arguments)
	_, entries, _ := splitServeAllowlist(next.Arguments)
	out.Arguments = slices.Concat(rest[:at], entries, rest[at:])
	out.Models = slices.Clone(next.Models)
	recipes := []registration.ModelRecipe{}
	for _, recipe := range kept.Recipes {
		if slices.ContainsFunc(models, func(m hostedModel) bool { return m.Name == recipe.Model && recipeNames(recipe, m) }) &&
			slices.ContainsFunc(out.Models, func(m registration.ScopedModel) bool { return m.Model == recipe.Model }) {
			recipes = append(recipes, recipe)
		}
	}
	if len(kept.Engines) > 0 {
		configured := make([]hostdeclaration.Model, 0, len(models))
		for _, m := range models {
			configured = append(configured, hostdeclaration.Model{Name: m.Name, Store: m.Store, Object: m.Object})
		}
		_, derived := hostdeclaration.Facts(out, engine, configured)
		for _, recipe := range derived {
			if !slices.ContainsFunc(recipes, func(r registration.ModelRecipe) bool { return r.Model == recipe.Model }) {
				recipes = append(recipes, recipe)
			}
		}
	}
	out.Recipes = recipes
	return out
}

// recipeNames reports whether recipe's model object is m's store and object.
func recipeNames(recipe registration.ModelRecipe, m hostedModel) bool {
	i := slices.IndexFunc(recipe.Objects, func(o registration.RecipeObject) bool { return o.Role == "model" })
	return i >= 0 && recipe.Objects[i].Store == m.Store && recipe.Objects[i].Object == m.Object
}

// allowlistOnly reports whether next differs from kept only in what the
// runtime may change under the kept Keep (REG-CFG2): the hosted models, their
// content digests and recipes, and store configurations the kept facet
// already names. A store configuration the kept facet does not name, an added
// config key, another engine, activation, service, resource or any other
// argument is a change the person Keeps again.
func allowlistOnly(kept, next registration.ProviderFacet) bool {
	_, keptEntries, _ := splitServeAllowlist(kept.Arguments)
	_, nextEntries, _ := splitServeAllowlist(next.Arguments)
	keptConfigs := allowlistValues(keptEntries, argConfig)
	for _, config := range allowlistValues(nextEntries, argConfig) {
		if !slices.Contains(keptConfigs, config) {
			return false
		}
	}
	strip := func(f registration.ProviderFacet) []byte {
		f = cloneProviderFacet(f)
		f.Arguments, _, _ = splitServeAllowlist(f.Arguments)
		f.Models, f.Recipes = []registration.ScopedModel{}, []registration.ModelRecipe{}
		raw, err := registrationEncoded(registration.EncodeProviderFacet, &f)
		if err != nil {
			return nil
		}
		return raw
	}
	a, b := strip(kept), strip(next)
	return a != nil && bytes.Equal(a, b)
}

// cloneProviderFacet is a deep copy through the generated codec.
func cloneProviderFacet(f registration.ProviderFacet) registration.ProviderFacet {
	if raw, err := registrationEncoded(registration.EncodeProviderFacet, &f); err == nil {
		if v, err := registration.DecodeProviderFacet(raw); err == nil {
			return *v
		}
	}
	return f
}
