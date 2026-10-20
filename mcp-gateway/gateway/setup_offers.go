package gateway

import (
	"context"
	"fmt"
	goruntime "runtime"
	"slices"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	model "github.com/openabstractions/abstraction-model/go/client"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

// setupOffer is one reviewed model OpenAbstractions Setup sets up for a use:
// the name a call asks for, its title, the profile it serves, the setup use
// (oa_setup_prepare's word), its download, the memory free for models it
// needs and its measured preset times.
type setupOffer struct {
	name, title, profile, use string
	download, need            int64
	presets                   []PresetTime
}

// PresetTime is one measured run of a model's preset on its reviewed engine:
// the size, the frames for a video, and the seconds it took.
type PresetTime struct {
	Size    string `json:"size"`
	Frames  int    `json:"frames,omitempty"`
	Seconds int64  `json:"seconds"`
}

// setupOffers are the reviewed sets Setup sets up beside the catalogue's
// chat models: LTX-2.5 for video, on this platform's reviewed engine, and
// Z-Image-Turbo for pictures.
func setupOffers() []setupOffer {
	var offers []setupOffer
	if _, engine := modelhost.PinnedVideoEngine(goruntime.GOOS, goruntime.GOARCH); engine {
		ltx := modelhost.LTX25
		title, _, _ := strings.Cut(ltx.DisplayTitle(), " (")
		offer := setupOffer{name: ltx.Name, title: title, profile: "video", use: "video", download: ltx.Size(), need: ltx.PeakBytes}
		for _, run := range ltx.Timings {
			offer.presets = append(offer.presets, PresetTime{Size: fmt.Sprintf("%dx%d", run.Width, run.Height), Frames: run.Frames, Seconds: run.Seconds})
		}
		offers = append(offers, offer)
	}
	z := torch.ZImageTurboTorch
	var size int64
	for _, f := range z.Files {
		size += f.Size
	}
	// 2026-11-10 on the Radeon 8060S: one 1024x1024 picture in 131 s, load
	// included (torch ZImageTurboTorch).
	offers = append(offers, setupOffer{name: z.Name, title: "Z-Image-Turbo", profile: "image", use: "image", download: size, need: z.Lease,
		presets: []PresetTime{{Size: fmt.Sprintf("%dx%d", z.Preset.Width, z.Preset.Height), Seconds: 131}}})
	return offers
}

// setupOfferNamed is the offer a model name asks for, by name or title.
func setupOfferNamed(name string) (setupOffer, bool) {
	for _, offer := range setupOffers() {
		if strings.EqualFold(offer.name, name) || strings.EqualFold(offer.title, name) {
			return offer, true
		}
	}
	return setupOffer{}, false
}

// aboutGB is a download size as a person reads it: about 41 GB.
func aboutGB(bytes int64) string {
	gb := float64(bytes) / 1e9
	if gb < 1 {
		return fmt.Sprintf("about %.1f GB", gb)
	}
	return fmt.Sprintf("about %.0f GB", gb)
}

// setupNeeded lists what Setup can set up on this computer and nothing here
// serves or holds yet: the catalogue's chat models that fit, and the
// reviewed video and picture sets whose need fits the memory free for
// models. A catalogue that cannot be read lists the sets unfiltered.
func (o *OA) setupNeeded(ctx context.Context, listed []Model) []Model {
	known := func(names ...string) bool {
		for _, m := range listed {
			for _, name := range names {
				if strings.EqualFold(m.Family, name) || strings.EqualFold(m.Name, name) || slices.ContainsFunc(m.Aliases, func(a string) bool { return strings.EqualFold(a, name) }) {
					return true
				}
			}
		}
		return false
	}
	pool := int64(-1)
	var out []Model
	if catalog, err := o.machine.ResolveModelCatalog(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err == nil {
		if fit, err := catalog.FitContext(ctx, model.CatalogUseCode); err == nil && fit.Outcome == model.FitOutcomeFitted {
			pool = fit.PoolBytes
			for _, e := range fit.Entries {
				if e.Availability != model.AvailabilityDownload || e.Machine != "" || e.Fit == model.FitNo || known(e.Family, e.Title) {
					continue
				}
				out = append(out, Model{Family: e.Family, Name: e.Title, Aliases: []string{}, Profiles: []string{"chat"},
					SetupNeeded: true, SetupUse: "code", DownloadBytes: e.DownloadBytes})
			}
		}
	}
	for _, offer := range setupOffers() {
		if known(offer.name, offer.title) || pool >= 0 && pool < offer.need {
			continue
		}
		out = append(out, Model{Family: offer.name, Name: offer.title, Aliases: []string{}, Profiles: []string{offer.profile},
			SetupNeeded: true, SetupUse: offer.use, DownloadBytes: offer.download, PresetTimes: offer.presets})
	}
	return out
}
