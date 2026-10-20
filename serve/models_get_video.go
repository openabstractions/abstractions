package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"github.com/openabstractions/abstraction-download/go/archive"
	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

// pinnedFile is one file the runtime's download service fetches by its
// SHA-256: the publisher's URL, the exact size and the file's name.
type pinnedFile struct {
	url, sha256, name string
	size              int64
}

// videoSetFiles are the downloads an sd-server video set needs: the pinned
// stable-diffusion.cpp archive, then each of the set's files.
func videoSetFiles(set modelhost.VideoModelSet, pin modelhost.EnginePin) []pinnedFile {
	out := []pinnedFile{{url: pin.ArchiveURL, sha256: pin.ArchiveSHA256, size: pin.ArchiveSize, name: pin.Archive}}
	for _, f := range set.Files {
		out = append(out, pinnedFile{url: f.Source, sha256: f.SHA256, size: f.Size, name: f.Name})
	}
	return out
}

// getVideoSet makes an sd-server video set ready on this computer
// (models host --video-model SET --get): each file comes through a download
// job of the runtime's download service by its SHA-256 (the service's
// archive fills it when it holds it), the engine archive is staged with
// modelhostd engine install, and the set's files are placed with modelhostd
// models install. It returns the staged sd-server. A file with no publisher
// download, such as LTX-2.5's Q8_0 encoder, comes from the download service
// when it holds a copy, and is otherwise converted from its source file by
// the staged sd-cli, proved again just before it runs (setGetter.place).
func getVideoSet(w *waiting, command string, source endpointSource, machine *client.Machine, setName, hostProgram, state, credential string, output io.Writer) (string, error) {
	set, ok := modelhost.VideoModelSetNamed(setName)
	if !ok {
		return "", &exitError{exitRefusedCall, fmt.Errorf("%s: no video model set is named %q", command, setName)}
	}
	pin, ok := modelhost.PinnedVideoEngine(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return "", &exitError{exitRefusedCall, fmt.Errorf("%s: no reviewed stable-diffusion.cpp release runs on this computer", command)}
	}
	if hostProgram == "" {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: modelhostd is not beside this command; pass --program", command)}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	folder := filepath.Join(modelhost.ModelsRoot(home), set.Name)
	staging, err := os.MkdirTemp("", "oa-video-engine-")
	if err != nil {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	defer os.RemoveAll(staging)
	files := videoSetFiles(set, pin)
	engine := files[0]
	if _, err := fmt.Fprintf(output, "getting %s (%s) %s\n", engine.name, humanBytes(engine.size), sourceOf(downloadServiceHolds, engine)); err != nil {
		return "", err
	}
	if err := fetchPinned(w, command, source, machine, engine, filepath.Join(staging, engine.name), "", fileProgress(output, engine)); err != nil {
		return "", err
	}
	installArgs := []string{"engine", "install", "--kind", "sdcpp", "--archive", filepath.Join(staging, pin.Archive)}
	if state != "" {
		installArgs = append(installArgs, "--state", state)
	}
	said, err := runModelHost(hostProgram, installArgs)
	if err != nil {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: staging stable-diffusion.cpp %s: %w", command, pin.Release, err)}
	}
	_, program, found := strings.Cut(said, "pass --video-engine ")
	program = strings.TrimSpace(program)
	if !found || !filepath.IsAbs(program) {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: modelhostd engine install named no engine", command)}
	}
	g := setGetter{
		fetch: func(f pinnedFile, dest, credential string) error {
			return fetchPinned(w, command, source, machine, f, dest, credential, fileProgress(output, f))
		},
		holds:   downloadServiceHolds,
		derived: derivedSource,
		files:   programfiles.NewVerifier(programfiles.Options{}),
		command: command,
		output:  output,
	}
	engineTree := convertEngine(filepath.Dir(program), pin)
	for _, f := range files[1:] {
		dest := filepath.Join(folder, f.name)
		if same, _ := fileIs(dest, f); same {
			continue
		}
		if err := g.place(f, dest, engineTree, credential, set.MemoryLimit); err != nil {
			return "", err
		}
	}
	placeArgs := []string{"models", "install", "--kind", "sdcpp", "--set", set.Name, "--from", folder}
	if state != "" {
		placeArgs = append(placeArgs, "--state", state)
	}
	if _, err := runModelHost(hostProgram, placeArgs); err != nil {
		return "", &exitError{exitNotResolved, fmt.Errorf("%s: placing the %s files: %w", command, set.Name, err)}
	}
	_, err = fmt.Fprintf(output, "%s is ready: stable-diffusion.cpp %s at %s, its files in %s\n", set.DisplayTitle(), pin.Release, program, folder)
	return program, err
}

// setGetter gets a set's files: fetch runs one download job by SHA-256,
// holds asks whether the download service's archive lists a copy, derived
// names the file a set file is made from, and files proves the engine
// folder before sd-cli converts.
type setGetter struct {
	fetch   func(f pinnedFile, dest, credential string) error
	holds   func(f pinnedFile) bool
	derived func(f pinnedFile) (pinnedFile, bool)
	files   *programfiles.Verifier
	command string
	output  io.Writer
}

// place makes dest hold f. A file with a publisher download comes through a
// download job. A derived file comes from the download service's copy when
// its archive lists one; otherwise, and when that copy cannot be given, it
// is converted from its source file (deriveFile).
func (g setGetter) place(f pinnedFile, dest string, engine programfiles.Tree, credential string, memoryLimit uint64) error {
	from, derived := g.derived(f)
	if !derived {
		if _, err := fmt.Fprintf(g.output, "getting %s (%s) %s\n", f.name, humanBytes(f.size), sourceOf(g.holds, f)); err != nil {
			return err
		}
		return g.fetch(f, dest, credentialFor(f.url, credential))
	}
	if g.holds(f) {
		if _, err := fmt.Fprintf(g.output, "getting %s (%s) from the download archive\n", f.name, humanBytes(f.size)); err != nil {
			return err
		}
		// The derived file's source is the gated file it is made from. The
		// job carries no credential, so that source refuses it at once and
		// only the archive can give the pinned bytes.
		err := g.fetch(f, dest, "")
		if err == nil {
			return nil
		}
		if _, err := fmt.Fprintf(g.output, "the download archive did not give %s (%v); making it\n", f.name, err); err != nil {
			return err
		}
	}
	return g.derive(f, from, dest, engine, credential, memoryLimit)
}

// derivedSource is the publisher file f is converted from when no
// publisher serves f itself.
func derivedSource(f pinnedFile) (pinnedFile, bool) {
	from, ok := modelhost.Derived(modelhost.VideoModelFile{SHA256: f.sha256})
	if !ok {
		return pinnedFile{}, false
	}
	return pinnedFile{url: from.Source, sha256: from.SHA256, size: from.Size, name: from.Name}, true
}

// fileIs reports whether path already holds f: its size, then its SHA-256.
func fileIs(path string, f pinnedFile) (bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.size {
		return false, err
	}
	return verifyDigest("models host", path, f.sha256) == nil, nil
}

// downloadServiceHolds reports whether the download service's archive lists
// a copy of f, after one look at it when its listing names none.
func downloadServiceHolds(f pinnedFile) bool {
	dir, err := archive.StateFor("")
	if err != nil {
		return false
	}
	archive.UseState(dir)
	want := []archive.Want{{SHA256: f.sha256, Bytes: f.size}}
	if done := archive.LookFor(want); done != nil {
		select {
		case <-done:
		case <-time.After(time.Minute):
		}
	}
	return archive.Holds(want)
}

// credentialFor is the credential a source needs: Lightricks' repository on
// Hugging Face is gated, so its files carry the person's named credential.
func credentialFor(source, credential string) string {
	u, err := url.Parse(source)
	if err != nil || u.Host != "huggingface.co" || !strings.HasPrefix(u.Path, "/Lightricks/") {
		return ""
	}
	return credential
}

// fetchPinned runs one download job for f, as openabstractions download
// does, and gives the verified result the name dest: a hard link from the
// job service where it can make one, else a copy hashed before the rename.
// sourceOf names where f comes from: the download archive when its listing
// holds f, else the publisher on the internet.
func sourceOf(holds func(pinnedFile) bool, f pinnedFile) string {
	if holds != nil && holds(f) {
		return "from the download archive"
	}
	return "from the internet"
}

// fileProgress reports a file's download job as it moves: the bytes done
// of its size, at most every 5 percent or 15 seconds.
func fileProgress(output io.Writer, f pinnedFile) func(*api.OperationSnapshot) error {
	said := time.Now()
	var saidDone int64
	return func(s *api.OperationSnapshot) error {
		done, total := s.Progress.Done, s.Progress.Total
		if total <= 0 {
			total = f.size
		}
		if done <= 0 || done == saidDone || done < total && done-saidDone < total/20 && time.Since(said) < 15*time.Second {
			return nil
		}
		said, saidDone = time.Now(), done
		_, err := fmt.Fprintf(output, "  %s: %s of %s\n", f.name, humanBytes(done), humanBytes(total))
		return err
	}
}

func fetchPinned(w *waiting, command string, source endpointSource, machine *client.Machine, f pinnedFile, dest, credential string, progress func(*api.OperationSnapshot) error) error {
	src := request.Source{Scheme: "https", Locator: f.url, Credential: credential}
	required := slices.Clone(api.AdmissionGuarantees)
	if credential != "" {
		required = append(required, request.CredentialGuarantees...)
	}
	payload := request.Encode(&request.Request{Artifact: request.Artifact{Digest: "sha256:" + f.sha256, Size: f.size}, Sources: []request.Source{src}})
	label, err := api.NormalizeLabel(f.name)
	if err != nil {
		label = ""
	}
	submission := api.Submission{Kind: download.Kind, Spec: payload, RequiredGuarantees: required, Label: label}
	jobs, err := resolveAcceptanceRequiring(w, command, source, machine, required)
	if err != nil {
		return err
	}
	call, done := w.call()
	window, err := jobs.GetHistoryWindow(call)
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	submission.Identity = api.RequestIdentity{HistoryEpoch: window.HistoryEpoch, Key: submissionKey(submission)}
	operations, err := resolveOperations(w, command, source, machine)
	if err != nil {
		return err
	}
	latest, err := latestAttempt(w, command, operations, submission.Identity)
	if err != nil {
		return err
	}
	submission.Identity.Attempt = latest.attempt
	if latest.found && (latest.sealed || latest.snapshot != nil && (latest.snapshot.State == api.WorkStateFailed || latest.snapshot.State == api.WorkStateCancelled)) {
		submission.Identity.Attempt++
	}
	if _, err := submit(w, command, jobs, submission); err != nil {
		return err
	}
	snapshot, err := follow(w, command, operations, submission.Identity, progress)
	if err != nil {
		return err
	}
	if err := terminalExit(command, snapshot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if linkResults {
		call, done = w.call()
		linked, err := operations.LinkResult(call, submission.Identity, dest)
		done()
		if err == nil && linked.Outcome == api.LinkOutcomeLinked {
			return nil
		}
	}
	_, err = copyResult(w, command, operations, submission.Identity, dest, func(partial string) error {
		return verifyDigest(command, partial, f.sha256)
	})
	return err
}

// runModelHost runs one modelhostd command and returns its output; a
// failure carries its first line.
func runModelHost(program string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var stdout, stderr strings.Builder
	cmd := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if first == "" {
			return "", err
		}
		return "", errors.New(first)
	}
	return stdout.String(), nil
}
