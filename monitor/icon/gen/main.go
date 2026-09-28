// Command gen builds the Panel's Windows icon and resource script from the
// brand mark, and — on Windows, with -syso — compiles the linkable resource
// objects go build picks up on its own.
//
// Default run (any OS, standard library only):
//
//	go run ./monitor/icon/gen
//
// reads openabstractions-flat/openabstractions.github.io/brand/mark-*.png,
// copies the sizes the brand ships as-is and derives the rest by box-average
// downscaling, writes them beside this program's package
// (monitor/icon/mark-<n>.png), packs them into monitor/icon/panel.ico, and
// writes monitor/panel.rc referencing that ICO, monitor/panel.manifest and a
// VERSIONINFO block.
//
// Syso run (Windows only, needs the Visual Studio or Windows SDK build
// tools — see monitor/README.md):
//
//	go run ./monitor/icon/gen -syso
//
// compiles monitor/panel.rc with rc.exe and converts the result with
// cvtres.exe into monitor/rsrc_windows_amd64.syso and
// monitor/rsrc_windows_arm64.syso, which `go build` links into the Panel
// automatically because of their names — no further wiring needed.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

// panelVersion is the Panel's reported version. Nothing in monitor exposes
// one today (checked in monitor/main.go and its flags), so this generator
// carries the fallback the task that added it settled on; update both here
// and wherever monitor grows a real version source.
const panelVersion = "0.3.0"

// iconSizes are the ICO's entries, in the brand's own preferred order
// (small to large). sourceSize names the brand mark PNG each is built from;
// a size equal to its source is a byte-for-byte copy, everything else is a
// downscale of that source.
var iconSizes = []struct{ size, sourceSize int }{
	{16, 16},
	{24, 32},
	{32, 32},
	{48, 64},
	{64, 64},
	{128, 256},
	{256, 256},
}

func main() {
	syso := flag.Bool("syso", false, "compile monitor/rsrc_windows_amd64.syso and monitor/rsrc_windows_arm64.syso from monitor/panel.rc via rc.exe and cvtres.exe (Windows only; run the default generation first)")
	vcvars := flag.String("vcvars", "", "absolute path to vcvars64.bat; auto-detected under Program Files when omitted")
	flag.Parse()

	iconDir, monitorDir, err := layout()
	if err != nil {
		fail(err)
	}

	if *syso {
		if err := buildSyso(monitorDir, *vcvars); err != nil {
			fail(err)
		}
		return
	}

	brandDir := filepath.Join(filepath.Dir(monitorDir), "openabstractions-flat", "openabstractions.github.io", "brand")
	if err := generateAssets(brandDir, iconDir, monitorDir); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err) //unchecked: the error is already being reported; nothing is left to say if stderr is gone
	os.Exit(1)
}

// layout finds monitor/icon (this program's parent package directory) and
// monitor (its parent) from this source file's own path, so the tool works
// the same way whether it is launched by `go generate` (cwd is monitor/icon,
// the directive's own file's directory) or run directly from anywhere else.
func layout() (iconDir, monitorDir string, err error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", fmt.Errorf("could not locate gen/main.go's own path")
	}
	genDir := filepath.Dir(thisFile)
	iconDir = filepath.Dir(genDir)
	monitorDir = filepath.Dir(iconDir)
	if filepath.Base(iconDir) != "icon" || filepath.Base(monitorDir) != "monitor" {
		return "", "", fmt.Errorf("unexpected layout: expected .../monitor/icon/gen, found %s", genDir)
	}
	return iconDir, monitorDir, nil
}

// generateAssets copies and resamples the brand mark into iconDir, packs
// panel.ico there, and writes monitorDir/panel.rc.
func generateAssets(brandDir, iconDir, monitorDir string) error {
	bySource := map[int]image.Image{}
	sourcePaths := map[int]string{}
	for _, e := range iconSizes {
		sourcePaths[e.sourceSize] = filepath.Join(brandDir, fmt.Sprintf("mark-%d.png", e.sourceSize))
	}
	for size, path := range sourcePaths {
		img, err := loadPNG(path)
		if err != nil {
			return fmt.Errorf("load brand mark: %w", err)
		}
		b := img.Bounds()
		if b.Dx() != size || b.Dy() != size {
			return fmt.Errorf("%s: is %dx%d, want %dx%d", path, b.Dx(), b.Dy(), size, size)
		}
		bySource[size] = img
	}

	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return err
	}

	var entries []builtIcon
	for _, e := range iconSizes {
		out := filepath.Join(iconDir, fmt.Sprintf("mark-%d.png", e.size))
		var data []byte
		var err error
		if e.size == e.sourceSize {
			data, err = os.ReadFile(sourcePaths[e.sourceSize])
			if err != nil {
				return err
			}
		} else {
			resized := boxDownscale(bySource[e.sourceSize], e.size, e.size)
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestCompression}
			if err := enc.Encode(&buf, resized); err != nil {
				return fmt.Errorf("encode mark-%d.png: %w", e.size, err)
			}
			data = buf.Bytes()
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		entries = append(entries, builtIcon{e.size, data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })

	icoPath := filepath.Join(iconDir, "panel.ico")
	if err := writeICO(icoPath, entries); err != nil {
		return fmt.Errorf("write panel.ico: %w", err)
	}
	fmt.Printf("wrote %s (%d entries)\n", icoPath, len(entries)) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced

	rcPath := filepath.Join(monitorDir, "panel.rc")
	if err := writeRC(rcPath); err != nil {
		return fmt.Errorf("write panel.rc: %w", err)
	}
	fmt.Println("wrote", rcPath) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced
	return nil
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// boxDownscale resamples src to exactly w by h by averaging, in straight
// (non-premultiplied) alpha space, every source pixel each destination
// pixel covers. It is a plain area-average filter — the standard library's
// image/draw does not resample at all, only compositing pixel-for-pixel —
// good enough for shrinking a flat-color brand mark by the small factors an
// icon needs (32 to 24, 64 to 48, 256 to 128).
func boxDownscale(src image.Image, w, h int) *image.NRGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy0 := y * sh / h
		sy1 := (y + 1) * sh / h
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < w; x++ {
			sx0 := x * sw / w
			sx1 := (x + 1) * sw / w
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, n uint64
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					c := color.NRGBAModel.Convert(src.At(sb.Min.X+xx, sb.Min.Y+yy)).(color.NRGBA)
					r += uint64(c.R)
					g += uint64(c.G)
					b += uint64(c.B)
					a += uint64(c.A)
					n++
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / n), G: uint8(g / n), B: uint8(b / n), A: uint8(a / n),
			})
		}
	}
	return dst
}

// builtIcon is one generated ICO entry: its pixel size and the PNG bytes
// that go with it, already written to monitor/icon/mark-<size>.png.
type builtIcon struct {
	size int
	png  []byte
}

// writeICO packs entries (smallest first) into an ICO container: the
// ICONDIR header, one ICONDIRENTRY per image, then the image payloads in
// the same order. Every entry carries its PNG bytes directly — the format
// Windows Vista and later read for any ICO entry — rather than an
// uncompressed BMP, so this needs no BMP encoder of its own.
func writeICO(path string, entries []builtIcon) error {
	var buf bytes.Buffer

	// ICONDIR: reserved(2)=0, type(2)=1 (icon), count(2).
	binary.Write(&buf, binary.LittleEndian, uint16(0))            //unchecked: a write into a bytes.Buffer cannot fail
	binary.Write(&buf, binary.LittleEndian, uint16(1))            //unchecked: a write into a bytes.Buffer cannot fail
	binary.Write(&buf, binary.LittleEndian, uint16(len(entries))) //unchecked: a write into a bytes.Buffer cannot fail

	headerSize := 6 + 16*len(entries)
	offset := uint32(headerSize)
	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // ICONDIRENTRY spells 256 as 0 in the one-byte width/height fields.
		}
		buf.WriteByte(dim)                                          //unchecked: a write into a bytes.Buffer cannot fail (width)
		buf.WriteByte(dim)                                          //unchecked: a write into a bytes.Buffer cannot fail (height)
		buf.WriteByte(0)                                            //unchecked: a write into a bytes.Buffer cannot fail (color count (0: not palettized))
		buf.WriteByte(0)                                            //unchecked: a write into a bytes.Buffer cannot fail (reserved)
		binary.Write(&buf, binary.LittleEndian, uint16(1))          //unchecked: a write into a bytes.Buffer cannot fail (planes)
		binary.Write(&buf, binary.LittleEndian, uint16(32))         //unchecked: a write into a bytes.Buffer cannot fail (bit count)
		binary.Write(&buf, binary.LittleEndian, uint32(len(e.png))) //unchecked: a write into a bytes.Buffer cannot fail
		binary.Write(&buf, binary.LittleEndian, offset)             //unchecked: a write into a bytes.Buffer cannot fail
		offset += uint32(len(e.png))
	}
	for _, e := range entries {
		buf.Write(e.png) //unchecked: a write into a bytes.Buffer cannot fail
	}

	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// writeRC writes monitor/panel.rc: the app icon at resource id 1, the
// manifest beside it at RT_MANIFEST (type 24) id 1 — CREATEPROCESS_MANIFEST_
// RESOURCE_ID, the id Windows reads an EXE's own manifest from — and a
// VERSIONINFO block. rc.exe understands ICON, RT_MANIFEST's numeric type and
// every VERSIONINFO keyword used here natively, so the file needs no
// #include and no Windows SDK headers to compile.
func writeRC(path string) error {
	const rc = `// Generated by ` + "`go generate ./monitor/icon/...`" + ` (monitor/icon/gen/main.go).
// Edit the brand mark or this generator, not this file, by hand.

1 ICON "icon/panel.ico"
1 24 "panel.manifest"

1 VERSIONINFO
FILEVERSION     0,3,0,0
PRODUCTVERSION  0,3,0,0
FILEFLAGSMASK   0x3fL
FILEFLAGS       0x0L
FILEOS          0x40004L
FILETYPE        0x1L
FILESUBTYPE     0x0L
BEGIN
    BLOCK "StringFileInfo"
    BEGIN
        BLOCK "040904b0"
        BEGIN
            VALUE "CompanyName",      "OpenAbstractions"
            VALUE "FileDescription",  "Open Abstractions control panel"
            VALUE "FileVersion",      "` + panelVersion + `.0"
            VALUE "InternalName",     "panel"
            VALUE "LegalCopyright",   "OpenAbstractions"
            VALUE "OriginalFilename", "Abstraction Panel.exe"
            VALUE "ProductName",      "Abstraction Panel"
            VALUE "ProductVersion",   "` + panelVersion + `.0"
        END
    END
    BLOCK "VarFileInfo"
    BEGIN
        VALUE "Translation", 0x409, 1200
    END
END
`
	return os.WriteFile(path, []byte(rc), 0o644)
}

// buildSyso compiles monitor/panel.rc into monitor/rsrc_windows_amd64.syso
// and monitor/rsrc_windows_arm64.syso. It writes one .bat and runs it by
// absolute path (the Windows C++ recipe this repository already follows for
// vcvars64: `cd` in the calling process does not carry into a child cmd.exe,
// and the .bat must be invoked by its full path or cmd reports it as not
// recognized), because rc.exe and cvtres.exe are not on PATH until
// vcvars64.bat has run.
//
// One rc.exe compile of panel.rc yields a single, architecture-neutral
// panel.res (resource data carries no machine type); cvtres.exe's own
// /MACHINE flag is what tags the two .syso files for amd64 and arm64, so
// this needs no ARM64 cross build tools — only cvtres.exe's /MACHINE:ARM64,
// which an x64-hosted cvtres.exe already supports.
func buildSyso(monitorDir, vcvarsOverride string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("-syso needs rc.exe and cvtres.exe; run it on Windows")
	}

	vcvars := vcvarsOverride
	if vcvars == "" {
		found, err := findVCVars64()
		if err != nil {
			return err
		}
		vcvars = found
	}
	if _, err := os.Stat(vcvars); err != nil {
		return fmt.Errorf("vcvars64.bat: %w", err)
	}

	rcPath := filepath.Join(monitorDir, "panel.rc")
	if _, err := os.Stat(rcPath); err != nil {
		return fmt.Errorf("%s: run the default `go run ./monitor/icon/gen` first: %w", rcPath, err)
	}
	resPath := filepath.Join(monitorDir, "panel.res")
	amd64Out := filepath.Join(monitorDir, "rsrc_windows_amd64.syso")
	arm64Out := filepath.Join(monitorDir, "rsrc_windows_arm64.syso")

	bat := fmt.Sprintf(`@echo off
setlocal
call "%s" >nul || exit /b 1
cd /d "%s" || exit /b 1
rc.exe /nologo /fo panel.res panel.rc || exit /b 1
cvtres.exe /NOLOGO /MACHINE:X64 /OUT:rsrc_windows_amd64.syso panel.res || exit /b 1
cvtres.exe /NOLOGO /MACHINE:ARM64 /OUT:rsrc_windows_arm64.syso panel.res || exit /b 1
`, vcvars, monitorDir)

	batPath := filepath.Join(os.TempDir(), "oa-panel-syso.bat")
	if err := os.WriteFile(batPath, []byte(bat), 0o644); err != nil {
		return err
	}
	defer os.Remove(batPath)
	defer os.Remove(resPath)

	cmd := exec.Command("cmd.exe", "/c", batPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rc.exe/cvtres.exe: %w\n%s", err, out)
	}
	fmt.Print(string(out)) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced

	for _, syso := range []string{amd64Out, arm64Out} {
		removed, err := stripAbsoluteSymbol(syso, "@comp.id")
		if err != nil {
			return fmt.Errorf("%s: %w", syso, err)
		}
		if err := stripCvtresDebugSection(syso); err != nil {
			return fmt.Errorf("%s: %w", syso, err)
		}
		if removed {
			fmt.Printf("wrote %s (stripped @comp.id and path-bearing debug section)\n", syso) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced
		} else {
			fmt.Printf("wrote %s (stripped path-bearing debug section)\n", syso) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced
		}
	}
	return nil
}

// stripCvtresDebugSection removes the compiler's CodeView debug payload.
// cvtres records absolute input, output and working-directory paths there;
// those paths are unrelated to the icon, manifest and version resources.
// Keep the COFF section entry, but mark it empty and clear its old bytes so
// no machine-local path remains anywhere in the committed object. Normalize
// the COFF timestamp too, making repeated generation byte-for-byte stable.
func stripCvtresDebugSection(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 20 {
		return fmt.Errorf("too small to be a COFF object (%d bytes)", len(data))
	}
	sections := int(binary.LittleEndian.Uint16(data[2:4]))
	if len(data) < 20+sections*40 {
		return fmt.Errorf("COFF section table runs past end of file")
	}
	found := false
	for s := 0; s < sections; s++ {
		header := data[20+s*40 : 20+(s+1)*40]
		if !bytes.Equal(header[:8], []byte(".debug$S")) {
			continue
		}
		found = true
		size := int(binary.LittleEndian.Uint32(header[16:20]))
		off := int(binary.LittleEndian.Uint32(header[20:24]))
		if off < 20+sections*40 || off > len(data) || size > len(data)-off {
			return fmt.Errorf("COFF debug section runs past end of file")
		}
		clear(data[off : off+size])
		binary.LittleEndian.PutUint32(header[16:20], 0)
		binary.LittleEndian.PutUint32(header[20:24], 0)
	}
	if !found {
		return fmt.Errorf("cvtres object lacks expected .debug$S section")
	}
	binary.LittleEndian.PutUint32(data[4:8], 0)
	return os.WriteFile(path, data, 0o644)
}

// stripAbsoluteSymbol removes one absolute (no-section) COFF symbol by name
// from a .syso cvtres.exe wrote, fixing up every relocation that indexes
// into the symbol table to match, and reports whether it found and removed
// one.
//
// This machine's cvtres.exe (Visual Studio 18) stamps every object it
// writes with an absolute "@comp.id" symbol recording its own tool
// identity. Go's linker (cmd/link/internal/loadpe.readpesyms) special-cases
// exactly one absolute symbol, "@feat.00", and refuses to load an object
// carrying any other with "sectnum < 0!" — confirmed against this Go
// toolchain (go1.26.5) by linking a minimal object cvtres produced, which
// failed the same way until this function's output was substituted in. The
// symbol carries no information the resulting binary needs, so removing it
// is safe; a cvtres that does not emit it (an older toolset) leaves this a
// no-op, which is why buildSyso runs it unconditionally rather than
// gating on a version check.
func stripAbsoluteSymbol(path, name string) (removed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	const fileHeaderSize = 20
	const sectionHeaderSize = 40
	const symbolSize = 18
	if len(data) < fileHeaderSize {
		return false, fmt.Errorf("too small to be a COFF object (%d bytes)", len(data))
	}

	numSections := int(binary.LittleEndian.Uint16(data[2:4]))
	symTabOff := int(binary.LittleEndian.Uint32(data[8:12]))
	numSymbols := int(binary.LittleEndian.Uint32(data[12:16]))
	if symTabOff == 0 || numSymbols == 0 {
		return false, nil // no symbol table to rewrite
	}
	if symTabOff+numSymbols*symbolSize > len(data) {
		return false, fmt.Errorf("symbol table (offset %d, %d symbols) runs past end of file (%d bytes)", symTabOff, numSymbols, len(data))
	}
	if len(name) > 8 {
		return false, fmt.Errorf("symbol name %q longer than the 8 bytes a short COFF name holds", name)
	}
	var want [8]byte // COFF's fixed short-name field: the name, NUL-padded.
	copy(want[:], name)

	// oldToNew maps every flat symbol-table row (primary symbols and the
	// aux rows attached to some of them alike, indexed the same way COFF
	// relocations reference them) to its index after removal, or -1 for a
	// row this function drops.
	oldToNew := make([]int, numSymbols)
	kept := make([]byte, 0, numSymbols*symbolSize)
	newCount := 0
	for i := 0; i < numSymbols; {
		off := symTabOff + i*symbolSize
		aux := int(data[off+17])
		span := 1 + aux
		if i+span > numSymbols {
			return false, fmt.Errorf("symbol %d declares %d aux symbols past the table end", i, aux)
		}
		if bytes.Equal(data[off:off+8], want[:]) {
			for k := 0; k < span; k++ {
				oldToNew[i+k] = -1
			}
			removed = true
			i += span
			continue
		}
		for k := 0; k < span; k++ {
			oldToNew[i+k] = newCount
			newCount++
		}
		kept = append(kept, data[off:off+span*symbolSize]...)
		i += span
	}
	if !removed {
		return false, nil
	}

	out := append([]byte(nil), data[:symTabOff]...)
	for s := 0; s < numSections; s++ {
		base := fileHeaderSize + s*sectionHeaderSize
		if base+sectionHeaderSize > len(out) {
			return false, fmt.Errorf("section header %d runs past the symbol table start", s)
		}
		relOff := int(binary.LittleEndian.Uint32(out[base+24 : base+28]))
		relCount := int(binary.LittleEndian.Uint16(out[base+32 : base+34]))
		for r := 0; r < relCount; r++ {
			recOff := relOff + r*10
			if recOff+10 > len(out) {
				return false, fmt.Errorf("relocation %d of section %d runs past the symbol table start", r, s)
			}
			symIdx := int(binary.LittleEndian.Uint32(out[recOff+4 : recOff+8]))
			if symIdx < 0 || symIdx >= numSymbols {
				return false, fmt.Errorf("relocation %d of section %d indexes symbol %d, outside the %d-entry table", r, s, symIdx, numSymbols)
			}
			mapped := oldToNew[symIdx]
			if mapped < 0 {
				return false, fmt.Errorf("relocation %d of section %d indexes the removed %q symbol", r, s, name)
			}
			binary.LittleEndian.PutUint32(out[recOff+4:recOff+8], uint32(mapped))
		}
	}
	binary.LittleEndian.PutUint32(out[12:16], uint32(newCount))

	out = append(out, kept...)
	out = append(out, data[symTabOff+numSymbols*symbolSize:]...) // string table, unchanged
	return true, os.WriteFile(path, out, 0o644)
}

// findVCVars64 looks for vcvars64.bat under the two conventional Visual
// Studio install roots, newest layout first. -vcvars overrides this on a
// machine laid out differently.
func findVCVars64() (string, error) {
	var matches []string
	for _, root := range []string{
		`C:\Program Files\Microsoft Visual Studio`,
		`C:\Program Files (x86)\Microsoft Visual Studio`,
	} {
		found, _ := filepath.Glob(filepath.Join(root, "*", "*", "VC", "Auxiliary", "Build", "vcvars64.bat")) //unchecked: the pattern is a literal; ErrBadPattern cannot occur
		matches = append(matches, found...)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("vcvars64.bat not found under Program Files; pass -vcvars <path>")
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}
