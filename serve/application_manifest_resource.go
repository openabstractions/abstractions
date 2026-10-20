package main

import (
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go/manifest"
)

const applicationManifestResourceName = "OA_APPLICATION_MANIFEST"
const applicationManifestMaxBytes = manifest.MaxApplicationManifestBytes

var errApplicationManifestMissing = errors.New("application manifest: named neutral RCDATA resource absent")
var errApplicationManifestResource = errors.New("application manifest: malformed or ambiguous PE resource")
var errApplicationManifestUnsupported = errors.New("application manifest: native signed resource unsupported on this platform")

type manifestSpan struct{ start, end uint64 }

func (a manifestSpan) overlaps(b manifestSpan) bool { return a.start < b.end && b.start < a.end }

// readApplicationManifestResource reads DATA through the caller's held file.
// It parses bounded headers directly: debug/pe.NewFile also reads a COFF symbol
// table, whose allocations this authority reader must not accept from the file.
// Every resource structure and payload must belong to one mapped resource range
// in a non-overlapping raw section, outside Authenticode's excluded certificate
// range. No PE loader, code, overlay or certificate padding is consulted.
func readApplicationManifestResource(r io.ReaderAt, size int64) ([]byte, error) {
	return readApplicationNamedResource(r, size, applicationManifestResourceName)
}

func readApplicationNamedResource(r io.ReaderAt, size int64, resourceName string) ([]byte, error) {
	bad := errApplicationManifestResource
	read := func(off, n uint64) ([]byte, error) {
		if size < 0 || off > uint64(size) || n > uint64(size)-off || n > 1<<20 {
			return nil, bad
		}
		b := make([]byte, int(n))
		if _, err := r.ReadAt(b, int64(off)); err != nil {
			return nil, bad
		}
		return b, nil
	}
	u16 := binary.LittleEndian.Uint16
	u32 := binary.LittleEndian.Uint32
	dos, err := read(0, 64)
	if err != nil || string(dos[:2]) != "MZ" {
		return nil, bad
	}
	peAt := uint64(u32(dos[60:]))
	if peAt < 64 || peAt > 1<<20 {
		return nil, bad
	}
	coff, err := read(peAt, 24)
	if err != nil || string(coff[:4]) != "PE\x00\x00" {
		return nil, bad
	}
	if flags := u16(coff[22:]); flags&2 == 0 || flags&0x2000 != 0 {
		return nil, bad
	}
	count, optSize := uint64(u16(coff[6:])), uint64(u16(coff[20:]))
	if count == 0 || count > 96 || optSize < 2 || optSize > 4096 {
		return nil, bad
	}
	opt, err := read(peAt+24, optSize)
	if err != nil {
		return nil, bad
	}
	var dirs, number uint64
	switch u16(opt) {
	case 0x10b:
		dirs, number = 96, 92
	case 0x20b:
		dirs, number = 112, 108
	default:
		return nil, bad
	}
	if optSize < dirs+5*8 || uint64(u32(opt[number:])) < 5 {
		return nil, bad
	}
	headers := uint64(u32(opt[60:]))
	tableAt := peAt + 24 + optSize
	if headers < tableAt+count*40 || headers > uint64(size) {
		return nil, bad
	}
	table, err := read(tableAt, count*40)
	if err != nil {
		return nil, bad
	}
	resourceRVA, resourceSize := uint64(u32(opt[dirs+16:])), uint64(u32(opt[dirs+20:]))
	certAt, certSize := uint64(u32(opt[dirs+32:])), uint64(u32(opt[dirs+36:]))
	if (certAt == 0) != (certSize == 0) || certAt > uint64(size) || certSize > uint64(size)-certAt {
		return nil, bad
	}
	cert := manifestSpan{certAt, certAt + certSize}
	if certSize != 0 && (certAt < headers || certAt%8 != 0) {
		return nil, bad
	}
	var rawRanges, virtualRanges []manifestSpan
	var resourceAt uint64
	found := false
	for i := uint64(0); i < count; i++ {
		s := table[i*40:]
		vs, va, rs, ra := uint64(u32(s[8:])), uint64(u32(s[12:])), uint64(u32(s[16:])), uint64(u32(s[20:]))
		if va+vs > 1<<32 || ra > uint64(size) || rs > uint64(size)-ra {
			return nil, bad
		}
		raw, virtual := manifestSpan{ra, ra + rs}, manifestSpan{va, va + max(vs, rs)}
		if rs != 0 && (ra < headers || certSize != 0 && raw.overlaps(cert)) || virtual.end > 1<<32 {
			return nil, bad
		}
		for _, old := range rawRanges {
			if rs != 0 && raw.overlaps(old) {
				return nil, bad
			}
		}
		for _, old := range virtualRanges {
			if virtual.end > virtual.start && virtual.overlaps(old) {
				return nil, bad
			}
		}
		if rs != 0 {
			rawRanges = append(rawRanges, raw)
		}
		if virtual.end > virtual.start {
			virtualRanges = append(virtualRanges, virtual)
		}
		if resourceRVA >= va && resourceRVA+resourceSize <= va+min(vs, rs) && resourceSize != 0 {
			if found {
				return nil, bad
			}
			resourceAt, found = ra+resourceRVA-va, true
		}
	}
	if resourceRVA == 0 && resourceSize == 0 {
		return nil, errApplicationManifestMissing
	}
	if !found || resourceSize < 16 || resourceRVA+resourceSize > 1<<32 {
		return nil, bad
	}
	var claims []manifestSpan
	claim := func(off, n uint64) error {
		if n == 0 || off > resourceSize || n > resourceSize-off {
			return bad
		}
		next := manifestSpan{off, off + n}
		for _, old := range claims {
			if old.overlaps(next) {
				return bad
			}
		}
		claims = append(claims, next)
		return nil
	}
	resourceRead := func(off, n uint64) ([]byte, error) {
		if off > resourceSize || n > resourceSize-off {
			return nil, bad
		}
		return read(resourceAt+off, n)
	}
	total := 0
	var answer []byte
	var walk func(uint64, int, bool) error
	walk = func(off uint64, depth int, target bool) error {
		if depth > 2 || off%4 != 0 {
			return bad
		}
		h, err := resourceRead(off, 16)
		if err != nil {
			return err
		}
		named, ids := int(u16(h[12:])), int(u16(h[14:]))
		n := named + ids
		total += n
		if n == 0 || total > 4096 {
			return bad
		}
		if err := claim(off, uint64(16+n*8)); err != nil {
			return err
		}
		entries, err := resourceRead(off+16, uint64(n*8))
		if err != nil {
			return err
		}
		keys := make(map[string]bool, n)
		for i := 0; i < n; i++ {
			e := entries[i*8:]
			name, child := u32(e), u32(e[4:])
			isName := name&0x80000000 != 0
			if isName != (i < named) {
				return bad
			}
			key := ""
			if isName {
				at := uint64(name & 0x7fffffff)
				if at%2 != 0 {
					return bad
				}
				length, err := resourceRead(at, 2)
				if err != nil {
					return err
				}
				units := uint64(u16(length))
				if units == 0 || units > 256 {
					return bad
				}
				if err := claim(at, 2+units*2); err != nil {
					return err
				}
				text, err := resourceRead(at+2, units*2)
				if err != nil {
					return err
				}
				words := make([]uint16, units)
				for j := range words {
					words[j] = u16(text[j*2:])
					if words[j] == 0 {
						return bad
					}
				}
				decoded := utf16.Decode(words)
				for _, c := range decoded {
					if c == utf8.RuneError {
						return bad
					}
				}
				key = "n:" + string(decoded)
			} else {
				if name > 65535 {
					return bad
				}
				key = string([]byte{'i', byte(name >> 8), byte(name)})
			}
			if keys[key] {
				return bad
			}
			keys[key] = true
			selected := target
			switch depth {
			case 0:
				selected = !isName && name == 10
			case 1:
				selected = target && isName && key == "n:"+resourceName
			case 2:
				if target && (isName || name != 0 || n != 1) {
					return bad
				}
			}
			at := uint64(child & 0x7fffffff)
			if depth < 2 {
				if child&0x80000000 == 0 {
					return bad
				}
				if err := walk(at, depth+1, selected); err != nil {
					return err
				}
				continue
			}
			if child&0x80000000 != 0 || at%4 != 0 {
				return bad
			}
			if err := claim(at, 16); err != nil {
				return err
			}
			data, err := resourceRead(at, 16)
			if err != nil {
				return err
			}
			rva, nbytes := uint64(u32(data)), uint64(u32(data[4:]))
			if u32(data[12:]) != 0 || rva < resourceRVA {
				return bad
			}
			payload := rva - resourceRVA
			if err := claim(payload, nbytes); err != nil {
				return err
			}
			if selected {
				if answer != nil || nbytes > applicationManifestMaxBytes || u32(data[8:]) != 0 && u32(data[8:]) != 65001 {
					return bad
				}
				answer, err = resourceRead(payload, nbytes)
				if err != nil || !utf8.Valid(answer) {
					return bad
				}
			}
		}
		return nil
	}
	if err := walk(0, 0, false); err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, errApplicationManifestMissing
	}
	return answer, nil
}
