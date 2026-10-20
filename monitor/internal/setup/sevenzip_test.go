package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func sevenZipTestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type sevenZipFixture struct {
	in       sevenZipArchive
	contents map[string]string
	parent   string
	calls    int
	listing  string
}

func newSevenZipFixture(t *testing.T) *sevenZipFixture {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "private")
	if err := privatedir.Make(parent); err != nil {
		t.Fatal(err)
	}
	parts := filepath.Join(parent, "parts")
	if err := privatedir.Make(parts); err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) sevenZipPin {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return sevenZipPin{Path: path, Bytes: int64(len(data)), SHA256: sevenZipTestHash([]byte(data))}
	}
	f := &sevenZipFixture{parent: parent, contents: map[string]string{"manifest.json": "{}\n", "data/payload.bin": "authenticated payload"}}
	f.in = sevenZipArchive{Tool: write(filepath.Join(parent, "inert-tool.exe"), "not an executable; never launched"), Parts: []sevenZipPin{write(filepath.Join(parts, "bundle.7z.001"), "one"), write(filepath.Join(parts, "bundle.7z.002"), "two")}, ArchiveBytes: 6, ArchiveSHA256: sevenZipTestHash([]byte("onetwo")), Destination: filepath.Join(parent, "destination"), MaxExpanded: 1024, MaxMembers: 8, Timeout: time.Minute}
	for _, name := range []string{"manifest.json", "data/payload.bin"} {
		data := f.contents[name]
		f.in.Files = append(f.in.Files, archiveFileRecord{Path: name, Size: int64(len(data)), SHA256: sevenZipTestHash([]byte(data))})
	}
	f.listing = "7-Zip fixture\n----------\n" + sevenZipTestEntry("manifest.json", int64(len(f.contents["manifest.json"])), false) + "\n" + sevenZipTestEntry("data", 0, true) + "\n" + sevenZipTestEntry("data/payload.bin", int64(len(f.contents["data/payload.bin"])), false)
	return f
}

func sevenZipTestEntry(name string, size int64, dir bool) string {
	folder := "-"
	if dir {
		folder = "+"
	}
	return fmt.Sprintf("Path = %s\nSize = %d\nFolder = %s\nEncrypted = -\n", name, size, folder)
}

func (f *sevenZipFixture) command(ctx context.Context, tool string, args []string, work string, limit int64) ([]byte, error) {
	f.calls++
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing bounded command context")
	}
	if filepath.Base(tool) != "inert-tool.exe" || limit != sevenZipOutputLimit {
		return nil, errors.New("wrong selected tool or output bound")
	}
	switch f.calls {
	case 1:
		if len(args) != 4 || strings.Join(args[:3], " ") != "l -slt -sccUTF-8" {
			return nil, errors.New("wrong fixed listing argv")
		}
		return []byte(f.listing), nil
	case 2:
		if len(args) != 4 || args[0] != "x" || args[1] != "-so" || args[3] != "manifest.json" {
			return nil, errors.New("wrong fixed manifest argv")
		}
		return []byte(f.contents["manifest.json"]), nil
	case 3:
		if len(args) != 4 || args[0] != "x" || args[1] != "-y" || !strings.HasPrefix(args[2], "-o") {
			return nil, errors.New("wrong fixed extraction argv")
		}
		output := strings.TrimPrefix(args[2], "-o")
		if filepath.Dir(output) != work {
			return nil, errors.New("output escaped owned work")
		}
		for name, data := range f.contents {
			path := filepath.Join(output, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				return nil, err
			}
		}
		return nil, nil
	default:
		return nil, errors.New("unexpected command")
	}
}

func (f *sevenZipFixture) noWork(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".archive-") {
			t.Fatal("owned temporary work retained", entry.Name())
		}
	}
}

func TestSevenZipVerifiesRawInventoryWithoutCreatingReceipt(t *testing.T) {
	f := newSevenZipFixture(t)
	if err := extractSevenZipWith(t.Context(), f.in, f.command); err != nil {
		t.Fatal(err)
	}
	if f.calls != 3 {
		t.Fatal(f.calls)
	}
	for name, data := range f.contents {
		raw, err := os.ReadFile(filepath.Join(f.in.Destination, filepath.FromSlash(name)))
		if err != nil || string(raw) != data {
			t.Fatal(name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.in.Destination, archiveMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("extraction created an install receipt", err)
	}
	f.noWork(t)
}

func TestSevenZipSinglePartUsesTheSameVerifiedBoundary(t *testing.T) {
	f := newSevenZipFixture(t)
	if err := os.Remove(f.in.Parts[1].Path); err != nil {
		t.Fatal(err)
	}
	old := f.in.Parts[0].Path
	name := strings.TrimSuffix(old, ".001")
	if err := os.Rename(old, name); err != nil {
		t.Fatal(err)
	}
	f.in.Parts = f.in.Parts[:1]
	f.in.Parts[0].Path = name
	f.in.ArchiveBytes = 3
	f.in.ArchiveSHA256 = sevenZipTestHash([]byte("one"))
	if err := extractSevenZipWith(t.Context(), f.in, f.command); err != nil {
		t.Fatal(err)
	}
	f.noWork(t)
}

func TestSevenZipWindowsHoldsInputsThroughoutTheCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("deny-write/delete hold control is Windows-specific")
	}
	f := newSevenZipFixture(t)
	command := func(ctx context.Context, tool string, args []string, work string, limit int64) ([]byte, error) {
		for _, name := range []string{f.in.Tool.Path, f.in.Parts[0].Path} {
			file, err := os.OpenFile(name, os.O_WRONLY, 0600)
			if err == nil {
				file.Close()
				return nil, errors.New("held input admitted a writer")
			}
		}
		parent := filepath.Dir(f.in.Parts[0].Path)
		if err := os.Rename(parent, parent+"-moved"); err == nil {
			_ = os.Rename(parent+"-moved", parent)
			return nil, errors.New("held input ancestor admitted rename")
		}
		return f.command(ctx, tool, args, work, limit)
	}
	if err := extractSevenZipWith(t.Context(), f.in, command); err != nil {
		t.Fatal(err)
	}
	f.noWork(t)
}

func TestSevenZipListingRefusesUnsafeOrAmbiguousMembers(t *testing.T) {
	f := newSevenZipFixture(t)
	wanted, dirs, err := sevenZipInventory(f.in)
	if err != nil {
		t.Fatal(err)
	}
	base := f.listing
	cases := map[string]string{
		"missing-separator":   "Path = manifest.json\nSize = 3\n",
		"duplicate-field":     strings.Replace(base, "Size = 3", "Size = 3\nSize = 3", 1),
		"duplicate-member":    base + "\n" + sevenZipTestEntry("manifest.json", 3, false),
		"case-alias":          base + "\n" + sevenZipTestEntry("MANIFEST.JSON", 3, false),
		"traversal":           strings.Replace(base, "data/payload.bin", "../payload.bin", 1),
		"backslash-traversal": strings.Replace(base, "data/payload.bin", `data\..\payload.bin`, 1),
		"absolute":            strings.Replace(base, "data/payload.bin", "/payload.bin", 1),
		"ads":                 strings.Replace(base, "data/payload.bin", "data/payload.bin:stream", 1),
		"reserved":            strings.Replace(base, "data/payload.bin", "data/NUL", 1),
		"trailing-dot":        strings.Replace(base, "data/payload.bin", "data/payload.bin.", 1),
		"encrypted":           strings.Replace(base, "Encrypted = -", "Encrypted = +", 1),
		"symbolic-link":       base + "Symbolic Link = elsewhere\n",
		"hard-link":           base + "Hard Link = elsewhere\n",
		"unknown-field":       base + "Alternate Stream = +\n",
		"malformed-line":      base + "not a field\n",
		"negative-size":       strings.Replace(base, "Size = 3", "Size = -3", 1),
		"overflow-size":       strings.Replace(base, "Size = 3", "Size = 9223372036854775808", 1),
		"noncanonical-size":   strings.Replace(base, "Size = 3", "Size = 03", 1),
		"wrong-size":          strings.Replace(base, "Size = 3", "Size = 4", 1),
		"foreign-directory":   base + "\n" + sevenZipTestEntry("foreign", 0, true),
		"missing-file":        strings.Split(base, "\n\n")[0] + "\n",
		"invalid-utf8":        base + string([]byte{0xff}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := sevenZipListing([]byte(raw), wanted, dirs, f.in.MaxExpanded, f.in.MaxMembers); err == nil {
				t.Fatal("unsafe listing accepted")
			}
		})
	}
	if err := sevenZipListing([]byte(base), wanted, dirs, 1, f.in.MaxMembers); err == nil {
		t.Fatal("expanded bound ignored")
	}
	if err := sevenZipListing([]byte(base), wanted, dirs, f.in.MaxExpanded, 1); err == nil {
		t.Fatal("member bound ignored")
	}
}

func TestSevenZipInputRefusalsPrecedeCommandsAndPreserveForeignMaterial(t *testing.T) {
	for _, fault := range []string{"tool-hash", "part-hash", "logical-hash", "logical-bytes", "part-order", "extra-volume", "input-hardlink", "existing-target", "duplicate-inventory", "file-directory-collision", "receipt-member", "excess-members", "excess-expanded"} {
		t.Run(fault, func(t *testing.T) {
			f := newSevenZipFixture(t)
			switch fault {
			case "tool-hash":
				f.in.Tool.SHA256 = strings.Repeat("a", 64)
			case "part-hash":
				f.in.Parts[1].SHA256 = strings.Repeat("a", 64)
			case "logical-hash":
				f.in.ArchiveSHA256 = strings.Repeat("a", 64)
			case "logical-bytes":
				f.in.ArchiveBytes--
			case "part-order":
				f.in.Parts[0], f.in.Parts[1] = f.in.Parts[1], f.in.Parts[0]
			case "extra-volume":
				if err := os.WriteFile(filepath.Join(filepath.Dir(f.in.Parts[0].Path), "bundle.7z.003"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "input-hardlink":
				if err := os.Link(f.in.Tool.Path, filepath.Join(f.parent, "tool-alias")); err != nil {
					t.Fatal(err)
				}
			case "existing-target":
				if err := os.Mkdir(f.in.Destination, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.in.Destination, "foreign"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate-inventory":
				pin := f.in.Files[0]
				f.in.Files = append(f.in.Files, pin)
			case "file-directory-collision":
				pin := f.in.Files[0]
				pin.Path = "data"
				f.in.Files = append(f.in.Files, pin)
			case "receipt-member":
				pin := f.in.Files[0]
				pin.Path = archiveMarker
				f.in.Files = append(f.in.Files, pin)
			case "excess-members":
				f.in.MaxMembers = 1
			case "excess-expanded":
				f.in.MaxExpanded = 1
			}
			if err := extractSevenZipWith(t.Context(), f.in, f.command); err == nil {
				t.Fatal("invalid input accepted")
			}
			if f.calls != 0 {
				t.Fatal("command ran before admission", f.calls)
			}
			if fault == "existing-target" {
				raw, err := os.ReadFile(filepath.Join(f.in.Destination, "foreign"))
				if err != nil || string(raw) != "preserve" {
					t.Fatal("foreign target changed", err)
				}
			} else if _, err := os.Stat(f.in.Destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("target published", err)
			}
			if fault == "extra-volume" {
				raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.in.Parts[0].Path), "bundle.7z.003"))
				if err != nil || string(raw) != "foreign" {
					t.Fatal("foreign volume changed", err)
				}
			}
			f.noWork(t)
		})
	}
}

func TestSevenZipWindowsNativeCaseAliasRefusesBeforeCommands(t *testing.T) {
	f := newSevenZipFixture(t)
	lower := filepath.Join(f.parent, "manifest.json")
	if err := os.WriteFile(lower, []byte("inert alias observation"), 0600); err != nil {
		t.Fatal(err)
	}
	same, err := fileid.SameFile(lower, filepath.Join(f.parent, "MANIFEST.JSON"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("this filesystem keeps the two case names distinct")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Skip("these names identify distinct files")
	}
	// The selected native archive is offered only on Windows. Its case-alias
	// proof uses the existing pending-name owner. That owner's documented
	// Darwin/APFS limit remains PT-5c; this test does not claim to repair it.
	if foldName("manifest.json") != foldName("MANIFEST.JSON") {
		t.Skip("pending-name owner has no case-alias contract here (identity PT-5c)")
	}
	pin := f.in.Files[0]
	pin.Path = "MANIFEST.JSON"
	f.in.Files = append(f.in.Files, pin)
	if err := extractSevenZipWith(t.Context(), f.in, f.command); err == nil {
		t.Fatal("case alias accepted")
	}
	if f.calls != 0 {
		t.Fatal("command ran before case-alias admission", f.calls)
	}
	f.noWork(t)
}

func TestSevenZipManifestAndCommandBoundsFailBeforeExtraction(t *testing.T) {
	for _, fault := range []string{"manifest", "output", "command-error", "cancel", "timeout"} {
		t.Run(fault, func(t *testing.T) {
			f := newSevenZipFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if fault == "timeout" {
				f.in.Timeout = time.Second
			}
			command := func(ctx context.Context, tool string, args []string, work string, limit int64) ([]byte, error) {
				if fault == "output" {
					f.calls++
					return make([]byte, sevenZipOutputLimit+1), nil
				}
				if fault == "command-error" {
					f.calls++
					return nil, errors.New("inert command refused")
				}
				if fault == "cancel" {
					f.calls++
					cancel()
					return nil, ctx.Err()
				}
				if fault == "timeout" {
					f.calls++
					<-ctx.Done()
					return nil, ctx.Err()
				}
				out, err := f.command(ctx, tool, args, work, limit)
				if f.calls == 2 {
					return []byte("bad"), nil
				}
				return out, err
			}
			if err := extractSevenZipWith(ctx, f.in, command); err == nil {
				t.Fatal("bounded refusal accepted")
			}
			if f.calls > 2 {
				t.Fatal("extraction ran", f.calls)
			}
			if _, err := os.Stat(f.in.Destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("target published", err)
			}
			f.noWork(t)
		})
	}
}

func TestSevenZipExtractedClosureAndForeignTargetRaceFailClosed(t *testing.T) {
	for _, fault := range []string{"extra", "changed", "missing", "hardlink", "foreign-target"} {
		t.Run(fault, func(t *testing.T) {
			f := newSevenZipFixture(t)
			foreign := filepath.Join(f.parent, "foreign-file")
			if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			command := func(ctx context.Context, tool string, args []string, work string, limit int64) ([]byte, error) {
				out, err := f.command(ctx, tool, args, work, limit)
				if err != nil || f.calls != 3 {
					return out, err
				}
				root := strings.TrimPrefix(args[2], "-o")
				payload := filepath.Join(root, "data/payload.bin")
				switch fault {
				case "extra":
					err = os.WriteFile(filepath.Join(root, "extra"), []byte("foreign"), 0600)
				case "changed":
					err = os.WriteFile(payload, []byte("changed"), 0600)
				case "missing":
					err = os.Remove(payload)
				case "hardlink":
					if err = os.Remove(payload); err == nil {
						err = os.Link(foreign, payload)
					}
				case "foreign-target":
					if err = os.Mkdir(f.in.Destination, 0700); err == nil {
						err = os.WriteFile(filepath.Join(f.in.Destination, "foreign"), []byte("keep"), 0600)
					}
				}
				return out, err
			}
			if err := extractSevenZipWith(t.Context(), f.in, command); err == nil {
				t.Fatal("extracted mismatch accepted")
			}
			raw, err := os.ReadFile(foreign)
			if err != nil || string(raw) != "preserve" {
				t.Fatal("outside material changed", err)
			}
			if fault == "foreign-target" {
				raw, err := os.ReadFile(filepath.Join(f.in.Destination, "foreign"))
				if err != nil || string(raw) != "keep" {
					t.Fatal("foreign target changed", err)
				}
			} else if _, err := os.Stat(f.in.Destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("target published", err)
			}
			f.noWork(t)
		})
	}
}
