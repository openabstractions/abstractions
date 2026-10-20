// Command gen builds the Panel's native application icons and resource script
// from the brand mark, and — on Windows, with -syso — compiles the linkable
// resource objects go build picks up on its own.
//
// Default run (any OS, standard library only):
//
//	go run ./monitor/icon/gen
//
// reads openabstractions-flat/openabstractions.github.io/brand/mark-512.png,
// places the existing mark on a pale rounded plate for contrast on either
// desktop theme, and derives the native sizes by area-average downscaling.
// It writes them beside this program's package (monitor/icon/mark-<n>.png),
// packs them into monitor/icon/panel.ico and panel.icns, and
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
	"image/draw"
	"image/png"
	"math"
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

// nativeSizes include the Windows ICO sizes and macOS's largest source size.
// ICO entries stop at 256; the 512 image belongs to the ICNS and PNG outputs.
var nativeSizes = []int{16, 24, 32, 48, 64, 128, 256, 512}

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

// generateAssets plates and resamples the brand mark into iconDir, packs
// panel.ico and panel.icns there, and writes monitorDir/panel.rc.
func generateAssets(brandDir, iconDir, monitorDir string) error {
	sourcePath := filepath.Join(brandDir, "mark-512.png")
	source, err := loadPNG(sourcePath)
	if err != nil {
		return fmt.Errorf("load brand mark: %w", err)
	}
	if b := source.Bounds(); b.Dx() != 512 || b.Dy() != 512 {
		return fmt.Errorf("%s: is %dx%d, want 512x512", sourcePath, b.Dx(), b.Dy())
	}
	plated := plateMark(source)

	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return err
	}

	var entries []builtIcon
	bySize := map[int][]byte{}
	for _, size := range nativeSizes {
		out := filepath.Join(iconDir, fmt.Sprintf("mark-%d.png", size))
		img := plated
		if size != 512 {
			img = boxDownscale(plated, size, size)
		}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode mark-%d.png: %w", size, err)
		}
		data := buf.Bytes()
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		bySize[size] = data
		if size <= 256 {
			entries = append(entries, builtIcon{size, data})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })

	icoPath := filepath.Join(iconDir, "panel.ico")
	if err := writeICO(icoPath, entries); err != nil {
		return fmt.Errorf("write panel.ico: %w", err)
	}
	fmt.Printf("wrote %s (%d entries)\n", icoPath, len(entries)) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced
	icnsPath := filepath.Join(iconDir, "panel.icns")
	if err := writeICNS(icnsPath, bySize); err != nil {
		return fmt.Errorf("write panel.icns: %w", err)
	}
	fmt.Println("wrote", icnsPath) //unchecked: a build tool's progress line on stdout; a failed write there changes nothing it produced

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

// plateMark carries a stable light ground behind the original dark artwork.
// The plate has a 2.5% outer inset and an 18% corner radius. The unchanged
// artwork occupies 93.75% of its source square, leaving its strokes inside
// the plate. Subpixel coverage keeps the transparent corners smooth.
func plateMark(src image.Image) *image.NRGBA {
	const size = 512
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	const inset = size * 0.025
	const radius = size * 0.18
	const edge = size - inset
	const samples = 8
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			inside := 0
			for yy := 0; yy < samples; yy++ {
				for xx := 0; xx < samples; xx++ {
					px := float64(x) + (float64(xx)+0.5)/samples
					py := float64(y) + (float64(yy)+0.5)/samples
					if px < inset || px > edge || py < inset || py > edge {
						continue
					}
					cx := math.Max(inset+radius, math.Min(edge-radius, px))
					cy := math.Max(inset+radius, math.Min(edge-radius, py))
					if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= radius*radius {
						inside++
					}
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{R: 0xf4, G: 0xf7, B: 0xfb, A: uint8((inside*255 + samples*samples/2) / (samples * samples))})
		}
	}
	mark := boxDownscale(src, 480, 480)
	draw.Draw(dst, image.Rect(16, 16, 496, 496), mark, mark.Bounds().Min, draw.Over)
	return dst
}

// boxDownscale uses area-weighted premultiplied alpha. Transparent corner
// pixels cannot darken the pale edge, and fractional ratios (512 to 24 or
// 48) cover every source pixel evenly.
func boxDownscale(src image.Image, w, h int) *image.NRGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy0 := float64(y) * float64(sh) / float64(h)
		sy1 := float64(y+1) * float64(sh) / float64(h)
		for x := 0; x < w; x++ {
			sx0 := float64(x) * float64(sw) / float64(w)
			sx1 := float64(x+1) * float64(sw) / float64(w)
			var r, g, b, a float64
			for yy := int(math.Floor(sy0)); yy < int(math.Ceil(sy1)); yy++ {
				for xx := int(math.Floor(sx0)); xx < int(math.Ceil(sx1)); xx++ {
					weight := (math.Min(sx1, float64(xx+1)) - math.Max(sx0, float64(xx))) *
						(math.Min(sy1, float64(yy+1)) - math.Max(sy0, float64(yy)))
					c := color.NRGBAModel.Convert(src.At(sb.Min.X+xx, sb.Min.Y+yy)).(color.NRGBA)
					alpha := float64(c.A) * weight
					r += float64(c.R) * alpha
					g += float64(c.G) * alpha
					b += float64(c.B) * alpha
					a += alpha
				}
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{
					R: uint8(math.Round(r / a)), G: uint8(math.Round(g / a)), B: uint8(math.Round(b / a)),
					A: uint8(math.Round(a / ((sx1 - sx0) * (sy1 - sy0)))),
				})
			}
		}
	}
	return dst
}

// writeICNS stores the native PNGs in modern ICNS chunks. Each big-endian
// chunk length includes its eight-byte header, as does the container length.
// The retina entries carry the same images the previous iconutil iconset
// recipe used; the source artwork's largest representation is 512 pixels.
func writeICNS(path string, bySize map[int][]byte) error {
	var payload bytes.Buffer
	for _, e := range []struct {
		kind string
		size int
	}{
		{"icp4", 16}, {"icp5", 32}, {"icp6", 64},
		{"ic07", 128}, {"ic08", 256}, {"ic09", 512},
		{"ic11", 32}, {"ic12", 64}, {"ic13", 256}, {"ic14", 512},
	} {
		data := bySize[e.size]
		payload.WriteString(e.kind)                                   //unchecked: a write into a bytes.Buffer cannot fail
		binary.Write(&payload, binary.BigEndian, uint32(len(data)+8)) //unchecked: a write into a bytes.Buffer cannot fail
		payload.Write(data)                                           //unchecked: a write into a bytes.Buffer cannot fail
	}
	var buf bytes.Buffer
	buf.WriteString("icns")                                       //unchecked: a write into a bytes.Buffer cannot fail
	binary.Write(&buf, binary.BigEndian, uint32(payload.Len()+8)) //unchecked: a write into a bytes.Buffer cannot fail
	buf.Write(payload.Bytes())                                    //unchecked: a write into a bytes.Buffer cannot fail
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
