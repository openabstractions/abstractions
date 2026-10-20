package gateway

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"unicode"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// setupToolKey carries whether this connection lists oa_setup_prepare, so a
// "not set up" sentence names it only to a caller that can call it.
type setupToolKey struct{}

func withSetupTool(ctx context.Context, offered bool) context.Context {
	return context.WithValue(ctx, setupToolKey{}, offered)
}

func setupToolOffered(ctx context.Context) bool {
	offered, _ := ctx.Value(setupToolKey{}).(bool)
	return offered
}

// profileKind is how a sentence names what a profile makes.
func profileKind(profile string) string {
	switch {
	case profile == "video":
		return "video"
	case strings.HasPrefix(profile, "image"):
		return "pictures"
	}
	return "chat"
}

// profileSetupUse is the Setup use (oa_setup_prepare's word) that sets up a
// model for profile.
func profileSetupUse(profile string) string {
	switch {
	case profile == "video":
		return "video"
	case strings.HasPrefix(profile, "image"):
		return "image"
	}
	return "code"
}

// refusalSentence is the one plain sentence, with its next step, for a
// router or engine refusal word the runtime gave for model and profile
// (inference provider.pick: router:<verdict>, engine:unverified:<host>,
// approval:pending and program:changed from the runtime's provider state,
// gpu:standby; a job's carry host:). The machine word
// stays in the result's reason field. "" for a word with no sentence.
func refusalSentence(ctx context.Context, reason, model, profile string) string {
	if strings.Contains(reason, "gpu:standby") {
		return "The computer is going to sleep; try again when it's awake."
	}
	if sentence := startFailedSentence(reason); sentence != "" {
		return sentence
	}
	word := strings.TrimPrefix(reason, "host:")
	name := model
	if offer, ok := setupOfferNamed(model); ok {
		name = offer.title
	}
	switch {
	case word == "router:not-here" || word == "router:no-host":
		return notSetUpSentence(ctx, model, profile)
	case word == "approval:pending":
		return fmt.Sprintf("%s is set up and waits for the person to allow the model host change in the OpenAbstractions question. It starts once they allow it.", name)
	case word == "program:changed":
		return fmt.Sprintf("%s is set up. The program that runs it was updated and waits for the person's OK in the OpenAbstractions question. It starts once they allow it.", name)
	case word == "router:unservable":
		return fmt.Sprintf("%s is on this computer, and no engine here can run it. OpenAbstractions Setup sets up one that can.", name)
	case word == "router:unauthorised":
		return fmt.Sprintf("%s runs only on a server this app may not use. The person can allow it in OpenAbstractions.", name)
	case word == "router:unparseable":
		return fmt.Sprintf("%q names no model. oa_models_list names the models on this computer.", model)
	case strings.HasPrefix(word, "engine:unverified"):
		return fmt.Sprintf("The engine for %s isn't checked on this computer yet. OpenAbstractions Setup checks it.", name)
	case word == "placement:remote":
		return fmt.Sprintf("%s must run on this computer, and only a paired computer was reachable for it.", name)
	}
	return ""
}

// startFailedSentence is the sentence for a provider the runtime stopped
// after failed starts (start_failed:<name>), named by its engine's title; ""
// for any other reason. An engine OpenAbstractions ships fails to start when
// Setup has not set it up on this computer.
func startFailedSentence(reason string) string {
	_, name, found := strings.Cut(reason, "start_failed:")
	if !found || name == "" {
		return ""
	}
	if title, ok := product.EngineTitle(name); ok {
		return fmt.Sprintf("%s isn't set up on this computer. OpenAbstractions Setup sets it up.", title)
	}
	return fmt.Sprintf("The provider %s can't start on this computer. Its log in OpenAbstractions says why.", name)
}

// notSetUpSentence names a model nothing here serves: a reviewed set Setup
// sets up gets its title and download, and the setup tool when this
// connection lists it.
func notSetUpSentence(ctx context.Context, model, profile string) string {
	offer, known := setupOfferNamed(model)
	use := profileSetupUse(profile)
	var sentence string
	switch {
	case known:
		use = offer.use
		sentence = fmt.Sprintf("%s isn't set up on this computer. It can be set up with OpenAbstractions Setup (%s).", offer.title, aboutGB(offer.download))
	case model == "":
		sentence = fmt.Sprintf("Nothing on this computer makes %s yet. OpenAbstractions Setup sets it up.", profileKind(profile))
	default:
		sentence = fmt.Sprintf("%s isn't set up on this computer for %s. oa_models_list names the models here and the ones Setup can set up.", model, profileKind(profile))
	}
	if setupToolOffered(ctx) {
		sentence += fmt.Sprintf(" Call oa_setup_prepare with use %s to ask the person to set it up.", use)
	}
	return sentence
}

// plainRefusal is refusalSentence after reading what this computer serves,
// for a router not-here or no-host word. Those words also come back when
// the model is set up and was asked for through the tool of another
// profile, or by a spelling the router does not resolve (R34: OpenCode
// asked oa_image_generate for ltx-2.5 and was told LTX-2.5 isn't set up).
// The "isn't set up" sentence stays for a model nothing here serves. A
// listing that cannot be read keeps the sentence the word alone gives.
func (o *OA) plainRefusal(ctx context.Context, reason, model, profile string) string {
	word := strings.TrimPrefix(reason, "host:")
	if model != "" && (word == "router:not-here" || word == "router:no-host") {
		if sentence := o.servedSentence(ctx, model, profile); sentence != "" {
			return sentence
		}
	}
	return refusalSentence(ctx, reason, model, profile)
}

func (o *OA) completeChat3Refusal(ctx context.Context, reason, model string) string {
	if sentence := o.visionRefusal(ctx, reason, model); sentence != "" {
		return sentence
	}
	return o.plainRefusal(ctx, reason, model, "chat")
}

func (o *OA) visionRefusal(ctx context.Context, reason, requested string) string {
	if reason != "feature:"+inference.FeatureVision {
		return ""
	}
	sentence := fmt.Sprintf("The model selected for %q does not accept image input.", requested)
	if o.machine == nil {
		return sentence
	}
	c, err := o.machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return sentence
	}
	snapshot, err := c.ModelsContext(ctx, false)
	if err != nil {
		return sentence
	}
	for _, family := range snapshot.Models {
		// Offer an installed model only when every listed local chat route in
		// its family advertises image input. A model name resolves by family.
		alternative := ""
		for _, alias := range family.Names {
			if alias.Host == "" || alias.Hosted || !alias.Servable || !slices.Contains(alias.Profiles, "chat") {
				continue
			}
			if alias.Capabilities == nil || !slices.Contains(alias.Capabilities.Input, "image") || strings.EqualFold(alias.Name, requested) {
				alternative = ""
				break
			}
			if len(alias.Name) <= 256 && (alternative == "" || alias.Name < alternative) {
				alternative = alias.Name
			}
		}
		if alternative != "" {
			return sentence + fmt.Sprintf(" Use %q for image input.", alternative)
		}
	}
	return sentence
}

// servedSentence names how to reach a model this computer serves that a
// call for profile did not reach; "" when nothing here serves it.
func (o *OA) servedSentence(ctx context.Context, asked, profile string) string {
	c, err := o.machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		log.Printf("gateway: refusal for %s: model listing unavailable: %v", asked, err)
		return ""
	}
	snapshot, err := c.ModelsContext(ctx, false)
	if err != nil {
		log.Printf("gateway: refusal for %s: model listing unavailable: %v", asked, err)
		return ""
	}
	m, exact, ok := listedModel(modelsFrom(snapshot.Models), asked)
	if !ok || !m.Servable || len(m.Aliases) == 0 {
		return ""
	}
	alias, name := m.Aliases[0], m.Name
	if offer, known := setupOfferNamed(alias); known {
		name = offer.title
	}
	want := routerProfile(profile)
	if slices.Contains(m.Profiles, want) {
		if !exact {
			return fmt.Sprintf("%s isn't a model name here; ask for %s.", asked, alias)
		}
		return fmt.Sprintf("%s is set up on this computer, and its model server didn't take this call. Try again in a moment.", name)
	}
	for _, other := range []string{"video", "image", "chat"} {
		if slices.Contains(m.Profiles, other) {
			return fmt.Sprintf("%s is a %s model. %s", name, profileNoun(other), callFor(other, alias))
		}
	}
	return fmt.Sprintf("%s serves %s, and this call asks for %s.", name, strings.Join(m.Profiles, ", "), profileKind(profile))
}

// listedModel is the listed model asked names: by family, alias or display
// name ignoring case (exact), else the same letters and digits ("ltx 2.5"
// for ltx-2.5).
func listedModel(models []Model, asked string) (Model, bool, bool) {
	names := func(m Model) []string { return append([]string{m.Family, m.Name}, m.Aliases...) }
	for _, m := range models {
		if slices.ContainsFunc(names(m), func(n string) bool { return strings.EqualFold(n, asked) }) {
			return m, true, true
		}
	}
	key := nameKey(asked)
	for _, m := range models {
		if key != "" && slices.ContainsFunc(names(m), func(n string) bool { return nameKey(n) == key }) {
			return m, false, true
		}
	}
	return Model{}, false, false
}

// nameKey is a model name's letters and digits, lower case.
func nameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
}

// routerProfile is the router profile a gateway profile word asks for.
func routerProfile(profile string) string {
	switch {
	case profile == "video":
		return "video"
	case strings.HasPrefix(profile, "image"):
		return "image"
	}
	return "chat"
}

// profileNoun names a model by what it makes: a video, picture or chat model.
func profileNoun(profile string) string {
	if profile == "image" {
		return "picture"
	}
	return profile
}

// callFor is the call that reaches alias for profile.
func callFor(profile, alias string) string {
	switch profile {
	case "video":
		return fmt.Sprintf("Call oa_video_generate with model %s.", alias)
	case "image":
		return fmt.Sprintf("Call oa_image_generate with model %s.", alias)
	}
	return fmt.Sprintf("Call oa_inference_complete with model %s.", alias)
}
