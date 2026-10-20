// Command oa-journey-picture is the journey suite's picture engine for
// J-PIC-COLD (release/testing/journeys.py). It serves image@1 like any
// local picture provider and behaves like a cold Diffusers load: the first
// picture asks the runtime's resource service for a lease of card:0 of its
// free memory and 4 GiB more, so a model holding the card has to give memory
// back, then loads for -cold before it answers. Each picture is a small PNG; no GPU work runs
// and the lease is admission accounting only.
//
// It writes what the lease service answered to picture.log in -store.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-provider-kit/go/providerhost"
	resclient "github.com/openabstractions/abstraction-resource/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// card is the resource the picture engine loads onto.
const card = "card:0"

// needFromHolders is how much more than the card's free memory the first
// picture's lease asks for.
const needFromHolders = 4 << 30

type coldPicture struct {
	cold  time.Duration
	store string
	mu    sync.Mutex
	warm  bool
	lease string
}

func (p *coldPicture) logf(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(p.store, "picture.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

func (*coldPicture) Unsupported(req wire.ImageRequest) string {
	if req.Mode != wire.ImageModeGenerate {
		return "mode:edit"
	}
	if req.Count != 1 {
		return "image:request"
	}
	return ""
}

// load takes the card as a Diffusers load does: a lease that needs part of
// what the card's current holders hold.
func (p *coldPicture) load(ctx context.Context, progress func(int64, string)) error {
	leases := resclient.Discover()
	state, err := leases.HoldersContext(ctx, card, true)
	if err != nil {
		p.logf("holders: %v", err)
		return fmt.Errorf("holders: %v", err)
	}
	capacity, held := state.Capacity, state.Held
	for _, h := range state.Holders {
		p.logf("holder %s %d bytes", h.Program, h.Amount)
	}
	// What is free and needFromHolders more: the holders on the card have
	// to give some back.
	amount := capacity - held + needFromHolders
	if capacity <= 0 || amount > capacity {
		amount = capacity / 4
	}
	p.logf("card capacity %d held %d; asking %d", capacity, held, amount)
	result, err := leases.AcquireContext(ctx, card, amount, time.Minute+resclient.AcquireBudget)
	asked := 0
	for _, a := range result.Asked {
		p.logf("asked %+v", a)
		asked++
	}
	if err != nil || result.Lease == nil {
		p.logf("acquire: %v %s", err, result.Outcome)
		return fmt.Errorf("acquire %s: %v %s", card, err, result.Outcome)
	}
	p.lease = result.Lease.ID
	p.logf("acquired %d bytes after asking %d holders", result.Lease.Amount, asked)
	go p.renew(result.Lease.ID)
	start := time.Now()
	for time.Since(start) < p.cold {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		progress(int64(100*time.Since(start)/p.cold), "loading")
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func (p *coldPicture) renew(lease string) {
	for {
		time.Sleep(5 * time.Second)
		change, err := resclient.Discover().Renew(lease)
		if err != nil {
			p.logf("renew: %v", err)
			continue
		}
		if !change.Applied {
			p.logf("renew: not applied")
			return
		}
	}
}

func (p *coldPicture) Run(ctx context.Context, _ string, _ wire.ImageRequest, _ map[string]inference.ImageInput, progress func(int64, string)) inference.ImageEngineReply {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.warm {
		if err := p.load(ctx, progress); err != nil {
			if ctx.Err() != nil {
				return inference.ImageEngineReply{Outcome: wire.ReplyOutcomeCancelled, Reason: "caller"}
			}
			return inference.ImageEngineReply{Outcome: wire.ReplyOutcomeUnavailable, Reason: "engine:load"}
		}
		p.warm = true
	}
	pic := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			pic.Set(x, y, color.RGBA{R: uint8(4 * x), G: 90, B: uint8(4 * y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pic); err != nil {
		return inference.ImageEngineReply{Outcome: wire.ReplyOutcomeUnavailable, Reason: "engine:encode"}
	}
	p.logf("picture made")
	return inference.ImageEngineReply{Outcome: wire.ReplyOutcomeCompleted, Images: []inference.ImageOutput{{Data: buf.Bytes(), MediaType: "image/png"}}}
}

func main() {
	endpoint := flag.String("endpoint", "", "native provider endpoint")
	runtimeProgram := flag.String("runtime", "", "absolute path of the OA runtime executable")
	store := flag.String("store", "", "absolute private provider store directory")
	model := flag.String("model", "journey/cold-picture", "the picture model this engine serves")
	cold := flag.Duration("cold", 40*time.Second, "how long the first picture's load takes")
	flag.Parse()
	if *endpoint == "" || *runtimeProgram == "" || *store == "" {
		fmt.Fprintln(os.Stderr, "endpoint, runtime and store are required")
		os.Exit(2)
	}
	host, err := router.NewImageEngine("journey-picture", []string{*model})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	engine := &coldPicture{cold: *cold, store: *store}
	provider, err := providerhost.NewImage(providerhost.ImageConfig{Endpoint: *endpoint, RuntimePrograms: []string{*runtimeProgram}, Host: host, Engine: engine, StoreRoot: *store, RecordPath: filepath.Join(*store, "uploads.json")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = provider.Serve(ctx)
	if engine.lease != "" {
		_, _ = resclient.Discover().Release(engine.lease)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
