package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-inference/go/providerhost/modelfiles"
	"github.com/openabstractions/abstraction-inference/go/providerhost/modelpack"
)

// testRates are fixed so the choice is deterministic: BF16's
// exp-huffman-v1 decodes slower than the disk can feed it, so BF16 takes the
// fixed code; F16 takes exp-huffman-v1.
const testRates = "--rates=bf16=7,f16=15,bf16fixed3=20,disk=6.5"

// halfWeights draws n normal values (standard deviation 0.02) as BF16, or
// as F16 when half is set.
func halfWeights(n int, seed uint64, half bool) []byte {
	r := rand.New(rand.NewPCG(seed, 3))
	out := make([]byte, 2*n)
	for i := range n {
		f := float32(r.NormFloat64() * 0.02)
		b := math.Float32bits(f)
		v := uint16((b + 0x7fff + (b>>16)&1) >> 16)
		if half {
			v = uint16(math.Float32bits(f)>>16&0x8000) | f16Bits(f)
		}
		binary.LittleEndian.PutUint16(out[2*i:], v)
	}
	return out
}

// f16Bits is |f| as F16 for the small normals halfWeights draws (no
// subnormal or overflow handling, truncating).
func f16Bits(f float32) uint16 {
	b := math.Float32bits(f) &^ (1 << 31)
	e := int(b>>23) - 112
	if e <= 0 {
		return 0
	}
	return uint16(e)<<10 | uint16(b>>13&0x3ff)
}

type stEntry struct {
	name, dtype string
	data        []byte
}

func safetensors(entries ...stEntry) []byte {
	header := map[string]any{"__metadata__": map[string]string{"format": "pt"}}
	var data []byte
	for _, e := range entries {
		header[e.name] = map[string]any{"dtype": e.dtype, "shape": []int{len(e.data)}, "data_offsets": []int{len(data), len(data) + len(e.data)}}
		data = append(data, e.data...)
	}
	h, _ := json.Marshal(header)
	return slices.Concat(binary.LittleEndian.AppendUint64(nil, uint64(len(h))), h, data)
}

// gguf builds a GGUF v3 file with one key and the tensors' data aligned to 32.
func gguf(tensors ...struct {
	typ  uint32
	data []byte
}) []byte {
	str := func(b []byte, s string) []byte {
		return append(binary.LittleEndian.AppendUint64(b, uint64(len(s))), s...)
	}
	b := []byte("GGUF")
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(tensors)))
	b = binary.LittleEndian.AppendUint64(b, 1)
	b = str(b, "general.architecture")
	b = binary.LittleEndian.AppendUint32(b, 8)
	b = str(b, "llama")
	var data []byte
	for i, t := range tensors {
		for len(data)%32 != 0 {
			data = append(data, 0)
		}
		b = str(b, "t"+string(rune('a'+i)))
		b = binary.LittleEndian.AppendUint32(b, 1)
		elements := uint64(len(t.data)) // a quantised type's count does not size it here
		switch t.typ {
		case 0:
			elements /= 4
		case 1, 30:
			elements /= 2
		}
		b = binary.LittleEndian.AppendUint64(b, elements)
		b = binary.LittleEndian.AppendUint32(b, t.typ)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
		data = append(data, t.data...)
	}
	for len(b)%32 != 0 {
		b = append(b, 0)
	}
	return append(b, data...)
}

func runPack(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diag bytes.Buffer
	err := modelsCommand(append([]string{"pack"}, args...), &out, &diag)
	return out.String() + diag.String(), err
}

func exitCode(err error) int {
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	if err != nil {
		return 1
	}
	return 0
}

// INF-MP9: a safetensors file with BF16, F16, F32 and integer tensors and
// metadata packs, verifies, and restores byte for byte; info names the
// object; --records writes OA's span record.
func TestModelsPackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := safetensors(
		stEntry{"layer.weight", "BF16", halfWeights(400_000, 1, false)},
		stEntry{"proj.weight", "F16", halfWeights(300_001, 2, true)},
		stEntry{"norm.weight", "F32", make([]byte, 4096)},
		stEntry{"positions", "I64", make([]byte, 800)},
	)
	in := filepath.Join(dir, "model.safetensors")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(dir, "model.oapack.zip")
	records := filepath.Join(dir, "records")
	out, err := runPack(t, in, pack, testRates, "--records", records)
	if err != nil {
		t.Fatalf("pack: %v\n%s", err, out)
	}
	for _, want := range []string{"BF16", "F16", "exp-fixed3-v1", "exp-huffman-v1", "stored", "kept"} {
		if !strings.Contains(out, want) {
			t.Errorf("pack summary lacks %q:\n%s", want, out)
		}
	}
	sum := sha256.Sum256(src)
	digest := "sha256:" + hexOf(sum[:])
	if out, err := runPack(t, "info", pack); err != nil || !strings.Contains(out, digest) {
		t.Fatalf("info: %v\n%s", err, out)
	}
	if out, err := runPack(t, "verify", pack, "--digest", digest); err != nil || !strings.Contains(out, "verified") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	restored := filepath.Join(dir, "restored.safetensors")
	if out, err := runPack(t, "restore", pack, restored); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	got, err := os.ReadFile(restored)
	if err != nil || !bytes.Equal(got, src) {
		t.Fatalf("restored file differs: %v", err)
	}
	r, err := modelfiles.OpenSpanRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup(digest); !ok {
		t.Fatal("--records wrote no span record")
	}
	// Nothing is overwritten.
	if _, err := runPack(t, "restore", pack, restored); exitCode(err) != 3 {
		t.Fatalf("restore over an existing file: %v", err)
	}
	if _, err := runPack(t, "verify", pack, "--digest", "sha256:"+strings.Repeat("0", 64)); exitCode(err) != 3 {
		t.Fatalf("verify against another digest: %v", err)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 2*len(b))
	for i, c := range b {
		out[2*i], out[2*i+1] = digits[c>>4], digits[c&15]
	}
	return string(out)
}

// INF-MP9: a GGUF file's quantised tensors stay stored; its F16 tensor is
// encoded; the pack restores byte for byte.
func TestModelsPackGGUF(t *testing.T) {
	dir := t.TempDir()
	src := gguf(
		struct {
			typ  uint32
			data []byte
		}{12, bytes.Repeat([]byte{0x5a, 0x13, 0xc7}, 200_000)}, // Q4_K bytes
		struct {
			typ  uint32
			data []byte
		}{1, halfWeights(300_000, 3, true)},
	)
	in := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(dir, "model.oapack.zip")
	if out, err := runPack(t, in, pack, testRates); err != nil {
		t.Fatalf("pack: %v\n%s", err, out)
	}
	abs, _ := filepath.Abs(pack)
	archive, err := modelpack.ReadZip(abs)
	if err != nil {
		t.Fatal(err)
	}
	index := archive.Files[0].Index
	if index.Chunks[0].Encoding != modelpack.EncodingStored || index.Chunks[0].Length < 600_000 {
		t.Fatalf("the quantised bytes: %+v", index.Chunks[0])
	}
	encoded := 0
	for _, c := range index.Chunks {
		if c.Encoding == modelpack.EncodingExpHuffmanV1 {
			encoded++
		}
	}
	if encoded == 0 {
		t.Fatal("the F16 tensor was not encoded")
	}
	restored := filepath.Join(dir, "back.gguf")
	if out, err := runPack(t, "restore", pack, restored); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(restored); !bytes.Equal(got, src) {
		t.Fatal("restored GGUF differs")
	}
}

// INF-MP9: a pack whose units changed fails verify with exit status 3 and
// restore leaves no file.
func TestModelsPackCorruptFailsVerify(t *testing.T) {
	dir := t.TempDir()
	src := safetensors(stEntry{"w", "BF16", halfWeights(300_000, 4, false)})
	in := filepath.Join(dir, "model.safetensors")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(dir, "model.oapack.zip")
	if out, err := runPack(t, in, pack, testRates); err != nil {
		t.Fatalf("pack: %v\n%s", err, out)
	}
	b, err := os.ReadFile(pack)
	if err != nil {
		t.Fatal(err)
	}
	b[64<<10+5000] ^= 0x40 // inside the units, which start at 64 KiB
	if err := os.WriteFile(pack, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runPack(t, "verify", pack); exitCode(err) != 3 {
		t.Fatalf("verify of a changed pack: %v\n%s", err, out)
	}
	restored := filepath.Join(dir, "back.safetensors")
	if _, err := runPack(t, "restore", pack, restored); exitCode(err) != 3 {
		t.Fatalf("restore of a changed pack: %v", err)
	}
	if _, err := os.Stat(restored); !os.IsNotExist(err) {
		t.Fatal("a failed restore left its file")
	}
}

// INF-MP9: pickles are refused, alone and inside a folder, before anything
// is written.
func TestModelsPackRefusesPickles(t *testing.T) {
	dir := t.TempDir()
	pth := filepath.Join(dir, "model.pth")
	if err := os.WriteFile(pth, []byte{0x80, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runPack(t, pth, filepath.Join(dir, "out.zip"), testRates); exitCode(err) != 3 {
		t.Fatalf("a pickle: %v", err)
	}
	folder := filepath.Join(dir, "model")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "model.safetensors"), safetensors(stEntry{"w", "BF16", halfWeights(1000, 5, false)}), 0o600)
	os.WriteFile(filepath.Join(folder, "pytorch_model.bin"), []byte{0x80, 0x02}, 0o600)
	out := filepath.Join(dir, "packed.zip")
	if _, err := runPack(t, folder, out, testRates); exitCode(err) != 3 {
		t.Fatalf("a folder holding a pickle: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("a refused folder pack wrote its archive")
	}
	if _, err := runPack(t, filepath.Join(dir, "notes.txt"), filepath.Join(dir, "x.zip"), testRates); exitCode(err) == 0 {
		t.Fatal("a file that is no model was packed")
	}
}

// INF-MP9: a folder packs each weight file and copies every other file.
func TestModelsPackFolder(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "model")
	os.MkdirAll(filepath.Join(folder, "tokenizer"), 0o755)
	weights := safetensors(stEntry{"w", "BF16", halfWeights(300_000, 6, false)})
	files := map[string][]byte{
		"model.safetensors":        weights,
		"config.json":              []byte(`{"architectures":["X"]}`),
		"tokenizer/tokenizer.json": []byte(`{"model":{}}`),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(folder, filepath.FromSlash(name)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pack := filepath.Join(dir, "model.oa.zip")
	if text, err := runPack(t, folder, pack, testRates); err != nil {
		t.Fatalf("folder pack: %v\n%s", err, text)
	}
	// The other files are their own STORED entries: any zip reader gets them.
	zr, err := zip.OpenReader(pack)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if b, ok := files[f.Name]; ok {
			rc, _ := f.Open()
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || !bytes.Equal(got, b) {
				t.Fatalf("archive/zip reads %s as other bytes: %v", f.Name, err)
			}
		}
	}
	zr.Close()
	slices.Sort(names)
	if want := []string{"config.json", "model.safetensors.oa-units", "oa-archive.json", "tokenizer/tokenizer.json"}; !slices.Equal(names, want) {
		t.Fatalf("entries %v, want %v", names, want)
	}
	if text, err := runPack(t, "verify", pack); err != nil {
		t.Fatalf("verify the folder's archive: %v\n%s", err, text)
	}
	back := filepath.Join(dir, "restored")
	if text, err := runPack(t, "restore", pack, back); err != nil {
		t.Fatalf("restore the folder: %v\n%s", err, text)
	}
	for name, b := range files {
		got, err := os.ReadFile(filepath.Join(back, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("%s restored as other bytes: %v", name, err)
		}
	}
	if _, err := runPack(t, folder, pack, testRates); exitCode(err) != 3 {
		t.Fatalf("packing over an existing archive: %v", err)
	}
	if _, err := runPack(t, "restore", pack, back); exitCode(err) != 3 {
		t.Fatalf("restoring into a folder that is not empty: %v", err)
	}
	if _, err := runPack(t, "restore", pack, filepath.Join(back, "config.json")); exitCode(err) != 3 {
		t.Fatalf("restoring several files over a file: %v", err)
	}

	// A restore that fails on its last file removes every file and folder it
	// made: tokenizer/tokenizer.json, the last entry, has one byte changed.
	b, err := os.ReadFile(pack)
	if err != nil {
		t.Fatal(err)
	}
	zr, err = zip.OpenReader(pack)
	if err != nil {
		t.Fatal(err)
	}
	last := zr.File[len(zr.File)-1]
	off, err := last.DataOffset()
	zr.Close()
	if err != nil || last.Name != "tokenizer/tokenizer.json" {
		t.Fatalf("last entry %s: %v", last.Name, err)
	}
	b[off] ^= 1
	changed := filepath.Join(dir, "changed.oa.zip")
	if err := os.WriteFile(changed, b, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh", "deeper")
	if _, err := runPack(t, "restore", changed, fresh); exitCode(err) != 3 {
		t.Fatalf("restore of a changed archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh")); !os.IsNotExist(err) {
		t.Fatalf("a failed restore left the folders it made: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	if _, err := runPack(t, "restore", changed, empty); exitCode(err) != 3 {
		t.Fatalf("restore of a changed archive: %v", err)
	}
	if left, err := os.ReadDir(empty); err != nil || len(left) != 0 {
		t.Fatalf("a failed restore left %v in a folder it did not make: %v", left, err)
	}
}

// INF-MP9: an output is written under a temporary name and renamed at its
// end only if nothing stands at the target then; the file that appeared
// meanwhile keeps its bytes, and no temporary file stays.
func TestWriteNewReplacesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.zip")
	err := writeNew(target, func(w io.Writer) error {
		if err := os.WriteFile(target, []byte("theirs"), 0o600); err != nil {
			return err
		}
		_, err := w.Write([]byte("ours"))
		return err
	})
	if !errors.Is(err, fs.ErrExist) || exitCode(packOutcome("models pack", err)) != 3 {
		t.Fatalf("writeNew over a file that appeared: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "theirs" {
		t.Fatalf("the target holds %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the folder holds %v", entries)
	}
	if err := writeNew(filepath.Join(dir, "new.zip"), func(w io.Writer) error { _, err := w.Write([]byte("x")); return err }); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("the folder holds %v", entries)
	}
}

// INF-MP9: measure prints this machine's rates in the form --rates reads.
func TestModelsPackMeasure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "disk.bin")
	if err := os.WriteFile(file, make([]byte, 32<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPack(t, "measure", file, "--seconds", "0.4")
	if err != nil {
		t.Fatalf("measure: %v\n%s", err, out)
	}
	// A rate of 0 would make pack encode nothing; the measurement gives a
	// positive, finite rate for each, however slowly the file opens.
	r, err := parseRates(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("measure printed %q: %v", out, err)
	}
	for _, v := range []float64{r.BF16, r.F16, r.BF16Fixed3, r.Disk} {
		if !(v > 0) || math.IsInf(v, 0) {
			t.Fatalf("measure printed %q: %+v", out, r)
		}
	}
}

// INF-MP9: the built command, with default flags. A 1.2 MB LoRA packs
// without a disk measurement and every unit stored, and info, verify,
// restore and measure each run on it; a folder whose first weight file is
// small and whose second is over 16 MiB measures the disk with the larger.
func TestModelsPackBuiltCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "openabstractions")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build the command: %v\n%s", err, out)
	}
	run := func(args ...string) (string, int) {
		t.Helper()
		out, err := exec.Command(bin, append([]string{"models", "pack"}, args...)...).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(out), 0
	}
	lora := safetensors(stEntry{"lora_A.weight", "BF16", halfWeights(600_000, 7, false)})
	in := filepath.Join(dir, "adapter.safetensors")
	if err := os.WriteFile(in, lora, 0o600); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(dir, "adapter.oa.zip")
	out, code := run(in, pack)
	if code != 0 || !strings.Contains(out, "not measured") {
		t.Fatalf("pack a 1.2 MB file: exit %d\n%s", code, out)
	}
	archive, err := modelpack.ReadZip(pack)
	if err != nil || archive.Files[0].Entry != "adapter.safetensors" {
		t.Fatalf("the small file's archive: %+v, %v", archive.Files, err)
	}
	for _, args := range [][]string{{"info", pack}, {"verify", pack}, {"restore", pack, filepath.Join(dir, "back.safetensors")}} {
		if out, code := run(args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dir, "back.safetensors")); err != nil || !bytes.Equal(got, lora) {
		t.Fatalf("restored adapter: %v", err)
	}
	out, code = run("measure", in)
	if r, err := parseRates(strings.TrimSpace(out)); code != 0 || err != nil || !(r.Disk > 0) || math.IsInf(r.Disk, 0) {
		t.Fatalf("measure on a small file: exit %d, printed %q: %v", code, out, err)
	}

	folder := filepath.Join(dir, "model")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(folder, "model.safetensors")
	for name, b := range map[string][]byte{
		"adapter.safetensors": lora, // first in name order
		"model.safetensors":   safetensors(stEntry{"w", "BF16", halfWeights(9<<20, 8, false)}),
	} {
		if err := os.WriteFile(filepath.Join(folder, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, code := run(folder, filepath.Join(dir, "model.oa.zip")); code != 0 || !strings.Contains(out, "disk read from "+big) {
		t.Fatalf("pack a folder, small file first: exit %d\n%s", code, out)
	}
}

// INF-MP12: through the built command, a fine-tune packs as delta units
// against its base, given as a file or as an archive; info names the base;
// verify and restore need the base, refuse another, and restore the
// fine-tune byte for byte; --base packs one weight file only.
func TestModelsPackDeltaBuiltCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "openabstractions")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build the command: %v\n%s", err, out)
	}
	run := func(args ...string) (string, int) {
		t.Helper()
		out, err := exec.Command(bin, append([]string{"models", "pack"}, args...)...).CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return string(out), 0
	}
	w := halfWeights(700_000, 11, false)
	moved := bytes.Clone(w)
	r := rand.New(rand.NewPCG(12, 13))
	for i := 0; i < len(moved); i += 2 {
		v := binary.LittleEndian.Uint16(moved[i:])
		binary.LittleEndian.PutUint16(moved[i:], v+uint16(r.IntN(7)-3))
	}
	norm := make([]byte, 4096)
	baseFile := safetensors(stEntry{"w", "BF16", w}, stEntry{"norm", "F32", norm})
	tunedFile := safetensors(stEntry{"w", "BF16", moved}, stEntry{"norm", "F32", norm}, stEntry{"extra", "BF16", halfWeights(30_000, 14, false)})
	other := safetensors(stEntry{"w", "BF16", halfWeights(700_000, 15, false)}, stEntry{"norm", "F32", norm})
	paths := map[string][]byte{"base.safetensors": baseFile, "tuned.safetensors": tunedFile, "other.safetensors": other}
	for name, b := range paths {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base, tuned, otherPath := filepath.Join(dir, "base.safetensors"), filepath.Join(dir, "tuned.safetensors"), filepath.Join(dir, "other.safetensors")
	baseArchive := filepath.Join(dir, "base.oa.zip")
	if out, code := run(base, baseArchive); code != 0 {
		t.Fatalf("pack the base: exit %d\n%s", code, out)
	}
	baseSum := sha256.Sum256(baseFile)
	for i, given := range []string{base, baseArchive} {
		pack := filepath.Join(dir, fmt.Sprintf("tuned-%d.oa.zip", i))
		out, code := run(tuned, pack, "--base", given)
		if code != 0 || !strings.Contains(out, "exp-ans-v1") {
			t.Fatalf("pack against %s: exit %d\n%s", given, code, out)
		}
		archive, err := modelpack.ReadZip(pack)
		if err != nil {
			t.Fatalf("read the delta archive: %v", err)
		}
		if archive.Files[0].Index.Base == nil || archive.Files[0].Index.Base.Digest != "sha256:"+hexOf(baseSum[:]) {
			t.Fatalf("the delta archive's base: %+v, %v", archive.Files[0].Index.Base, err)
		}
		if out, code := run("info", pack); code != 0 || !strings.Contains(out, "delta against") {
			t.Fatalf("info: exit %d\n%s", code, out)
		}
		if out, code := run("verify", pack); code != 3 {
			t.Fatalf("verify without the base: exit %d\n%s", code, out)
		}
		if out, code := run("verify", pack, "--base", otherPath); code != 3 {
			t.Fatalf("verify against another base: exit %d\n%s", code, out)
		}
		if out, code := run("verify", pack, "--base", given); code != 0 || !strings.Contains(out, "verified") {
			t.Fatalf("verify against the base: exit %d\n%s", code, out)
		}
		back := filepath.Join(dir, fmt.Sprintf("back-%d.safetensors", i))
		if out, code := run("restore", pack, back, "--base", given); code != 0 {
			t.Fatalf("restore: exit %d\n%s", code, out)
		}
		if got, err := os.ReadFile(back); err != nil || !bytes.Equal(got, tunedFile) {
			t.Fatalf("restored fine-tune differs: %v", err)
		}
	}
	if out, code := run(dir, filepath.Join(t.TempDir(), "folder.oa.zip"), "--base", base); code != 2 {
		t.Fatalf("--base with a folder: exit %d\n%s", code, out)
	}
}

// INF-MP9: an empty or invalid weight file refuses the run before any
// measurement and before the line that says none is taken.
func TestModelsPackReadsHeadersBeforeMeasuring(t *testing.T) {
	dir := t.TempDir()
	for name, b := range map[string][]byte{"empty.safetensors": nil, "junk.gguf": []byte("not a gguf file")} {
		in := filepath.Join(dir, name)
		if err := os.WriteFile(in, b, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := runPack(t, in, filepath.Join(dir, name+".zip"))
		if exitCode(err) != 3 || strings.Contains(out, "measured") {
			t.Fatalf("%s: exit %d\n%s", name, exitCode(err), out)
		}
	}
}

// INF-MP9: weight files of 64 MiB or more, none of one 16 MiB block, are
// measured: the largest is read whole once for the disk rate.
func TestModelsPackMeasuresManySmallFiles(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "model")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	shard := safetensors(stEntry{"w", "BF16", halfWeights(6<<20+1000, 9, false)}) // just over 12 MiB
	for i := range 6 {
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("shard-%d.safetensors", i)), shard, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPack(t, folder, filepath.Join(dir, "model.oa.zip"), "--seconds", "0.4")
	if err != nil || !strings.Contains(out, "measured: ") || strings.Contains(out, "not measured") {
		t.Fatalf("six 12 MiB shards: %v\n%s", err, out)
	}
}

// INF-MP9: help is the default; mistakes are usage errors.
func TestModelsPackHelpAndMistakes(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"info", "--help"}} {
		out, err := runPack(t, args...)
		if err != nil || !strings.HasPrefix(out, "Usage: openabstractions models pack") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	for _, args := range [][]string{{"only-one"}, {"info"}, {"restore", "a.zip"}, {"a", "b", "--rates", "bf16=1"}, {"a", "b", "--seconds", "0"}} {
		if _, err := runPack(t, args...); exitCode(err) != exitUsage {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
	r, err := parseRates("bf16=10.5,f16=6,bf16fixed3=20,disk=6.5")
	if err != nil || r.BF16 != 10.5e9 || r.Disk != 6.5e9 {
		t.Fatalf("parseRates: %+v %v", r, err)
	}
	if again, err := parseRates(formatRates(r)); err != nil || again != r {
		t.Fatalf("formatRates does not read back: %+v %v", again, err)
	}
}
