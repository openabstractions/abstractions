package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/digest"
)

const sevenZipOutputLimit = 16 << 20
const sevenZipErrorLimit = 1 << 20
const sevenZipMemberLimit = 100000

type sevenZipPin struct {
	Path   string
	Bytes  int64
	SHA256 string
}

// Inputs come from the authenticated installation owner. Files is the complete
// pre-seal roster, including manifest.json. Parts occupy their own private leaf.
type sevenZipArchive struct {
	Tool          sevenZipPin
	Parts         []sevenZipPin
	ArchiveBytes  int64
	ArchiveSHA256 string
	Files         []archiveFileRecord
	Destination   string // absent, directly below an existing private directory
	MaxExpanded   int64
	MaxMembers    int
	Timeout       time.Duration
}

type sevenZipCommand func(context.Context, string, []string, string, int64) ([]byte, error)

// extractSevenZip publishes verified raw files only. The installation owner must
// perform its native seal before recording an installed receipt or using files.
func extractSevenZip(ctx context.Context, in sevenZipArchive) error {
	return extractSevenZipWith(ctx, in, runSevenZip)
}

func extractSevenZipWith(ctx context.Context, in sevenZipArchive, command sevenZipCommand) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	wanted, dirs, err := sevenZipInventory(in)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	parent := filepath.Dir(in.Destination)
	if err = privatedir.CheckDir(parent); err != nil {
		return err
	}
	parentHold, err := programfiles.HoldDirectory(parent)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parentHold.Release()) }()
	if err = absent(in.Destination); err != nil {
		return err
	}
	if err = sevenZipPartsDirectory(in.Parts); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, ".archive-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	if err = privatedir.Tighten(work); err != nil {
		return err
	}
	workHold, err := programfiles.HoldDirectory(work)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, workHold.Release()) }()
	placed := []programfiles.Placed{{Path: in.Tool.Path, Size: in.Tool.Bytes, SHA256: in.Tool.SHA256}}
	for _, p := range in.Parts {
		placed = append(placed, programfiles.Placed{Path: p.Path, Size: p.Bytes, SHA256: p.SHA256})
	}
	hold, err := programfiles.NewVerifier(programfiles.Options{}).LaunchPlaced(ctx, "archive-inputs", placed, filepath.Join(work, "inputs.json"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, hold.Release()) }()
	paths := hold.Paths()
	for _, p := range paths {
		if err = sevenZipOrdinary(ctx, p, false); err != nil {
			return err
		}
	}
	if err = sevenZipLogicalHash(ctx, paths[1:], in); err != nil {
		return err
	}
	invoke := func(args []string, limit int64) ([]byte, error) {
		if err := sevenZipPartsDirectory(in.Parts); err != nil {
			return nil, err
		}
		out, err := command(ctx, paths[0], args, work, limit)
		if err != nil {
			return nil, err
		}
		if int64(len(out)) > limit {
			return nil, errors.New("setup 7z: command output exceeds bound")
		}
		if err = sevenZipPartsDirectory(in.Parts); err != nil {
			return nil, err
		}
		for _, p := range paths {
			if err = sevenZipOrdinary(ctx, p, false); err != nil {
				return nil, err
			}
		}
		return out, ctx.Err()
	}
	listing, err := invoke([]string{"l", "-slt", "-sccUTF-8", paths[1]}, sevenZipOutputLimit)
	if err != nil {
		return err
	}
	if err = sevenZipListing(listing, wanted, dirs, in.MaxExpanded, in.MaxMembers); err != nil {
		return err
	}
	manifest := wanted["manifest.json"]
	raw, err := invoke([]string{"x", "-so", paths[1], "manifest.json"}, sevenZipOutputLimit)
	if err != nil {
		return err
	}
	if int64(len(raw)) != manifest.Size || digest.Hex(sumSevenZip(raw)) != digest.Prefix+manifest.SHA256 {
		return errors.New("setup 7z: archived manifest differs")
	}
	output := filepath.Join(work, "output")
	if err = privatedir.Make(output); err != nil {
		return err
	}
	outputHold, err := programfiles.HoldDirectory(output)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, outputHold.Release()) }()
	if _, err = invoke([]string{"x", "-y", "-o" + output, paths[1]}, sevenZipOutputLimit); err != nil {
		return err
	}
	if err = sevenZipTree(ctx, output, wanted, dirs, in.MaxMembers); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = outputHold.Release(); err != nil {
		return err
	}
	if err = absent(in.Destination); err != nil {
		return err
	}
	if err = os.Rename(output, in.Destination); err != nil {
		return err
	}
	return nil
}

func sumSevenZip(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }

func sevenZipInventory(in sevenZipArchive) (map[string]archiveFileRecord, map[string]bool, error) {
	fail := func() (map[string]archiveFileRecord, map[string]bool, error) {
		return nil, nil, errors.New("setup 7z: invalid bounded archive inputs")
	}
	if in.Timeout <= 0 || in.Timeout > 10*time.Minute || in.MaxExpanded <= 0 || in.MaxMembers < 1 || in.MaxMembers > sevenZipMemberLimit ||
		len(in.Files) < 1 || len(in.Files) > in.MaxMembers || len(in.Parts) < 1 || len(in.Parts) > 1024 || in.ArchiveBytes <= 0 || !digest.Valid(digest.Prefix+in.ArchiveSHA256) {
		return fail()
	}
	if !filepath.IsAbs(in.Destination) || filepath.Clean(in.Destination) != in.Destination || fileid.SafeName(filepath.Base(in.Destination)) != nil {
		return fail()
	}
	if err := identity.LocalPath(in.Destination); err != nil {
		return nil, nil, err
	}
	for _, p := range append([]sevenZipPin{in.Tool}, in.Parts...) {
		if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || p.Bytes <= 0 || !digest.Valid(digest.Prefix+p.SHA256) {
			return fail()
		}
		if err := identity.LocalPath(p.Path); err != nil {
			return nil, nil, err
		}
	}
	wanted, dirs, folded := map[string]archiveFileRecord{}, map[string]bool{}, map[string]string{}
	var total int64
	for _, f := range in.Files {
		if !sevenZipMember(f.Path) || foldName(f.Path) == foldName(archiveMarker) || f.Size < 0 || f.Size > in.MaxExpanded-total || !digest.Valid(digest.Prefix+f.SHA256) {
			return fail()
		}
		parts := strings.Split(f.Path, "/")
		for i := range parts {
			name := strings.Join(parts[:i+1], "/")
			key := foldName(name)
			if old, ok := folded[key]; ok && old != name {
				return fail()
			}
			folded[key] = name
			if i < len(parts)-1 {
				dirs[name] = true
			}
		}
		if _, ok := wanted[f.Path]; ok {
			return fail()
		}
		wanted[f.Path] = f
		total += f.Size
	}
	for name := range wanted {
		if dirs[name] {
			return fail()
		}
	}
	manifest, ok := wanted["manifest.json"]
	if !ok || manifest.Size <= 0 || manifest.Size > sevenZipOutputLimit || len(wanted)+len(dirs) > in.MaxMembers {
		return fail()
	}
	return wanted, dirs, nil
}

func sevenZipMember(name string) bool {
	return len(name) <= 4096 && strings.Count(name, "/") < 64 && safeMemberPath(name)
}

func sevenZipPartsDirectory(parts []sevenZipPin) error {
	parent := filepath.Dir(parts[0].Path)
	if err := privatedir.CheckDir(parent); err != nil {
		return err
	}
	wanted := map[string]bool{}
	first := filepath.Base(parts[0].Path)
	stem := first
	if len(parts) > 1 || strings.HasSuffix(first, ".001") {
		if !strings.HasSuffix(first, ".7z.001") {
			return errors.New("setup 7z: noncanonical first volume")
		}
		stem = strings.TrimSuffix(first, "001")
	} else if !strings.HasSuffix(first, ".7z") {
		return errors.New("setup 7z: archive suffix differs")
	}
	for i, p := range parts {
		same, err := identity.SameFile(parent, filepath.Dir(p.Path))
		if err != nil || !same {
			return errors.New("setup 7z: parts occupy different directories")
		}
		name := filepath.Base(p.Path)
		if stem != first && name != stem+fmt.Sprintf("%03d", i+1) {
			return errors.New("setup 7z: volume order differs")
		}
		if wanted[name] || !safeMemberPath(name) {
			return errors.New("setup 7z: duplicate volume")
		}
		wanted[name] = true
	}
	f, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer f.Close()
	seen := 0
	for {
		entries, err := f.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, e := range entries {
			if !wanted[e.Name()] || !e.Type().IsRegular() {
				return errors.New("setup 7z: parts directory contains foreign material")
			}
			seen++
		}
		if err == io.EOF {
			break
		}
	}
	if seen != len(parts) {
		return errors.New("setup 7z: missing volume")
	}
	return nil
}

func sevenZipOrdinary(ctx context.Context, path string, directory bool) error {
	h, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer h.Close()
	st, err := h.File().Stat()
	if err != nil {
		return err
	}
	if directory {
		if !st.IsDir() {
			return errors.New("setup 7z: directory changed type")
		}
	} else if !st.Mode().IsRegular() || h.FileID().Links() != 1 {
		return errors.New("setup 7z: file is not ordinary single-name material")
	}
	return nil
}

func sevenZipLogicalHash(ctx context.Context, paths []string, in sevenZipArchive) error {
	h := sha256.New()
	var total int64
	buf := make([]byte, archiveChunk)
	for i, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		var count int64
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			var n int
			n, err = f.Read(buf)
			if n > 0 {
				if int64(n) > in.Parts[i].Bytes-count || int64(n) > in.ArchiveBytes-total {
					err = errors.New("setup 7z: ordered archive exceeds pin")
					break
				}
				count += int64(n)
				total += int64(n)
				_, _ = h.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		closeErr := f.Close()
		if err != io.EOF {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if count != in.Parts[i].Bytes {
			return errors.New("setup 7z: part size differs")
		}
	}
	if total != in.ArchiveBytes || digest.Hex(h.Sum(nil)) != digest.Prefix+in.ArchiveSHA256 {
		return errors.New("setup 7z: ordered logical archive differs")
	}
	return nil
}

func sevenZipListing(raw []byte, wanted map[string]archiveFileRecord, dirs map[string]bool, limit int64, max int) error {
	if len(raw) > sevenZipOutputLimit || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("setup 7z: invalid listing bytes")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	split := strings.SplitN(text, "\n----------\n", 2)
	if len(split) != 2 {
		return errors.New("setup 7z: listing separator absent")
	}
	seen, folded := map[string]bool{}, map[string]bool{}
	count := 0
	var total int64
	allowed := map[string]bool{"Path": true, "Size": true, "Packed Size": true, "Modified": true, "Created": true, "Accessed": true, "Attributes": true, "CRC": true, "Encrypted": true, "Method": true, "Block": true, "Folder": true, "Host OS": true, "Version": true, "Volume Index": true, "Offset": true}
	for _, block := range strings.Split(strings.TrimSpace(split[1]), "\n\n") {
		if block == "" {
			continue
		}
		count++
		if count > max {
			return errors.New("setup 7z: listing member bound")
		}
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, " = ")
			if !ok || !allowed[key] {
				return errors.New("setup 7z: malformed or unsupported listing field")
			}
			if _, ok = fields[key]; ok {
				return errors.New("setup 7z: duplicate listing field")
			}
			fields[key] = value
		}
		name := strings.ReplaceAll(fields["Path"], "\\", "/")
		key := foldName(name)
		if !sevenZipMember(name) || folded[key] || fields["Encrypted"] != "-" || fields["Folder"] != "" && fields["Folder"] != "+" && fields["Folder"] != "-" {
			return errors.New("setup 7z: unsafe listing member")
		}
		folded[key] = true
		size, err := strconv.ParseInt(fields["Size"], 10, 64)
		if err != nil || size < 0 || strconv.FormatInt(size, 10) != fields["Size"] {
			return errors.New("setup 7z: invalid member size")
		}
		directory := fields["Folder"] == "+" || strings.HasPrefix(fields["Attributes"], "D")
		if directory {
			if !dirs[name] || size != 0 {
				return errors.New("setup 7z: foreign directory")
			}
		} else {
			pin, ok := wanted[name]
			if !ok || pin.Size != size || size > limit-total {
				return errors.New("setup 7z: member differs from exact inventory")
			}
			seen[name] = true
			total += size
		}
	}
	if len(seen) != len(wanted) {
		return errors.New("setup 7z: incomplete listing")
	}
	return nil
}

func sevenZipTree(ctx context.Context, root string, wanted map[string]archiveFileRecord, dirs map[string]bool, max int) error {
	seen := 0
	count := 0
	var walk func(string, string) error
	walk = func(dir, relative string) error {
		if err := sevenZipOrdinary(ctx, dir, true); err != nil {
			return err
		}
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			entries, err := f.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				count++
				if count > max {
					return errors.New("setup 7z: extracted member bound")
				}
				name := entry.Name()
				if relative != "" {
					name = relative + "/" + name
				}
				path := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					if !dirs[name] {
						return errors.New("setup 7z: foreign extracted directory")
					}
					if err := walk(path, name); err != nil {
						return err
					}
					continue
				}
				pin, ok := wanted[name]
				if !ok {
					return errors.New("setup 7z: foreign extracted file")
				}
				if err := sevenZipOrdinary(ctx, path, false); err != nil {
					return err
				}
				if err := sevenZipVerifyExtracted(ctx, path, pin); err != nil {
					return err
				}
				seen++
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return err
	}
	if seen != len(wanted) {
		return errors.New("setup 7z: missing extracted file")
	}
	return nil
}

func sevenZipVerifyExtracted(ctx context.Context, path string, pin archiveFileRecord) error {
	h, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return err
	}
	defer h.Close()
	st, err := h.File().Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || h.FileID().Links() != 1 || st.Size() != pin.Size {
		return errors.New("setup 7z: extracted file type or size differs")
	}
	return verifyArchive(ctx, h.File(), Artifact{Bytes: pin.Size, SHA256: pin.SHA256})
}

// The executable and argv are fixed by the archive owner. The shared supervisor
// owns cancellation and descendant reaping; pipes retain only bounded bytes.
func runSevenZip(ctx context.Context, tool string, args []string, work string, limit int64) ([]byte, error) {
	if args[0] != "x" || len(args) < 2 || args[1] != "-y" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	env, err := (supervision.CleanEnvironment{Temp: work}).Build()
	if err != nil {
		return nil, err
	}
	stdout, out, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	defer out.Close()
	stderr, failure, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stderr.Close()
	defer failure.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	p, err := supervision.Start(ctx, supervision.Options{Executable: tool, Args: args, Dir: work, Env: env, Stdin: input, Stdout: out, Stderr: failure, ForceTimeout: 3 * time.Second, ProcessMemoryLimit: 512 << 20})
	if err != nil {
		return nil, err
	}
	_ = out.Close()
	_ = failure.Close()
	type result struct {
		data []byte
		err  error
	}
	outputs := make(chan result, 1)
	failures := make(chan result, 1)
	read := func(file *os.File, bound int64, ch chan result) {
		raw, err := io.ReadAll(io.LimitReader(file, bound+1))
		if int64(len(raw)) > bound {
			err = errors.New("setup 7z: output bound exceeded")
			cancel()
		}
		ch <- result{raw, err}
	}
	go read(stdout, limit, outputs)
	go read(stderr, sevenZipErrorLimit, failures)
	code, waitErr := p.Wait()
	output, diagnostic := <-outputs, <-failures
	if err := errors.Join(output.err, diagnostic.err, waitErr, ctx.Err()); err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("setup 7z: child exited %d", code)
	}
	return output.data, nil
}
