//go:build windows

package main

import (
	"encoding/binary"
	"errors"
)

// withManifestResource returns a copy of the Windows executable exe with one
// added .rsrc section holding name as a named, language-neutral RCDATA
// resource, the layout rc.exe gives OA_APPLICATION_MANIFEST. The image must
// carry no resource directory and no signature yet. The section is appended
// at the end of the file; the loader maps it like any other initialized data.
func withManifestResource(exe []byte, name string, data []byte) ([]byte, error) {
	bad := func(why string) ([]byte, error) { return nil, errors.New("manifest resource: " + why) }
	u16, u32 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32
	p16, p32 := binary.LittleEndian.PutUint16, binary.LittleEndian.PutUint32
	if len(exe) < 64 || string(exe[:2]) != "MZ" {
		return bad("not a PE image")
	}
	pe := int(u32(exe[60:]))
	if pe < 64 || pe+24 > len(exe) || string(exe[pe:pe+4]) != "PE\x00\x00" {
		return bad("no PE header")
	}
	count := int(u16(exe[pe+6:]))
	optSize := int(u16(exe[pe+20:]))
	opt := pe + 24
	if opt+optSize > len(exe) || u16(exe[opt:]) != 0x20b {
		return bad("only PE32+ images are supported")
	}
	sectionAlign, fileAlign := u32(exe[opt+32:]), u32(exe[opt+36:])
	headers := int(u32(exe[opt+60:]))
	dirs := opt + 112
	if u32(exe[opt+108:]) < 5 {
		return bad("too few data directories")
	}
	if u32(exe[dirs+16:]) != 0 || u32(exe[dirs+20:]) != 0 {
		return bad("the image already has resources")
	}
	if u32(exe[dirs+32:]) != 0 || u32(exe[dirs+36:]) != 0 {
		return bad("the image is signed")
	}
	table := opt + optSize
	if table+(count+1)*40 > headers {
		return bad("no room for another section header")
	}
	var end uint32
	for i := 0; i < count; i++ {
		s := table + i*40
		if ra := int(u32(exe[s+20:])); u32(exe[s+16:]) != 0 && ra < table+(count+1)*40 {
			return bad("a section starts inside the headers")
		}
		vs, va := u32(exe[s+8:]), u32(exe[s+12:])
		if va+vs > end {
			end = va + vs
		}
	}
	align := func(v, a uint32) uint32 { return (v + a - 1) &^ (a - 1) }
	va := align(end, sectionAlign)

	// Root (RT_RCDATA) -> named entry -> neutral language -> data entry.
	units := []uint16{}
	for _, c := range name {
		units = append(units, uint16(c))
	}
	nameAt := 88
	dataAt := (nameAt + 2 + 2*len(units) + 7) &^ 7
	content := make([]byte, dataAt+len(data))
	p16(content[14:], 1)
	p32(content[16:], 10)
	p32(content[20:], 0x80000000|24)
	p16(content[24+12:], 1)
	p32(content[40:], 0x80000000|uint32(nameAt))
	p32(content[44:], 0x80000000|48)
	p16(content[48+14:], 1)
	p32(content[64:], 0)
	p32(content[68:], 72)
	p32(content[72:], va+uint32(dataAt))
	p32(content[76:], uint32(len(data)))
	p16(content[nameAt:], uint16(len(units)))
	for i, w := range units {
		p16(content[nameAt+2+2*i:], w)
	}
	copy(content[dataAt:], data)

	raw := align(uint32(len(exe)), fileAlign)
	rawSize := align(uint32(len(content)), fileAlign)
	out := make([]byte, int(raw)+int(rawSize))
	copy(out, exe)
	copy(out[raw:], content)
	s := table + count*40
	copy(out[s:s+8], ".rsrc\x00\x00\x00")
	p32(out[s+8:], uint32(len(content)))
	p32(out[s+12:], va)
	p32(out[s+16:], rawSize)
	p32(out[s+20:], raw)
	p32(out[s+36:], 0x40000040) // initialized data, readable
	p16(out[pe+6:], uint16(count+1))
	p32(out[opt+56:], align(va+uint32(len(content)), sectionAlign))
	p32(out[dirs+16:], va)
	p32(out[dirs+20:], uint32(len(content)))
	return out, nil
}
