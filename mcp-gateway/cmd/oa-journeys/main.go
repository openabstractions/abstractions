//go:build windows

// Command oa-journeys drives the Windows person and agent journeys against an
// isolated runtime built from this checkout. release/testing/journeys.py
// builds the programs and runs it; see that script's --help for the journey
// map. It plays the agent app (MCP calls through the real gateway main) and
// the person (answers through the Panel's own routes after a delay).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// check is one assertion of a journey.
type check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
	// Unbuilt names the release a deferred check waits for
	// (backlog/JOURNEY-SCOPE.md); it reports as expected unbuilt, not FAIL.
	Unbuilt string `json:"unbuilt,omitempty"`
	// Pending names the branch whose fix this check waits for; while the
	// checkout under test lacks that branch (-unmerged), a failure reports
	// as expected and does not fail the journey.
	Pending string `json:"pending,omitempty"`
	// Ruling names the architect ruling this check waits for; until it is
	// ruled on, a failure reports as expected and does not fail the journey.
	Ruling string `json:"ruling,omitempty"`
}

// result is one journey's outcome.
type result struct {
	Journey string   `json:"journey"`
	Pass    bool     `json:"pass"`
	Skipped string   `json:"skipped,omitempty"`
	Note    string   `json:"note,omitempty"`
	Seconds float64  `json:"seconds"`
	Checks  []check  `json:"checks"`
	Calls   int      `json:"calls,omitempty"`
	Asked   []string `json:"asked,omitempty"`
}

func (r *result) check(name string, pass bool, detail string, args ...any) bool {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	r.Checks = append(r.Checks, check{Name: name, Pass: pass, Detail: detail})
	return pass
}

// unmerged is the set of fix branches the checkout under test lacks
// (journeys.py -unmerged).
var unmerged = map[string]bool{}

// awaiting is check for a behaviour the fix on branch brings: a failure
// while the checkout lacks branch is an expected failure.
func (r *result) awaiting(branch, name string, pass bool, detail string, args ...any) bool {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	r.Checks = append(r.Checks, check{Name: name, Pass: pass, Detail: detail, Pending: branch})
	return pass
}

// awaitingRuling is check for a behaviour an architect ruling decides: a
// failure reports as expected, with the ruling as its reason.
func (r *result) awaitingRuling(ruling, name string, pass bool, detail string, args ...any) bool {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	r.Checks = append(r.Checks, check{Name: name, Pass: pass, Detail: detail, Ruling: ruling})
	return pass
}

// expectedFailure is whether c fails only because its fix is not merged or
// its ruling is not made.
func (c check) expectedFailure() bool {
	return !c.Pass && (c.Ruling != "" || c.Pending != "" && unmerged[c.Pending])
}

// waitsFor is what an expected failure waits for.
func (c check) waitsFor() string {
	if c.Ruling != "" {
		return c.Ruling
	}
	return c.Pending + " merges"
}

// deferred records a check the architect deferred to a later release
// (backlog/JOURNEY-SCOPE.md). It stays in the report as "expected unbuilt
// (<release>)" and never fails the journey; its detail says whether OA built
// it, refused it with a sentence, or refused it bare. A bare refusal is the
// RIGHTS-FLOW bug that the journey's own refusal checks report.
func (r *result) deferred(name, release string, built bool, refusal string, detail string, args ...any) {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	state := "built"
	if !built {
		state = "refused with a sentence: " + clip(refusal, 200)
		if refusal == "" {
			state = "refused without a sentence (RIGHTS-FLOW: one outcome word and one sentence)"
		}
	}
	unbuilt := release
	if built {
		unbuilt = ""
	}
	r.Checks = append(r.Checks, check{Name: name, Pass: true, Unbuilt: unbuilt, Detail: state + "; " + detail})
}

func (r *result) finish() {
	r.Pass = r.Skipped == "" && len(r.Checks) > 0
	for _, c := range r.Checks {
		if !c.Pass && !c.expectedFailure() {
			r.Pass = false
		}
	}
}

// expected lists the branches this result's expected failures wait for.
func (r *result) expected() []string {
	var branches []string
	for _, c := range r.Checks {
		if c.expectedFailure() && !slices.Contains(branches, c.waitsFor()) {
			branches = append(branches, c.waitsFor())
		}
	}
	return branches
}

type journey struct {
	name  string
	run   func(context.Context, *env, *options, *result)
	order int
	// budget is the journey's timeout, derived from its work; nil is the
	// suite's four minutes.
	budget func(*options) time.Duration
}

type options struct {
	bin, work   string
	tools       string
	answerDelay time.Duration
	pytorch     string
	stall       time.Duration
	// chatModel is the GGUF the cold chat journeys host on the real model
	// host; engineBundle the llama.cpp bundle ZIP its engine is staged from.
	chatModel, engineBundle string
	// comfyManifest is the ComfyUI connector's application manifest.
	comfyManifest string
}

// journeys run in the order they are registered by order; each journey file
// registers itself in init.
var journeys []journey

func register(order int, name string, run func(context.Context, *env, *options, *result)) {
	registerTimed(order, name, nil, run)
}

// registerTimed registers a journey whose timeout budget derives from its
// work.
func registerTimed(order int, name string, budget func(*options) time.Duration, run func(context.Context, *env, *options, *result)) {
	journeys = append(journeys, journey{name: name, run: run, order: order, budget: budget})
	sort.SliceStable(journeys, func(i, j int) bool { return journeys[i].order < journeys[j].order })
}

func main() {
	var o options
	only := flag.String("only", "", "comma-separated journeys to run (default: all of "+names()+")")
	out := flag.String("out", "", "write the JSON results to this file")
	flag.StringVar(&o.bin, "bin", "", "folder holding gateway.test.exe and the journey helpers (journey-image.exe, oa-journey-chat.exe, oa-journey-app.exe, oa-journey-picture.exe)")
	flag.StringVar(&o.tools, "tools", "", "folder holding the product programs under test: openabstractions.exe, Abstraction Panel.exe, modelhostd.exe, inventoryd.exe (default: -bin); an installed build's tools folder runs the journeys against that install")
	flag.StringVar(&o.chatModel, "chat-model", "", "J-COLD-CHAT, J-PIC-COLD: a chat GGUF on this computer the real model host loads cold (hard-linked, never copied or changed)")
	flag.StringVar(&o.engineBundle, "engine-bundle", "", "J-COLD-CHAT, J-PIC-COLD: the llama.cpp bundle ZIP (default: the one in -tools\\engines)")
	flag.StringVar(&o.comfyManifest, "comfy-manifest", "", "J-CONNECT: the ComfyUI connector's application-manifest.json")
	pending := flag.String("unmerged", "", "comma-separated fix branches the checkout under test lacks; checks awaiting them fail as expected")
	flag.StringVar(&o.work, "work", "", "empty folder for the isolated runtime's state and logs")
	flag.DurationVar(&o.answerDelay, "answer-delay", 3*time.Second, "how long the person takes to answer a question after it appears")
	flag.StringVar(&o.pytorch, "pytorch", "", "J-PIC: use this pytorch picture model declaration instead of the test image provider (empty: test provider)")
	flag.DurationVar(&o.stall, "stall", 14*time.Second, "J-MSG: how long the runtime stops answering while a Send is in flight")
	connectorImage := flag.String("connector-image", "", "write J-CONNECT's connector program (-bin's stand-in with -comfy-manifest) to this file and exit")
	list := flag.Bool("list", false, "print the registered journey names, one per line, and exit")
	flag.Parse()
	if *list {
		for _, j := range journeys {
			fmt.Println(j.name)
		}
		return
	}
	if *connectorImage != "" {
		manifest, err := os.ReadFile(o.comfyManifest)
		if err == nil {
			var image []byte
			if image, err = connectorStandIn(o.bin, manifest); err == nil {
				err = os.WriteFile(*connectorImage, image, 0o600)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "oa-journeys:", err)
			os.Exit(1)
		}
		return
	}
	if o.bin == "" || o.work == "" {
		fmt.Fprintln(os.Stderr, "oa-journeys: -bin and -work are required; run release/testing/journeys.py instead")
		os.Exit(2)
	}
	o.bin, _ = filepath.Abs(o.bin)
	o.work, _ = filepath.Abs(o.work)
	if o.tools == "" {
		o.tools = o.bin
	}
	o.tools, _ = filepath.Abs(o.tools)
	for _, b := range strings.Split(*pending, ",") {
		if b = strings.TrimSpace(b); b != "" {
			unmerged[b] = true
		}
	}
	if err := os.MkdirAll(o.work, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The fixture home lies in a short temporary folder: the model host's
	// engine socket under its LOCALAPPDATA must fit an AF_UNIX address (107
	// bytes), which a home under a deep work folder outgrows.
	short, err := os.MkdirTemp("", "oaj-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "oa-journeys: fixture home:", err)
		os.Exit(1)
	}
	defer removeFixtureHome(short)
	if _, err := enterTestHome(short); err != nil {
		fmt.Fprintln(os.Stderr, "oa-journeys: fixture home:", err)
		removeFixtureHome(short)
		os.Exit(1)
	}
	logFile := mustLog(o.work, "journeys.log")
	logf := func(format string, args ...any) {
		line := time.Now().Format("15:04:05.000") + " " + fmt.Sprintf(format, args...)
		if logFile != nil {
			fmt.Fprintln(logFile, line)
		}
	}
	release := holdAwake(func(format string, args ...any) {
		fmt.Println(fmt.Sprintf(format, args...))
		logf(format, args...)
	})
	defer release()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	go func() {
		<-interrupt
		release()
		removeFixtureHome(short)
		os.Exit(130)
	}()
	selected := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			selected[strings.ToUpper(n)] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var results []result
	for _, j := range journeys {
		if len(selected) > 0 && !selected[j.name] {
			continue
		}
		r := result{Journey: j.name, Checks: []check{}}
		start := time.Now()
		logf("=== %s", j.name)
		// Each journey meets a fresh isolated runtime, as a person meets a
		// fresh install: no rule, registration or answer carries over.
		jo := o
		jo.work = filepath.Join(o.work, strings.ToLower(j.name))
		budget := 4 * time.Minute
		if j.budget != nil {
			budget = j.budget(&jo)
		}
		logf("budget %s", budget)
		e, err := startJourneyEnv(ctx, j.name, &jo, &r, logf)
		if err != nil {
			logf("%s did not start: %v", j.name, err)
		} else {
			e.startAnswerer(o.answerDelay)
			func() {
				jctx, jcancel := context.WithTimeout(ctx, budget)
				defer jcancel()
				defer func() {
					if p := recover(); p != nil {
						r.check("journey ran", false, "panic: %v", p)
					}
				}()
				j.run(jctx, e, &jo, &r)
			}()
			for _, q := range e.answers.since(0) {
				r.Asked = append(r.Asked, fmt.Sprintf("%s %s: %s -> %s", q.Source, q.Key, clip(q.Text, 160), q.Answer))
			}
			e.close()
		}
		r.Seconds = time.Since(start).Seconds()
		r.finish()
		results = append(results, r)
		fmt.Println(summaryLine(r))
		for _, c := range r.Checks {
			mark := "ok  "
			if c.Unbuilt != "" {
				mark = "expected unbuilt (" + c.Unbuilt + "):"
			} else if c.expectedFailure() {
				mark = "XFAIL (until " + c.waitsFor() + ")"
			} else if !c.Pass {
				mark = "FAIL"
			}
			fmt.Printf("    %s %s %s\n", mark, c.Name, clip(c.Detail, 400))
		}
	}
	removeLocalCache(o.work, logf)
	raw, _ := json.MarshalIndent(results, "", "  ")
	if *out != "" {
		_ = os.WriteFile(*out, raw, 0o600)
	}
	if foreign := listForeignIsolatedCredentials(); len(foreign) > 0 {
		fmt.Printf("%d stored oa-isolated-* credentials from other runs remain; the journeys leave them alone\n", len(foreign))
	}
	for _, r := range results {
		if !r.Pass {
			release()
			removeFixtureHome(short)
			os.Exit(1)
		}
	}
}

func startJourneyEnv(ctx context.Context, name string, o *options, r *result, logf func(string, ...any)) (*env, error) {
	if err := os.MkdirAll(o.work, 0o700); err != nil {
		r.check("journey work folder", false, "%v", err)
		return nil, err
	}
	if name == "J-NAS" {
		if err := nasPreflight(o.work); err != nil {
			r.check("archive fixture capacity", false, "%v", err)
			return nil, err
		}
	}
	e, err := startEnv(ctx, o.bin, o.tools, o.work, logf)
	if err != nil {
		r.check("isolated runtime started", false, "%v", err)
	}
	return e, err
}

// removeFixtureHome deletes the temporary fixture home. It holds no link or
// junction; a file a stopped child still holds stays for the temporary
// folder's own cleanup.
func removeFixtureHome(dir string) {
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if err := os.RemoveAll(dir); err == nil || time.Now().After(deadline) {
			return
		}
	}
}

func summaryLine(r result) string {
	state := "PASS"
	if r.Skipped != "" {
		state = "SKIP " + r.Skipped
	} else if !r.Pass {
		state = "FAIL"
	} else if pending := r.expected(); len(pending) > 0 {
		state = "XFAIL (until " + strings.Join(pending, ", ") + ")"
	}
	line := fmt.Sprintf("%-9s %s (%.0fs", r.Journey, state, r.Seconds)
	if r.Calls > 0 {
		line += fmt.Sprintf(", %d tool calls", r.Calls)
	}
	line += fmt.Sprintf(", %d questions)", len(r.Asked))
	if r.Note != "" {
		line += " — " + r.Note
	}
	return line
}

func names() string {
	var n []string
	for _, j := range journeys {
		n = append(n, j.name)
	}
	return strings.Join(n, ", ")
}
