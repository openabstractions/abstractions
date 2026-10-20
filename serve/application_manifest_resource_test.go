package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func manifestPE(data []byte, plus bool) []byte {
	optSize, dirs := 224, 96
	magic := uint16(0x10b)
	if plus {
		optSize, dirs, magic = 240, 112, 0x20b
	}
	const raw = 512
	n := (136 + len(data) + 511) &^ 511
	b := make([]byte, raw+n)
	p16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }
	p32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	copy(b, "MZ")
	p32(60, 128)
	copy(b[128:], "PE\x00\x00")
	p16(132, 0x8664)
	p16(134, 1)
	p16(148, uint16(optSize))
	p16(150, 2)
	p16(152, magic)
	p32(152+60, 512)
	p32(152+dirs-4, 16)
	p32(152+dirs+16, 4096)
	p32(152+dirs+20, uint32(n))
	s := 152 + optSize
	copy(b[s:], ".rsrc")
	p32(s+8, uint32(n))
	p32(s+12, 4096)
	p32(s+16, uint32(n))
	p32(s+20, raw)
	p16(raw+14, 1)
	p32(raw+16, 10)
	p32(raw+20, 0x80000000|24)
	p16(raw+24+12, 1)
	p32(raw+40, 0x80000000|88)
	p32(raw+44, 0x80000000|48)
	p16(raw+48+14, 1)
	p32(raw+64, 0)
	p32(raw+68, 72)
	p32(raw+72, 4096+136)
	p32(raw+76, uint32(len(data)))
	p16(raw+88, uint16(len(applicationManifestResourceName)))
	for i, c := range applicationManifestResourceName {
		p16(raw+90+i*2, uint16(c))
	}
	copy(b[raw+136:], data)
	return b
}

func TestApplicationManifestResourceReadsBothPEFormats(t *testing.T) {
	for _, plus := range []bool{false, true} {
		for _, n := range []int{2, applicationManifestMaxBytes} {
			want := bytes.Repeat([]byte("x"), n)
			b := manifestPE(want, plus)
			got, err := readApplicationManifestResource(bytes.NewReader(b), int64(len(b)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("plus=%v bytes=%d: %d %v", plus, n, len(got), err)
			}
		}
	}
}

func TestApplicationManifestResourceRejectsMalformedAndExcludedBytes(t *testing.T) {
	put16 := func(b []byte, at int, n uint16) { binary.LittleEndian.PutUint16(b[at:], n) }
	put32 := func(b []byte, at int, n uint32) { binary.LittleEndian.PutUint32(b[at:], n) }
	cases := map[string]func([]byte) []byte{
		"DOS":                           func(b []byte) []byte { b[0] = 0; return b },
		"not executable":                func(b []byte) []byte { put16(b, 150, 0); return b },
		"DLL":                           func(b []byte) []byte { put16(b, 150, 0x2002); return b },
		"PE signature":                  func(b []byte) []byte { b[128] = 0; return b },
		"header offset overflow":        func(b []byte) []byte { put32(b, 60, 0xfffffff0); return b },
		"optional short":                func(b []byte) []byte { put16(b, 148, 1); return b },
		"optional format":               func(b []byte) []byte { put16(b, 152, 3); return b },
		"section count cap":             func(b []byte) []byte { put16(b, 134, 97); return b },
		"section over file":             func(b []byte) []byte { put32(b, 376+16, 0xffffffff); return b },
		"section in headers":            func(b []byte) []byte { put32(b, 376+20, 128); return b },
		"virtual overflow":              func(b []byte) []byte { put32(b, 376+12, 0xfffffff0); return b },
		"resource outside section":      func(b []byte) []byte { put32(b, 152+96+16, 0xfffffff0); return b },
		"resource size overflow":        func(b []byte) []byte { put32(b, 152+96+20, 0xffffffff); return b },
		"resource outside mapped bytes": func(b []byte) []byte { put32(b, 376+8, 64); return b },
		"certificate overlaps section":  func(b []byte) []byte { put32(b, 152+96+32, 512); put32(b, 152+96+36, 8); return b },
		"certificate in headers":        func(b []byte) []byte { put32(b, 152+96+32, 128); put32(b, 152+96+36, 8); return b },
		"certificate outside file":      func(b []byte) []byte { put32(b, 152+96+32, 0xfffffff8); put32(b, 152+96+36, 8); return b },
		"certificate incomplete":        func(b []byte) []byte { put32(b, 152+96+32, 512); return b },
		"wrong type":                    func(b []byte) []byte { put32(b, 528, 11); return b },
		"wrong name":                    func(b []byte) []byte { put16(b, 602, 'X'); return b },
		"wrong language":                func(b []byte) []byte { put32(b, 576, 1033); return b },
		"duplicate type":                func(b []byte) []byte { put16(b, 526, 2); copy(b[536:544], b[528:536]); return b },
		"duplicate name":                func(b []byte) []byte { put16(b, 548, 2); copy(b[560:568], b[552:560]); return b },
		"multiple languages":            func(b []byte) []byte { put16(b, 574, 2); return b },
		"directory cycle":               func(b []byte) []byte { put32(b, 532, 0x80000000); return b },
		"unaligned directory":           func(b []byte) []byte { put32(b, 532, 0x80000000|25); return b },
		"wrong name entry form":         func(b []byte) []byte { put32(b, 552, 88); return b },
		"directory instead of data":     func(b []byte) []byte { put32(b, 580, 0x80000000|72); return b },
		"data instead of directory":     func(b []byte) []byte { put32(b, 532, 24); return b },
		"payload before resource":       func(b []byte) []byte { put32(b, 584, 128); return b },
		"payload in metadata":           func(b []byte) []byte { put32(b, 584, 4096+72); return b },
		"payload outside range":         func(b []byte) []byte { put32(b, 584, 4096+512); return b },
		"payload offset overflow":       func(b []byte) []byte { put32(b, 584, 0xffffffff); return b },
		"payload size overflow":         func(b []byte) []byte { put32(b, 588, 0xffffffff); return b },
		"reserved":                      func(b []byte) []byte { put32(b, 596, 1); return b },
		"codepage":                      func(b []byte) []byte { put32(b, 592, 1252); return b },
		"invalid UTF8":                  func(b []byte) []byte { b[648] = 0xff; return b },
		"invalid UTF16":                 func(b []byte) []byte { put16(b, 602, 0xd800); return b },
		"unbounded name":                func(b []byte) []byte { put16(b, 600, 65535); return b },
		"named language":                func(b []byte) []byte { put16(b, 572, 1); put16(b, 574, 0); put32(b, 576, 0x80000000|88); return b },
		"overlapping sections":          func(b []byte) []byte { put16(b, 134, 2); copy(b[416:456], b[376:416]); return b },
		"BSS virtual overlap": func(b []byte) []byte {
			put16(b, 134, 2)
			copy(b[416:456], b[376:416])
			put32(b, 416+16, 0)
			put32(b, 416+20, 0)
			return b
		},
		"truncated": func(b []byte) []byte { return b[:len(b)-1] },
		"oversize manifest": func(b []byte) []byte {
			return manifestPE(bytes.Repeat([]byte("x"), applicationManifestMaxBytes+1), false)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := mutate(manifestPE([]byte("{}"), false))
			got, err := readApplicationManifestResource(bytes.NewReader(b), int64(len(b)))
			if err == nil || got != nil {
				t.Fatalf("accepted %q", got)
			}
		})
	}
}

func TestApplicationManifestResourceIgnoresOverlayAsAuthority(t *testing.T) {
	b := manifestPE([]byte("{}"), false)
	binary.LittleEndian.PutUint32(b[528:], 11)
	b = append(b, []byte(applicationManifestResourceName+"{untrusted overlay}")...)
	if _, err := readApplicationManifestResource(bytes.NewReader(b), int64(len(b))); !errors.Is(err, errApplicationManifestMissing) {
		t.Fatal(err)
	}
	// A data entry pointing at appended bytes cannot make them resource data.
	b = manifestPE([]byte("{}"), false)
	binary.LittleEndian.PutUint32(b[584:], 4096+512)
	b = append(b, []byte("{}")...)
	if _, err := readApplicationManifestResource(bytes.NewReader(b), int64(len(b))); err == nil {
		t.Fatal("overlay accepted")
	}
}

type manifestFailReader struct{}

func (manifestFailReader) ReadAt([]byte, int64) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestApplicationManifestResourceBoundedReadsAndFailure(t *testing.T) {
	for _, size := range []int64{-1, 0, 1 << 40} {
		if _, err := readApplicationManifestResource(manifestFailReader{}, size); err == nil {
			t.Fatal("failed read accepted")
		}
	}
	b := manifestPE([]byte("{}"), false)
	for n := 0; n < 650; n++ {
		if _, err := readApplicationManifestResource(bytes.NewReader(b[:min(n, len(b))]), int64(n)); err == nil {
			t.Fatalf("truncation %d accepted", n)
		}
	}
}
