package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-inference/go/providerhost/modelfiles"
	"github.com/openabstractions/abstraction-inference/go/providerhost/modelpack"
	"github.com/openabstractions/abstraction-inference/go/providerhost/noreplace"
)

const modelsPackUsage = `Usage: openabstractions models pack <model file or folder> <out.zip> [options]
       openabstractions models pack info|verify|restore|measure ...

Packs a model file, or a model folder, into one OpenAbstractions model
archive and back, on this machine, from files to files; it reaches no
service. The archive is a zip of STORED entries that standard zip tools
list and extract: first oa-archive.json, the index with every file's
digest, its 16 MiB span digests and its tensor table, then one entry per
file (inference CONTRACT.md INF-MP6, INF-MP9). Each BF16 and F16 tensor is
cut into units, and each unit takes the encoding that loads fastest on this
machine by its measured rates: stored, exp-huffman-v1, or the fixed 3-bit
exponent code exp-fixed3-v1 for BF16. A file whose units all stay stored is
its own entry, which any zip tool extracts as the file; a file with encoded
units is <path>.oa-units, which "restore" turns back into the file byte for
byte.

Commands:
  models pack <file or folder> <out.zip>
        pack one safetensors or GGUF file, or every file under a folder
        (weight files packed, every other file stored as it is)
  models pack info <archive.zip>
        print each file: its digest and size, units per encoding, bytes kept
  models pack verify <archive.zip> [--digest sha256:HEX] [--base FILE]
        decode every file in memory and check every 16 MiB span and every
        file's digest against the index, and that the archive holds the
        object --digest names; nothing is written
  models pack restore <archive.zip> <out> [--base FILE]
        write the original files, byte for byte, checked as they are
        written: one file to <out>, several into the folder <out>, which
        must be absent or empty
  models pack measure <file>
        measure this machine's decode rate of each encoding and the read rate
        of the volume <file> is on, and print them as --rates takes them; a
        file under 16 MiB is read whole once

pack:
  --rates bf16=G,f16=G,bf16fixed3=G,disk=G
        the rates in GB/s, as "measure" prints them. Without it pack measures
        them first, reading the largest weight file for the disk rate; weight
        files under 64 MiB in all, none of 16 MiB, are stored unmeasured
  --seconds N
        how long a measurement may take, in seconds (default 4)
  --records DIR
        also write OpenAbstractions' span record of each packed weight file
        into DIR, a provider host's span record folder (INF-MS7). Without it
        the first fill from the archive hashes the object and records it
  --base FILE
        the model the file was fine-tuned from: a safetensors file, or an
        OpenAbstractions archive holding it. Each BF16 tensor the base holds
        by name, dtype and size is tried as a delta against it, the exact
        integer difference of the values coded with the rANS coder, and kept
        where it is smaller (INF-MP12). The archive names the base by its
        SHA-256, and verify and restore need it again (--base); a base that
        is itself a delta is refused. Packs one weight file only

verify and restore:
  --base FILE
        the base a file of delta units names, as for pack; it is hashed and
        refused, before anything is decoded, unless it is that object

Pickles (.pth, .pt, .ckpt, .bin, .pkl, .pickle) are refused: loading one runs
code. No command overwrites a file: each output is written under a
temporary name beside it and renamed at the end only if nothing stands
there, and a restore that fails removes every file and folder it made.

Exit codes: 0 done, 1 a file could not be read or written, 2 usage,
3 refused (a pickle, a weight file that is not safetensors or GGUF, an
archive that fails verification, an output that already exists).
`

// packRefused marks an error as a refusal: exit status 3.
func packRefused(command string, err error) error {
	return &exitError{exitRefusedCall, fmt.Errorf("%s: %w", command, err)}
}

// packFailed is a read or write failure: exit status 1.
func packFailed(command string, err error) error {
	return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
}

// packOutcome sorts an error from the pack library into a refusal or a
// failure.
func packOutcome(command string, err error) error {
	for _, refusal := range []error{modelpack.ErrNotWeightFile, modelpack.ErrCorrupt, modelpack.ErrDType,
		modelfiles.ErrDigest, modelpack.ErrBase, errors.ErrUnsupported, errPickle, fs.ErrExist} {
		if errors.Is(err, refusal) {
			return packRefused(command, err)
		}
	}
	return packFailed(command, err)
}

var errPickle = errors.New("a pickle runs code when loaded; convert it to safetensors first")

// modelFileKind says what pack does with a file: "weights" to pack,
// "pickle" to refuse, "" to store as it is.
func modelFileKind(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".safetensors", ".sft", ".gguf"} {
		if strings.HasSuffix(lower, ext) {
			return "weights"
		}
	}
	for _, ext := range []string{".pth", ".pt", ".ckpt", ".bin", ".pkl", ".pickle"} {
		if strings.HasSuffix(lower, ext) {
			return "pickle"
		}
	}
	return ""
}

func modelsPackCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || isHelp(args[0]) || containsHelp(args) {
		_, err := io.WriteString(output, modelsPackUsage)
		return err
	}
	switch args[0] {
	case "info", "verify", "restore", "measure":
		return packSubcommand(args[0], args[1:], output, diagnostics)
	}
	return packCreate(args, output, diagnostics)
}

// packFlags is a flag set that reports mistakes in this program's shape.
func packFlags(command string) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	return flags
}

func packPath(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(p), nil
}

// packInput is one file of a pack run, with the archive path it gets.
type packInput struct {
	src, path string
	weights   bool
}

const (
	// packDiskBlock is one block of the disk measurement, 16 MiB
	// (modelpack.MeasureRates).
	packDiskBlock = 16 << 20
	// packMeasureFloor is the weight bytes below which a run whose weight
	// files are all under one block packs without measuring. It is the 4 x
	// 16 MiB a full-width disk measurement reads. Below it the input loads
	// in tens of milliseconds at SSD rates, the most an encoding saves is a
	// few milliseconds, the measurement itself takes --seconds, and a read
	// shorter than one block gives a rate that leans on the volume's
	// latency. Such a run stores every unit and says so.
	packMeasureFloor = 4 * packDiskBlock
)

// sizedInput is a weight file and its size.
type sizedInput struct {
	src  string
	size int64
}

// weightSizes is the largest weight file of a run, the one that measures
// the disk, and the weight files' total size; largest is nil when the run
// holds no weight file.
func weightSizes(inputs []packInput) (largest *sizedInput, total int64, err error) {
	for _, p := range inputs {
		if !p.weights {
			continue
		}
		st, err := os.Stat(p.src)
		if err != nil {
			return nil, 0, err
		}
		total += st.Size()
		if largest == nil || st.Size() > largest.size {
			largest = &sizedInput{p.src, st.Size()}
		}
	}
	return largest, total, nil
}

// packInputs lists the files packing in makes an archive of: the file
// itself, or every file under the folder. A pickle anywhere refuses the
// whole run before anything is written.
func packInputs(in, out string, st fs.FileInfo) ([]packInput, error) {
	if !st.IsDir() {
		switch modelFileKind(in) {
		case "pickle":
			return nil, fmt.Errorf("%s: %w", in, errPickle)
		case "":
			return nil, fmt.Errorf("%w: %s is neither a safetensors nor a GGUF file by its name", modelpack.ErrNotWeightFile, in)
		}
		return []packInput{{src: in, path: filepath.Base(in), weights: true}}, nil
	}
	var inputs []packInput
	err := filepath.WalkDir(in, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(path) // a link is followed, as in a model cache's snapshot
		if err != nil {
			return err
		}
		if info.IsDir() || path == out {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is neither a file nor a folder", path)
		}
		rel, err := filepath.Rel(in, path)
		if err != nil {
			return err
		}
		kind := modelFileKind(path)
		if kind == "pickle" {
			return fmt.Errorf("%s: %w", path, errPickle)
		}
		inputs = append(inputs, packInput{src: path, path: filepath.ToSlash(rel), weights: kind == "weights"})
		return nil
	})
	return inputs, err
}

func packCreate(args []string, output, diagnostics io.Writer) error {
	const command = "models pack"
	flags := packFlags(command)
	ratesFlag := flags.String("rates", "", "rates in GB/s")
	seconds := flags.Float64("seconds", 4, "measurement budget")
	recordsFlag := flags.String("records", "", "span record folder")
	baseFlag := flags.String("base", "", "the base model of a fine-tune")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsPackUsage, args, err)
	}
	if len(positional) != 2 {
		return flagMistake(diagnostics, command, modelsPackUsage, "name the model file or folder and the archive to write")
	}
	if *seconds <= 0 || *seconds > 120 {
		return flagMistake(diagnostics, command, modelsPackUsage, "--seconds takes 0 to 120")
	}
	var given *modelpack.Rates
	if *ratesFlag != "" {
		r, err := parseRates(*ratesFlag)
		if err != nil {
			return flagMistake(diagnostics, command, modelsPackUsage, err.Error())
		}
		given = &r
	}
	in, err := packPath(positional[0])
	if err != nil {
		return packFailed(command, err)
	}
	out, err := packPath(positional[1])
	if err != nil {
		return packFailed(command, err)
	}
	st, err := os.Stat(in)
	if err != nil {
		return packFailed(command, err)
	}
	if err := absent(out); err != nil {
		return packOutcome(command, err)
	}
	if *baseFlag != "" && st.IsDir() {
		return flagMistake(diagnostics, command, modelsPackUsage, "--base packs one weight file, not a folder")
	}
	inputs, err := packInputs(in, out, st)
	if err != nil {
		return packOutcome(command, err)
	}
	var records *modelfiles.SpanRecords
	if *recordsFlag != "" {
		dir, err := packPath(*recordsFlag)
		if err != nil {
			return packFailed(command, err)
		}
		if records, err = modelfiles.OpenSpanRecords(dir); err != nil {
			return packRefused(command, err)
		}
	}
	// Every weight file's header is read before any measurement: an empty or
	// invalid weight file refuses the run first.
	sources := make([]modelpack.ArchiveSource, 0, len(inputs))
	for _, p := range inputs {
		data, unmap, err := mapFile(p.src)
		if err != nil {
			return packFailed(command, err)
		}
		defer unmap()
		s := modelpack.ArchiveSource{Path: p.path, Data: data}
		if p.weights {
			if s.WeightFormat, s.Tensors, err = modelpack.ReadTensors(bytes.NewReader(data), int64(len(data))); err != nil {
				return packOutcome(command, fmt.Errorf("%s: %w", p.src, err))
			}
		}
		sources = append(sources, s)
	}
	if *baseFlag != "" {
		base, done, err := loadBase(*baseFlag)
		if err != nil {
			return packOutcome(command, fmt.Errorf("--base: %w", err))
		}
		defer done()
		sources[0].Base = &base.DeltaBase
		if _, err := fmt.Fprintf(output, "base %s, %d bytes\n", base.Base.Digest(), base.Length); err != nil {
			return err
		}
	}
	rates := modelpack.Rates{}
	if given != nil {
		rates = *given
	} else {
		largest, total, err := weightSizes(inputs)
		if err != nil {
			return packFailed(command, err)
		}
		switch {
		case largest == nil:
			// No weight file: every file is stored as it is.
		case largest.size < packDiskBlock && total < packMeasureFloor:
			if _, err := fmt.Fprintf(output, "not measured: the weight files hold %d bytes, under 64 MiB, none of 16 MiB; every unit is stored\n", total); err != nil {
				return err
			}
		default:
			r, err := modelpack.MeasureRates(context.Background(), largest.src, time.Duration(*seconds*float64(time.Second)))
			if err != nil {
				return packFailed(command, fmt.Errorf("measuring this machine: %w", err))
			}
			if _, err := fmt.Fprintf(output, "measured: %s (disk read from %s)\n", formatRates(r), largest.src); err != nil {
				return err
			}
			rates = r
		}
	}
	var archive modelpack.ModelArchive
	err = writeNew(out, func(w io.Writer) error {
		var err error
		archive, err = modelpack.WriteZip(w, sources, rates, records)
		return err
	})
	if err != nil {
		return packOutcome(command, err)
	}
	if _, err := fmt.Fprintf(output, "packed %s\n", out); err != nil {
		return err
	}
	return printArchive(output, archive, sources)
}

// absent refuses a path where something already stands.
func absent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s: %w; no command overwrites a file", path, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeNew writes path through fill under a temporary name in path's
// folder and renames it to path at the end, only if nothing stands at path
// then. The temporary file is removed on any failure; path never holds part
// of a file.
func writeNew(path string, fill func(w io.Writer) error) (err error) {
	if err := absent(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.oa-partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			//unchecked: removing this call's own temporary file; the write error is what the caller is told
			os.Remove(tmp)
		}
	}()
	w := bufio.NewWriterSize(f, 4<<20)
	err = fill(w)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = noreplace.RenameNew(tmp, path); errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s: %w; no command overwrites a file", path, fs.ErrExist)
	}
	return err
}

// mkdirNew makes dir and its missing parents, appending each folder it
// made to made, outermost first.
func mkdirNew(dir string, made *[]string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			return err
		}
		*made = append(*made, missing[i])
	}
	return nil
}

// fileSummary counts one file's bytes by dtype and by encoding.
type fileSummary struct {
	byDType   map[string][2]int64             // bytes in, bytes out
	byEncoder map[modelpack.Encoding][3]int64 // units, bytes in, bytes out
}

// summarize attributes each unit to the tensor that holds it whole, and
// every other unit to "other".
func summarize(index modelpack.Index, tensors []modelpack.Tensor) fileSummary {
	s := fileSummary{byDType: map[string][2]int64{}, byEncoder: map[modelpack.Encoding][3]int64{}}
	for _, u := range index.Chunks {
		e := s.byEncoder[u.Encoding]
		s.byEncoder[u.Encoding] = [3]int64{e[0] + 1, e[1] + u.Length, e[2] + u.Packed}
		dtype := "other"
		// The first tensor that ends after the unit starts is the only one
		// that can hold it.
		i := sort.Search(len(tensors), func(k int) bool { return tensors[k].Offset+tensors[k].Length > u.Offset })
		if i < len(tensors) {
			if t := tensors[i]; t.Offset <= u.Offset && u.Offset+u.Length <= t.Offset+t.Length {
				dtype = t.DType
			}
		}
		d := s.byDType[dtype]
		s.byDType[dtype] = [2]int64{d[0] + u.Length, d[1] + u.Packed}
	}
	return s
}

func kept(out, in int64) string {
	if in == 0 {
		return "-"
	}
	return strconv.FormatFloat(float64(out)/float64(in), 'f', 4, 64)
}

// printArchive prints each file of an archive; sources, when given, name
// the tensors that attribute bytes to dtypes.
func printArchive(output io.Writer, archive modelpack.ModelArchive, sources []modelpack.ArchiveSource) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	var in, out int64
	for i, f := range archive.Files {
		var tensors []modelpack.Tensor
		if i < len(sources) {
			tensors = sources[i].Tensors
		} else {
			for _, t := range f.TensorTable {
				tensors = append(tensors, modelpack.Tensor{Offset: t.Offset, Length: t.Length, DType: t.DType})
			}
		}
		s := summarize(f.Index, tensors)
		var packed int64
		for _, c := range f.Index.Chunks {
			packed += c.Packed
		}
		in += f.Index.Length
		out += packed
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%d bytes\tkept %s\tentry %s\n", f.Path, f.Index.Digest, f.Index.Length, kept(packed, f.Index.Length), f.Entry)
		if f.Index.Base != nil {
			//unchecked: as below
			fmt.Fprintf(table, "  delta against\t%s\t%d bytes\t\t\n", f.Index.Base.Digest, f.Index.Base.Length)
		}
		for _, k := range slices.Sorted(maps.Keys(s.byDType)) {
			v := s.byDType[k]
			//unchecked: as above
			fmt.Fprintf(table, "  %s\t%d\t%d\tkept %s\t\n", k, v[0], v[1], kept(v[1], v[0]))
		}
		for _, k := range slices.Sorted(maps.Keys(s.byEncoder)) {
			v := s.byEncoder[k]
			//unchecked: as above
			fmt.Fprintf(table, "  %s\t%d units\t%d\t%d\t\n", k, v[0], v[1], v[2])
		}
	}
	//unchecked: as above
	fmt.Fprintf(table, "total\t%d files\t%d bytes\tkept %s\t\n", len(archive.Files), in, kept(out, in))
	return table.Flush()
}

func packSubcommand(sub string, args []string, output, diagnostics io.Writer) error {
	command := "models pack " + sub
	flags := packFlags(command)
	digest := flags.String("digest", "", "the object digest the archive must hold")
	seconds := flags.Float64("seconds", 4, "measurement budget")
	baseFlag := flags.String("base", "", "the base model a file of delta units names")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, modelsPackUsage, args, err)
	}
	want := map[string]int{"info": 1, "verify": 1, "restore": 2, "measure": 1}[sub]
	if len(positional) != want {
		return flagMistake(diagnostics, command, modelsPackUsage, fmt.Sprintf("takes %d path(s)", want))
	}
	if *digest != "" && sub != "verify" {
		return flagMistake(diagnostics, command, modelsPackUsage, "--digest belongs to models pack verify")
	}
	if *baseFlag != "" && sub != "verify" && sub != "restore" {
		return flagMistake(diagnostics, command, modelsPackUsage, "--base belongs to models pack, verify and restore")
	}
	if *seconds <= 0 || *seconds > 120 {
		return flagMistake(diagnostics, command, modelsPackUsage, "--seconds takes 0 to 120")
	}
	path, err := packPath(positional[0])
	if err != nil {
		return packFailed(command, err)
	}
	if sub == "measure" {
		r, err := modelpack.MeasureRates(context.Background(), path, time.Duration(*seconds*float64(time.Second)))
		if err != nil {
			return packFailed(command, err)
		}
		_, err = fmt.Fprintf(output, "%s\n", formatRates(r))
		return err
	}
	archive, err := modelpack.ReadZip(path)
	if err != nil {
		return packOutcome(command, err)
	}
	var base modelpack.ProvenBase
	if *baseFlag != "" {
		b, done, err := loadBase(*baseFlag)
		if err != nil {
			return packOutcome(command, fmt.Errorf("--base: %w", err))
		}
		defer done()
		base = b.Base
	}
	switch sub {
	case "info":
		return printArchive(output, archive, nil)
	case "verify":
		if _, ok := archive.Lookup(*digest); *digest != "" && !ok {
			return packRefused(command, fmt.Errorf("%w: the archive holds no object %s", modelfiles.ErrDigest, *digest))
		}
		for _, f := range archive.Files {
			if err := modelpack.RestoreFileWithBase(context.Background(), io.Discard, f, base, 0); err != nil {
				return packOutcome(command, fmt.Errorf("%s: %w", f.Path, err))
			}
			if _, err := fmt.Fprintf(output, "verified %s: %s, %d bytes\n", f.Path, f.Index.Digest, f.Index.Length); err != nil {
				return err
			}
		}
		return nil
	}
	out, err := packPath(positional[1])
	if err != nil {
		return packFailed(command, err)
	}
	return packRestore(command, archive, base, out, output)
}

// packRestore writes an archive's files: one file to out, several into the
// folder out, which must be absent or empty. Each file is written under a
// temporary name and renamed at its end without replacing anything. When
// any file fails, every file and folder this run made is removed again.
func packRestore(command string, archive modelpack.ModelArchive, base modelpack.ProvenBase, out string, output io.Writer) (err error) {
	targets := map[string]string{}
	if len(archive.Files) == 1 {
		targets[archive.Files[0].Path] = out
	} else {
		if st, err := os.Lstat(out); err == nil && !st.IsDir() {
			return packRefused(command, fmt.Errorf("%s: %w and is no folder; no command overwrites a file", out, fs.ErrExist))
		}
		if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
			return packRefused(command, fmt.Errorf("%s: %w and is not empty; no command overwrites a file", out, fs.ErrExist))
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return packFailed(command, err)
		}
		for _, f := range archive.Files {
			// ReadZip refused any path an extraction could not write as given.
			targets[f.Path] = filepath.Join(out, filepath.FromSlash(f.Path))
		}
	}
	var made, written []string
	defer func() {
		if err == nil {
			return
		}
		for _, p := range slices.Backward(written) {
			//unchecked: undoing this run's own output; the failure is what the caller is told
			os.Remove(p)
		}
		for _, d := range slices.Backward(made) {
			//unchecked: as above; a folder something else wrote into stays
			os.Remove(d)
		}
	}()
	for _, f := range archive.Files {
		dst := targets[f.Path]
		if err := mkdirNew(filepath.Dir(dst), &made); err != nil {
			return packFailed(command, err)
		}
		err := writeNew(dst, func(w io.Writer) error { return modelpack.RestoreFileWithBase(context.Background(), w, f, base, 0) })
		if err != nil {
			return packOutcome(command, fmt.Errorf("%s: %w", f.Path, err))
		}
		written = append(written, dst)
		if _, err := fmt.Fprintf(output, "restored %s: %s, %d bytes\n", dst, f.Index.Digest, f.Index.Length); err != nil {
			return err
		}
	}
	return nil
}

// formatRates prints rates in GB/s, the form --rates takes.
func formatRates(r modelpack.Rates) string {
	g := func(v float64) string { return strconv.FormatFloat(v/1e9, 'g', 4, 64) }
	return "bf16=" + g(r.BF16) + ",f16=" + g(r.F16) + ",bf16fixed3=" + g(r.BF16Fixed3) + ",disk=" + g(r.Disk)
}

// parseRates reads rates in GB/s; every name is required once.
func parseRates(s string) (modelpack.Rates, error) {
	var r modelpack.Rates
	fields := map[string]*float64{"bf16": &r.BF16, "f16": &r.F16, "bf16fixed3": &r.BF16Fixed3, "disk": &r.Disk}
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		field, known := fields[name]
		if !ok || !known || seen[name] {
			return r, fmt.Errorf("--rates takes bf16=G,f16=G,bf16fixed3=G,disk=G in GB/s, not %q", part)
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || v < 0 || v > 1e4 {
			return r, fmt.Errorf("--rates %s=%s is not a rate in GB/s", name, value)
		}
		*field, seen[name] = v*1e9, true
	}
	if len(seen) != len(fields) {
		return r, errors.New("--rates names bf16, f16, bf16fixed3 and disk")
	}
	return r, nil
}

// loadedBase is a base model for --base: its proven bytes and tensors, and
// its length.
type loadedBase struct {
	modelpack.DeltaBase
	Length int64
}

// loadBase reads --base: a safetensors file, mapped read-only with writes
// and deletes denied (mapLocked) and hashed whole, or an OpenAbstractions
// archive holding one weight file, restored into memory and checked
// against its index's digest. A base that is itself a delta is refused:
// deltas are one level deep (INF-MP12). The returned ProvenBase borrows this
// storage, so the command keeps it immutable through every operation and
// calls done only after its last use. On Windows the source file stays locked
// against writes and deletes and is mapped read-only from hash through decode;
// Unix and archive inputs use a command-owned private buffer.
func loadBase(arg string) (loadedBase, func(), error) {
	path, err := packPath(arg)
	if err != nil {
		return loadedBase{}, nil, err
	}
	done := func() {}
	var data []byte
	if strings.HasSuffix(strings.ToLower(path), ".zip") {
		archive, err := modelpack.ReadZip(path)
		if err != nil {
			return loadedBase{}, nil, err
		}
		var weights []modelpack.ArchiveFile
		for _, f := range archive.Files {
			if f.WeightFormat != "" {
				weights = append(weights, f)
			}
		}
		if len(weights) != 1 {
			return loadedBase{}, nil, fmt.Errorf("%w: %s holds %d weight files; a base archive holds one", modelpack.ErrCorrupt, path, len(weights))
		}
		if weights[0].Index.Base != nil {
			return loadedBase{}, nil, fmt.Errorf("%w: %s is itself a delta against %s; deltas are one level deep", modelpack.ErrCorrupt, path, weights[0].Index.Base.Digest)
		}
		var b bytes.Buffer
		b.Grow(int(weights[0].Index.Length))
		if err := modelpack.RestoreFile(context.Background(), &b, weights[0], 0); err != nil {
			return loadedBase{}, nil, err
		}
		data = b.Bytes()
	} else {
		d, unmap, err := mapLocked(path)
		if err != nil {
			return loadedBase{}, nil, err
		}
		data, done = d, unmap
	}
	_, tensors, err := modelpack.ReadTensors(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		done()
		return loadedBase{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	return loadedBase{DeltaBase: modelpack.DeltaBase{Base: modelpack.ProveBase(data), Tensors: tensors}, Length: int64(len(data))}, done, nil
}
