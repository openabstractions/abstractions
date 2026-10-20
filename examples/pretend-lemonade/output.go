package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	asksbook "github.com/openabstractions/abstraction-asks/go"
	asks "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type outputChoiceInput func(context.Context, time.Duration) (string, bool, error)

// This app reads only a bounded local selection. It owns no input goroutine,
// leaves terminal modes unchanged and never interprets EOF as an offered option.
func terminalOutputInput(file *os.File, out io.Writer) outputChoiceInput {
	var line []byte
	echo := outputInputEcho(file)
	return func(ctx context.Context, maximum time.Duration) (string, bool, error) {
		until := time.Now().Add(min(maximum, 100*time.Millisecond))
		for {
			char, ready, err := outputInputByte(ctx, file, max(time.Until(until), 0))
			if err != nil || !ready {
				return "", false, err
			}
			switch char {
			case '\r', '\n':
				if echo {
					fmt.Fprintln(out)
				}
				word := strings.ToLower(strings.TrimSpace(string(line)))
				line = line[:0]
				switch word {
				case "replace", "r":
					return "replace", true, nil
				case "keep", "k":
					return "keep", true, nil
				case "cancel", "c":
					return "cancel", true, nil
				default:
					fmt.Fprintln(out, "Type Replace, Keep or Cancel, then press Enter.")
				}
			case '\b', 127:
				if len(line) > 0 {
					line = line[:len(line)-1]
					if echo {
						fmt.Fprint(out, "\b \b")
					}
				}
			default:
				if char < 32 || char > 126 || len(line) >= 32 {
					return "", false, errors.New("invalid output choice input; existing file retained")
				}
				line = append(line, char)
				if echo {
					fmt.Fprintf(out, "%c", char)
				}
			}
			if !time.Now().Before(until) {
				return "", false, nil
			}
		}
	}
}

func outputInputPause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// This CLI retains one immutable intent in process. A separate invocation makes
// a fresh output choice while the download's request identity stays unchanged.
func outputChoice(ctx context.Context, where string) (asks.ChoiceRequest, error) {
	title, err := outputChoiceTitle(where)
	if err != nil {
		return asks.ChoiceRequest{}, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return asks.ChoiceRequest{}, err
	}
	now := time.Now()
	pending := now.Add(90 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(pending) {
		pending = deadline
	}
	q := asks.ChoiceRequest{
		RequestRef:     asks.RequestRef{Key: fmt.Sprintf("pretend-lemonade-copy-%x", nonce), ReplayUntilMs: now.Add(5 * time.Minute).UnixMilli()},
		Title:          title,
		Options:        []asks.ChoiceOption{{ID: "replace", Label: "Replace"}, {ID: "keep", Label: "Keep"}, {ID: "cancel", Label: "Cancel"}},
		PendingUntilMs: pending.UnixMilli(),
	}
	if !asksbook.ValidChoiceRequest(q) {
		return asks.ChoiceRequest{}, errors.New("output choice does not meet the ordinary presentation bounds; existing file retained")
	}
	return q, nil
}

// Display text supplies context only. The effect retains the separate full
// destination and never derives a path from this quoted presentation.
func outputChoiceTitle(where string) (string, error) {
	title := "Existing output: " + strconv.QuoteToGraphic(filepath.Base(where))
	if utf8.RuneCountInString(title) > 80 {
		return "", errors.New("cannot present this full output filename within 80 Unicode characters; existing file retained")
	}
	parent := filepath.Dir(where)
	for _, context := range []string{parent, filepath.Base(parent)} {
		candidate := title + " in " + strconv.QuoteToGraphic(context)
		if utf8.RuneCountInString(candidate) <= 80 {
			return candidate, nil
		}
	}
	return title, nil
}

type choiceService interface {
	AskContext(context.Context, asks.ChoiceRequest) (asks.ChoiceObservation, error)
	ObserveContext(context.Context, asks.RequestRef, int64) (asks.ChoiceObservation, error)
	AcknowledgeContext(context.Context, asks.RequestRef, asks.ChoiceAnswer) (asks.ChoiceAcknowledgement, error)
}

// outputAssistantOptions are the options this app lets an assistant the
// person enrolled choose (asks ASK-A12). Replace overwrites the person's
// file, so it stays with the person.
var outputAssistantOptions = []string{"keep", "cancel"}

type choiceShareService interface {
	ShareContext(context.Context, asks.ChoiceRequest, []string) (asks.ChoiceObservation, error)
}

// admitOutputChoice shares the choice with an enrolled assistant when the
// runtime offers ShareChoice. A runtime too old for it answers unknown_method;
// this app then asks unshared, says so once, and keeps asking unshared, so a
// retry replays the same immutable request.
func admitOutputChoice(ctx context.Context, c choiceService, q asks.ChoiceRequest, unshared *bool, out io.Writer) (asks.ChoiceObservation, error) {
	sharer, ok := c.(choiceShareService)
	if !ok || *unshared {
		return c.AskContext(ctx, q)
	}
	observation, err := sharer.ShareContext(ctx, q, outputAssistantOptions)
	var service *asks.ServiceError
	if errors.As(err, &service) && service.Code == asks.ServiceErrorCodeUnknownMethod {
		*unshared = true
		fmt.Fprintln(out, "This OpenAbstractions runtime cannot share the choice with an assistant; it waits for you.")
		return c.AskContext(ctx, q)
	}
	return observation, err
}

type choiceAnswerService interface {
	AnswerContext(context.Context, asks.RequestRef, string) (asks.ChoiceDecision, error)
	ReconcileAnswerContext(context.Context, asks.RequestRef, string) (asks.ChoiceDecision, error)
}

func waitOutputChoice(ctx context.Context, c choiceService, q asks.ChoiceRequest, where string, out io.Writer, input outputChoiceInput) (asks.ChoiceAnswer, error) {
	if input != nil {
		// Finish a partially typed console line before reporting the winner or error.
		defer fmt.Fprintln(out)
	}
	waitCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(q.RequestRef.ReplayUntilMs))
	defer cancel()
	inputCtx, cancelInput := context.WithDeadline(waitCtx, time.UnixMilli(q.PendingUntilMs))
	defer cancelInput()
	var answerer choiceAnswerService
	if input != nil {
		var ok bool
		answerer, ok = c.(choiceAnswerService)
		if !ok {
			return asks.ChoiceAnswer{}, errors.New("output choice service cannot accept this app's own answer; existing file retained")
		}
	}
	var knownID string
	var selected string
	var attempted bool
	var uncertain bool
	var unshared bool
	for {
		var observation asks.ChoiceObservation
		var err error
		if knownID == "" {
			// An uncertain admission replays the same complete request. After a
			// known admission, only observation is retried.
			observation, err = admitOutputChoice(waitCtx, c, q, &unshared, out)
		} else {
			waitMS := int64(1000)
			if input != nil {
				waitMS = 100 // Observe concurrent answers between bounded input reads.
			}
			if selected != "" {
				waitMS = 0 // Reconcile the retained winner after our submission.
			}
			if time.Now().UnixMilli() >= q.PendingUntilMs {
				waitMS = 0 // Reconcile a recorded answer after the pending window.
			}
			observation, err = c.ObserveContext(waitCtx, q.RequestRef, waitMS)
		}
		if err != nil {
			if time.Now().UnixMilli() >= q.PendingUntilMs {
				return asks.ChoiceAnswer{}, fmt.Errorf("output choice unresolved after pending deadline; existing file retained: %w", err)
			}
			if err := retryChoice(waitCtx); err != nil {
				return asks.ChoiceAnswer{}, fmt.Errorf("output choice unresolved; existing file retained: %w", err)
			}
			continue
		}
		if knownID != "" && observation.ID != "" && observation.ID != knownID {
			return asks.ChoiceAnswer{}, errors.New("output choice changed identity; existing file retained")
		}
		switch observation.Outcome {
		case asks.ChoiceOutcomePending:
			if observation.ID == "" || observation.Answer != nil {
				return asks.ChoiceAnswer{}, errors.New("invalid pending output choice")
			}
			if knownID == "" {
				knownID = observation.ID
				fmt.Fprintf(out, "output choice %s for %s: Replace, Keep or Cancel\n", knownID, strconv.QuoteToGraphic(where))
				if input != nil {
					fmt.Fprintln(out, "Type Replace (r), Keep (k) or Cancel (c), then press Enter. Closing input retains the existing file.")
				} else {
					fmt.Fprintln(out, "Waiting for an external answer; this app does not open an ordinary-choice popup.")
				}
				if _, shares := c.(choiceShareService); shares && !unshared {
					fmt.Fprintln(out, "An assistant you let answer for you may choose Keep or Cancel; Replace stays with you.")
				}
			}
			if time.Now().UnixMilli() >= q.PendingUntilMs {
				return asks.ChoiceAnswer{}, errors.New("output choice pending deadline expired; existing file retained")
			}
			if input != nil && selected == "" {
				option, ready, err := input(inputCtx, 100*time.Millisecond)
				if errors.Is(err, context.DeadlineExceeded) && time.Now().UnixMilli() >= q.PendingUntilMs {
					continue // Observe an answer recorded before the input window closed.
				}
				if err != nil {
					return asks.ChoiceAnswer{}, fmt.Errorf("output choice input closed; existing file retained: %w", err)
				}
				if ready {
					offered := false
					for _, offeredOption := range q.Options {
						offered = offered || offeredOption.ID == option
					}
					if !offered {
						return asks.ChoiceAnswer{}, errors.New("output choice input selected an unoffered option; existing file retained")
					}
					selected = option
				}
			}
			if selected != "" {
				if attempted && !uncertain {
					return asks.ChoiceAnswer{}, errors.New("output choice remained pending after a completed decision; existing file retained")
				}
				var decision asks.ChoiceDecision
				if attempted {
					decision, err = answerer.ReconcileAnswerContext(waitCtx, q.RequestRef, selected)
				} else {
					decision, err = answerer.AnswerContext(waitCtx, q.RequestRef, selected)
				}
				attempted = true
				uncertain = err != nil || decision.Outcome == asks.ChoiceDecisionOutcomeUnavailable
				if err == nil {
					switch decision.Outcome {
					case asks.ChoiceDecisionOutcomeAnswered, asks.ChoiceDecisionOutcomeConflict, asks.ChoiceDecisionOutcomeUnavailable:
						// The subsequent observation supplies the retained winner.
					default:
						return asks.ChoiceAnswer{}, fmt.Errorf("output choice answer %s; existing file retained", decision.Outcome)
					}
				}
				if uncertain {
					if err := retryChoice(waitCtx); err != nil {
						return asks.ChoiceAnswer{}, fmt.Errorf("output choice answer unresolved; existing file retained: %w", err)
					}
				}
			}
		case asks.ChoiceOutcomeAnswered, asks.ChoiceOutcomeDismissed:
			answer := observation.Answer
			if answer == nil || answer.ID == "" || answer.ID != observation.ID || answer.AnsweredMs <= 0 || answer.AnsweredBy == "" {
				return asks.ChoiceAnswer{}, errors.New("invalid output choice answer")
			}
			if observation.Outcome == asks.ChoiceOutcomeDismissed {
				if answer.Option != "" {
					return asks.ChoiceAnswer{}, errors.New("invalid dismissed output choice")
				}
			} else {
				offered := false
				for _, option := range q.Options {
					offered = offered || option.ID == answer.Option
				}
				if !offered {
					return asks.ChoiceAnswer{}, errors.New("output choice returned an unoffered option")
				}
			}
			return *answer, nil
		default:
			return asks.ChoiceAnswer{}, fmt.Errorf("output choice %s; existing file retained", observation.Outcome)
		}
	}
}

// Ack surrenders service replay after the completed effect/no-op. This invocation
// retains the exact answer for uncertain Ack retries and never repeats the copy.
func acknowledgeOutput(ctx context.Context, c choiceService, ref asks.RequestRef, answer asks.ChoiceAnswer) error {
	for {
		ack, err := c.AcknowledgeContext(ctx, ref, answer)
		if err != nil {
			if err := retryChoice(ctx); err != nil {
				return fmt.Errorf("local copy decision completed; acknowledgement unresolved: %w", err)
			}
			continue
		}
		if ack.Outcome != asks.ChoiceAckOutcomeAcknowledged {
			return fmt.Errorf("local copy decision completed; acknowledgement %s", ack.Outcome)
		}
		return nil
	}
}

func retryChoice(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func deliver(ctx context.Context, selected *facade.Machine, jobs *facade.JobsClient, identity api.RequestIdentity, source, destination string, size int64, digest string, ask bool, out io.Writer, input outputChoiceInput) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil {
		return "", err
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" {
		name = "download"
	}
	where, err := filepath.Abs(filepath.Join(destination, name))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(where), 0o755); err != nil {
		return "", err
	}
	var f *os.File
	if ask {
		// Exclusive creation also catches a file appearing during this call.
		// A failed create leaves the existing target untouched before asking.
		f, err = os.OpenFile(where, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, os.ErrExist) {
			choices, err := selected.ResolveAsksChoices(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			if err != nil {
				return "", fmt.Errorf("output choice service: %w", err)
			}
			q, err := outputChoice(ctx, where)
			if err != nil {
				return "", err
			}
			return applyOutputChoice(ctx, choices, q, where, size, digest, func(ctx context.Context, w io.Writer) (int64, error) {
				return jobs.CopyResult(ctx, identity, w)
			}, out, input)
		}
	} else {
		f, err = os.Create(where)
	}
	if err != nil {
		return "", err
	}
	if _, err := jobs.CopyResult(ctx, identity, f); err != nil {
		f.Close() //unchecked: the copy error is the one to return
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "delivered to %s\n", where)
	return "download delivered", nil
}

func applyOutputChoice(ctx context.Context, c choiceService, q asks.ChoiceRequest, where string, size int64, digest string, copyResult func(context.Context, io.Writer) (int64, error), out io.Writer, input outputChoiceInput) (string, error) {
	answer, err := waitOutputChoice(ctx, c, q, where, out, input)
	if err != nil {
		return "", err
	}
	effectCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(q.RequestRef.ReplayUntilMs))
	defer cancel()
	if err := effectCtx.Err(); err != nil {
		return "", fmt.Errorf("output choice replay expired; existing file retained: %w", err)
	}
	message := "download copy cancelled"
	switch answer.Option {
	case "replace":
		if err := replaceOutput(effectCtx, where, size, digest, copyResult, replaceSibling); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "delivered to %s\n", where)
		message = "download delivered"
	case "keep":
		fmt.Fprintf(out, "kept existing output %s; download remains completed\n", where)
		message = "download kept existing output"
	case "cancel", "":
		fmt.Fprintf(out, "cancelled local copy; existing output %s and completed download retained\n", where)
	default:
		return "", errors.New("unknown output choice; existing file retained")
	}
	return message, acknowledgeOutput(effectCtx, c, q.RequestRef, answer)
}

func replaceOutput(ctx context.Context, where string, size int64, digest string, copyResult func(context.Context, io.Writer) (int64, error), install func(string, string) error) error {
	f, err := os.CreateTemp(filepath.Dir(where), "."+filepath.Base(where)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hash := sha256.New()
	written, copyErr := copyResult(ctx, io.MultiWriter(f, hash))
	if copyErr == nil && (written != size || fmt.Sprintf("sha256:%x", hash.Sum(nil)) != digest) {
		copyErr = errors.New("completed result size or digest mismatch; existing file retained")
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("output choice expired before replacement; existing file retained: %w", err)
	}
	if err := install(tmp, where); err != nil {
		// A matching result reconciles an uncertain completed replacement.
		if !matchesOutput(where, size, digest) {
			return fmt.Errorf("safe output replacement failed; existing file retained: %w", err)
		}
	}
	if err := syncOutputDirectory(filepath.Dir(where)); err != nil {
		return fmt.Errorf("output replaced; directory synchronization failed, answer not acknowledged: %w", err)
	}
	return nil
}

// Match CAS's directory synchronization convention: Windows has no portable
// directory flush; POSIX flushes the rename where the filesystem supports it.
func syncOutputDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

// CAS uses this same os.Root primitive. It performs a single replacement;
// unsupported filesystems fail without a delete-then-rename fallback.
func replaceSibling(tmp, where string) error {
	root, err := os.OpenRoot(filepath.Dir(where))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Rename(filepath.Base(tmp), filepath.Base(where))
}

func matchesOutput(where string, size int64, digest string) bool {
	f, err := os.Open(where)
	if err != nil {
		return false
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	return err == nil && n == size && fmt.Sprintf("sha256:%x", hash.Sum(nil)) == digest
}
