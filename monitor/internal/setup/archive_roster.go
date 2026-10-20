package setup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// expectedArchiveRoster derives file expectations from the actual catalog-
// pinned ZIP, independently of the mutable installation marker.
func expectedArchiveRoster(ctx context.Context, a Artifact, source string) ([]archiveFileRecord, []string, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return nil, nil, errors.New("archive source must be absolute and clean")
	}
	input, err := openArchiveSource(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	defer input.Close()
	if err := verifyArchive(ctx, input, a); err != nil {
		return nil, nil, err
	}
	reader, err := zip.NewReader(input, a.Bytes)
	if err != nil {
		return nil, nil, err
	}
	files := normalizedArchiveMembers(a, reader.File)
	if err := validateMembers(files, a.PeakBytes); err != nil {
		return nil, nil, err
	}
	var records []archiveFileRecord
	dirs := map[string]bool{}
	var total int64
	buf := make([]byte, archiveChunk)
	for _, f := range files {
		name := strings.TrimSuffix(f.Name, "/")
		parts := strings.Split(name, "/")
		for i := 0; i < len(parts)-1; i++ {
			dirs[strings.Join(parts[:i+1], "/")] = true
		}
		if strings.HasSuffix(f.Name, "/") {
			dirs[name] = true
			continue
		}
		member, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		h := sha256.New()
		start := total
		err = copyBounded(ctx, h, member, buf, &total, a.PeakBytes)
		err = errors.Join(err, member.Close())
		if err != nil {
			return nil, nil, err
		}
		records = append(records, archiveFileRecord{Path: name, Size: total - start, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	if a.InstallIdentity == "tn" {
		for i := range records {
			records[i].Path = "e/" + records[i].Path
		}
		prefixed := map[string]bool{"e": true}
		for name := range dirs {
			prefixed["e/"+name] = true
		}
		dirs = prefixed
	}
	ordered := make([]string, 0, len(dirs))
	for name := range dirs {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	if err := verifyArchive(ctx, input, a); err != nil {
		return nil, nil, err
	}
	return records, ordered, nil
}

var receiptArchiveRoster = expectedArchiveRoster // focused fixture observation seam

func receiptHasExpectedRoster(ctx context.Context, a Artifact, r archiveReceipt) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if r.Format != 2 || r.ArchivePath == "" {
		return errors.New("installed package needs its pinned source archive before use")
	}
	files, dirs, err := receiptArchiveRoster(ctx, a, r.ArchivePath)
	if err != nil {
		return err
	}
	if a.NativeResources != nil {
		if err := validateNativeMarkerBudget(a, files, dirs); err != nil {
			return err
		}
	}
	actualDirs := append([]string{}, r.Dirs...)
	sort.Strings(actualDirs)
	if !reflect.DeepEqual(files, r.Files) || !reflect.DeepEqual(dirs, actualDirs) {
		return errors.New("install receipt differs from the catalog-pinned archive roster")
	}
	return nil
}

func normalizedArchiveMembers(a Artifact, files []*zip.File) []*zip.File {
	if a.InstallIdentity != "tn" {
		return files
	}
	result := make([]*zip.File, 0, len(files))
	for _, f := range files {
		if f.Name == "./" && f.Mode().IsDir() {
			continue
		}
		copy := *f
		copy.Name = strings.TrimPrefix(copy.Name, "./")
		result = append(result, &copy)
	}
	return result
}
