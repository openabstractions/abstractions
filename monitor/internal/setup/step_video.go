package setup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

// UseVideo is an app's request for video with sound: the reviewed video
// model set the model host serves (architect ruling 2026-11-11, Setup use
// video).
const UseVideo = "video"

// stageVideo is the stage a stopped video setup stopped at.
const stageVideo = "video"

// errStepVideo marks a failure of the video part, so its notice names video.
var errStepVideo = errors.New("the video part did not finish")

// StepVideo is the video part an app's request for use video sets up.
type StepVideo struct {
	// Set is the reviewed video model set and Engine its pinned engine;
	// the question's facts come from them.
	Set    modelhost.VideoModelSet
	Engine modelhost.EnginePin
	// Served reports whether the router serves the set's video on this
	// computer now. Nil reads as not served.
	Served func(ctx context.Context) (bool, error)
	// Get makes the set ready and declares the video provider: the pinned
	// engine and every set file through the runtime's download jobs
	// (openabstractions models host --video-model SET --get).
	Get func(ctx context.Context) error
}

// videoTitle is a set's title as the question names it: its display title
// without the parenthesis, "LTX-2.5" for "LTX-2.5 (video with sound)".
func videoTitle(set modelhost.VideoModelSet) string {
	title, _, _ := strings.Cut(set.DisplayTitle(), " (")
	return title
}

// VideoSetupFact is what the question states about set on engine: the
// download (the engine archive and every set file), the free space it
// needs (the download, the engine's expanded files and the headroom the
// picture plans keep), the memory the set needs, its use policy, its Hugging
// Face sources and its measured preset times.
func VideoSetupFact(set modelhost.VideoModelSet, engine modelhost.EnginePin) asks.SetupVideoFact {
	f := asks.SetupVideoFact{Model: set.Name, Title: videoTitle(set), License: set.UsePolicy.Name, LicenseURL: set.UsePolicy.URL, MemoryBytes: set.PeakBytes,
		Sources: []asks.SetupVideoSource{}, Presets: []asks.SetupVideoPreset{}}
	if f.License == "" && len(set.Files) > 0 {
		f.License = set.Files[0].License
	}
	f.DownloadBytes = engine.ArchiveSize + set.Size()
	var expanded int64
	for _, file := range engine.Files {
		expanded += file.Size
	}
	f.PeakBytes = f.DownloadBytes + expanded + bf16DiskHeadroom
	for _, file := range set.Files {
		repository := huggingFaceRepository(file.Source)
		if repository == "" || file.Revision == "" {
			continue
		}
		source := asks.SetupVideoSource{Repository: repository, Revision: file.Revision}
		seen := false
		for _, s := range f.Sources {
			seen = seen || s == source
		}
		if !seen {
			f.Sources = append(f.Sources, source)
		}
	}
	timing := func(width, height int) (modelhost.VideoTiming, bool) {
		for _, t := range set.Timings {
			if t.Width == width && t.Height == height {
				return t, true
			}
		}
		return modelhost.VideoTiming{}, false
	}
	if t, ok := timing(set.Preset.Width, set.Preset.Height); ok {
		f.Presets = append(f.Presets, asks.SetupVideoPreset{Name: "quick", Width: t.Width, Height: t.Height, Stages: 1, Seconds: t.Seconds})
	}
	for _, r := range set.Preset.Refine {
		if t, ok := timing(r.Width, r.Height); ok {
			f.Presets = append(f.Presets, asks.SetupVideoPreset{Name: "quality", Width: t.Width, Height: t.Height, Stages: 2, Seconds: t.Seconds})
			break
		}
	}
	return f
}

// huggingFaceRepository is the owner/name of a huggingface.co file URL.
func huggingFaceRepository(source string) string {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host != "huggingface.co" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// videoQuestion is the question for app's request for video.
func videoQuestion(app string, v StepVideo) (StepQuestion, error) {
	fact := VideoSetupFact(v.Set, v.Engine)
	return stepQuestionFromFacts(asks.SetupQuestionFacts{Requester: app, RequestUse: UseVideo, Video: &fact})
}

// runVideo answers an app's request for video. It asks nothing when the
// router already serves the set here (already_set_up) or the memory free
// for models is below the set's need (cannot_run, one sentence). Otherwise
// it asks the one question and, on Set up, gets the set. The coding-model
// journal stays as it is.
func (s SetupStep) runVideo(ctx context.Context) (stepOutcome, error) {
	r := s.Requester
	v := s.Owners.Video
	if v == nil || v.Get == nil {
		return stepCannotRun, s.notify(ctx, StepNotice{Title: r.App + " cannot get video here yet", Body: r.App + " asked for video with sound, which this computer cannot set up yet.", Tag: "setup"})
	}
	title := videoTitle(v.Set)
	if v.Served != nil {
		// Display data: a router that cannot be read leaves the question.
		if served, err := v.Served(ctx); err != nil {
			log.Printf("setup video: the router's video read: %v", err)
		} else if served {
			return stepAlreadySetUp, s.notify(ctx, StepNotice{Title: "Already set up", Body: title + " makes video with sound on this computer.", Tag: "setup"})
		}
	}
	if s.Owners.Fit != nil {
		if fit, err := s.Owners.Fit(ctx, UseCode); err != nil {
			log.Printf("setup video: the memory pool read: %v", err)
		} else if fit.PoolBytes < v.Set.PeakBytes {
			return stepCannotRun, s.notify(ctx, StepNotice{Title: r.App + " cannot make video here", Body: asks.SetupCannotRunSentence(fit.PoolBytes, title, v.Set.PeakBytes), Tag: "setup"})
		}
	}
	q, err := videoQuestion(r.App, *v)
	if err != nil {
		return "", atStage(stageQuestion, err)
	}
	yes, err := s.Owners.Ask(ctx, q)
	if err != nil {
		return "", atStage(stageQuestion, err)
	}
	if !yes {
		return stepAnsweredNotNow, nil
	}
	if err := s.notify(ctx, StepNotice{Title: "Setting up video with sound", Body: title + " is downloading for " + r.App + ". OpenAbstractions tells you when it is ready.", Tag: "setup"}); err != nil {
		log.Printf("setup video: the progress notice: %v", err)
	}
	if err := v.Get(ctx); err != nil {
		return "", atStage(stageVideo, fmt.Errorf("%w: %w", errStepVideo, err))
	}
	return stepSetUp, s.notify(ctx, StepNotice{Title: "Video with sound is ready", Body: title + " makes video with sound for " + r.App + " on this computer.", Tag: "setup"})
}
