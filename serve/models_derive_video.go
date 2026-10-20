package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"github.com/openabstractions/abstraction-provider-kit/go/sdcpp"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

// convertTimeLimit bounds one sd-cli conversion: 109 s for the 26 GB Gemma 4
// encoder on the reviewed run, with room for a slower disk.
const convertTimeLimit = 30 * time.Minute

// convertWorkPrefix names a conversion's work folder in the set folder.
const convertWorkPrefix = ".oa-convert-"

// staleConvertWork is the age after which a work folder is a leftover of a
// stopped command: a conversion's last entry, its partial output, is made
// when sd-cli starts, and sd-cli is stopped after convertTimeLimit.
const staleConvertWork = 2 * convertTimeLimit

// convertEngine is the folder of pin's staged engine as sd-server's launch
// proves it: the pinned files and nothing else. Its record is set per
// conversion.
func convertEngine(dir string, pin modelhost.EnginePin) programfiles.Tree {
	files := append(append([]programfiles.File(nil), pin.Files...), pin.Runtime.Files...)
	return programfiles.Tree{Name: "stable-diffusion.cpp " + pin.Release, Root: dir, Manifest: programfiles.Manifest{Files: files}}
}

// derive makes f, a set file no publisher serves, from its source file
// from (LTX addendum, amendment 1): from comes through a download job by
// its SHA-256, with credential for a gated publisher; the staged sd-cli in
// engine converts it on the CPU (`-M convert -m <from> --type q8_0 -o
// <partial>`) inside a job object limited to memoryLimit, from a clean
// environment, in the engine folder; the output is kept at dest only when
// its SHA-256 and size are f's. A differing output keeps nothing and fails
// the step. The work folder, with the link to or copy of the source file,
// is removed either way, and so are work folders a stopped command left.
func (g setGetter) derive(f, from pinnedFile, dest string, engine programfiles.Tree, credential string, memoryLimit uint64) error {
	folder := filepath.Dir(dest)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", g.command, err)}
	}
	removeStaleConvertWork(folder, g.output)
	work, err := os.MkdirTemp(folder, convertWorkPrefix)
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", g.command, err)}
	}
	defer os.RemoveAll(work)
	if _, err := fmt.Fprintf(g.output, "getting %s (%s) to make %s\n", from.name, humanBytes(from.size), f.name); err != nil {
		return err
	}
	input := filepath.Join(work, from.name)
	if err := g.fetch(from, input, credentialFor(from.url, credential)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(g.output, "converting %s to Q8_0\n", from.name); err != nil {
		return err
	}
	if err := convertAndKeep(g.files, engine, input, work, dest, f, memoryLimit); err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w; nothing was kept", g.command, err)}
	}
	return nil
}

// removeStaleConvertWork removes the conversion work folders in folder
// older than staleConvertWork. A folder it cannot remove is reported and
// left.
func removeStaleConvertWork(folder string, output io.Writer) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), convertWorkPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < staleConvertWork {
			continue
		}
		path := filepath.Join(folder, entry.Name())
		remove := os.RemoveAll
		if !entry.Type().IsDir() {
			// A link or junction goes by itself; nothing behind it is touched.
			remove = os.Remove
		}
		if err := remove(path); err != nil {
			//unchecked: a progress line; the leftover stays for the next run
			_, _ = fmt.Fprintf(output, "leaving %s: %v\n", path, err)
		}
	}
}

// convertAndKeep proves engine again and holds it while its sd-cli converts
// input into work, then renames the output to dest only when its size and
// SHA-256 are f's; any other output is removed. An engine folder that is
// not the pinned one runs nothing.
func convertAndKeep(files *programfiles.Verifier, engine programfiles.Tree, input, work, dest string, f pinnedFile, memoryLimit uint64) error {
	// A record of its own makes the proof hash every file in full.
	engine.Record = filepath.Join(work, "engine.files")
	hold, err := files.Launch(context.Background(), engine)
	if err != nil {
		return fmt.Errorf("the engine folder %s is not the pinned %s (%w); stage it again with modelhostd engine install --kind sdcpp", engine.Root, engine.Name, err)
	}
	//unchecked: releasing read handles after the conversion; its outcome is reported below
	defer func() { _ = hold.Release() }()
	partial := filepath.Join(work, f.name+".partial")
	defer os.Remove(partial)
	if err := runConvert(filepath.Join(engine.Root, sdCLIName()), engine.Root, input, partial, work, memoryLimit); err != nil {
		return fmt.Errorf("converting %s: %w", filepath.Base(input), err)
	}
	if info, err := os.Stat(partial); err != nil || info.Size() != f.size {
		return fmt.Errorf("the converted %s is not the pinned file of %d bytes", f.name, f.size)
	}
	if verifyDigest("convert", partial, f.sha256) != nil {
		return fmt.Errorf("the converted %s does not hash to the pinned sha256:%s", f.name, f.sha256)
	}
	return os.Rename(partial, dest)
}

// sdCLIName is the pinned release's converter program.
func sdCLIName() string {
	if runtime.GOOS == "windows" {
		return "sd-cli.exe"
	}
	return "sd-cli"
}

// convertArgs is the one sd-cli command line the step runs (LTX addendum,
// amendment 1): convert mode, the source file, Q8_0 and the output, all by
// absolute path. No other option is passed.
func convertArgs(input, output string) []string {
	return []string{"-M", "convert", "-m", input, "--type", "q8_0", "-o", output}
}

// runConvert runs sd-cli's conversion supervised: its own job object with
// the per-process memory limit, a clean environment holding no GGML_, SD_,
// VK_, HF_ or proxy variable, the engine folder as working directory and
// convertTimeLimit, after which the tree is stopped.
func runConvert(program, engineDir, input, output, temp string, memoryLimit uint64) error {
	env, err := supervision.CleanEnvironment{Temp: temp, WindowsDefaultPath: true}.Build()
	if err != nil {
		return err
	}
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if sdcpp.ForbiddenEnvironment(name) {
			return fmt.Errorf("the conversion environment carries %s", name)
		}
	}
	p, err := supervision.Start(context.Background(), supervision.Options{Executable: program, Args: convertArgs(input, output),
		Env: env, Dir: engineDir, ProcessMemoryLimit: memoryLimit, ForceTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	timer := time.AfterFunc(convertTimeLimit, func() {
		//unchecked: the exit below reports the stopped conversion
		_ = p.Stop()
	})
	defer timer.Stop()
	code, err := p.Wait()
	if err != nil {
		return err
	}
	if exit, known := p.Exit(); known && exit.MemoryLimitHit {
		return fmt.Errorf("sd-cli reached its memory limit of %d bytes", memoryLimit)
	}
	if code != 0 {
		return fmt.Errorf("sd-cli exited with code %d", code)
	}
	return nil
}
