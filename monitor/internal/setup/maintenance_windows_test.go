package setup

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMaintenanceCommandUsesOnlyFixedInteractiveArguments(t *testing.T) {
	program := `C:\ProgramData\Package Cache\{fixture}\OpenAbstractions-Setup-0.8.0.exe`
	env := []string{"SystemRoot=C:\\Windows"}
	command := maintenanceCommand(program, env)
	if command.Path != program || !reflect.DeepEqual(command.Args, []string{program, "/modify"}) || !reflect.DeepEqual(command.Env, env) {
		t.Fatalf("registry text entered command: %+v", command)
	}
}

func TestMaintenanceReadsBundleCodeFromTheActualPESection(t *testing.T) {
	// Minimal PE with one .wixburn section. Every byte is inert.
	raw := make([]byte, 0x240)
	copy(raw, "MZ")
	binary.LittleEndian.PutUint32(raw[0x3c:], 0x80)
	copy(raw[0x80:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(raw[0x84:], 0x14c)
	binary.LittleEndian.PutUint16(raw[0x86:], 1)
	binary.LittleEndian.PutUint16(raw[0x94:], 224)
	binary.LittleEndian.PutUint16(raw[0x98:], 0x10b)
	binary.LittleEndian.PutUint32(raw[0x98+92:], 16)
	section := 0x98 + 224
	copy(raw[section:], ".wixburn")
	binary.LittleEndian.PutUint32(raw[section+16:], 64)
	binary.LittleEndian.PutUint32(raw[section+20:], 0x200)
	binary.LittleEndian.PutUint32(raw[0x200:], 0x00f14300)
	binary.LittleEndian.PutUint32(raw[0x204:], 2)
	want, err := windows.GUIDFromString("{91C8B12A-64B2-4CF7-966E-76BD6977580F}")
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(raw[0x208:], want.Data1)
	binary.LittleEndian.PutUint16(raw[0x20c:], want.Data2)
	binary.LittleEndian.PutUint16(raw[0x20e:], want.Data3)
	copy(raw[0x210:], want.Data4[:])
	if got, err := maintenanceBundleCode(bytes.NewReader(raw)); err != nil || got != want.String() {
		t.Fatalf("Burn identity %s %v", got, err)
	}
	for _, offset := range []int{0, section, 0x201, 0x204} {
		changed := append([]byte(nil), raw...)
		changed[offset] = 0
		if _, err := maintenanceBundleCode(bytes.NewReader(changed)); err == nil {
			t.Fatalf("malformed PE/Burn identity accepted at %d", offset)
		}
	}
}

func TestMaintenanceRegistrationRequiresMachineOwnerAndNoOtherWriter(t *testing.T) {
	for _, row := range []struct {
		sddl string
		ok   bool
	}{
		{"O:SYG:SYD:(A;;KA;;;SY)(A;;KA;;;BA)(A;;KR;;;BU)", true},
		{"O:SYG:SYD:(A;;KA;;;SY)(A;;KW;;;BU)", false},
		{"O:BUG:SYD:(A;;KA;;;SY)(A;;KR;;;BU)", false},
		{"O:SYG:SY", false},
	} {
		sd, err := windows.SecurityDescriptorFromString(row.sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := trustedMaintenanceDescriptor(sd); (err == nil) != row.ok {
			t.Fatalf("%s: %v", row.sddl, err)
		}
	}
}
