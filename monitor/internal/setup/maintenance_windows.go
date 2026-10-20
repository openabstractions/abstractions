package setup

import (
	"context"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const maintenanceRegistryPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

// Only machine installer registration supplies family/cache authority. Burn's
// registry command strings are deliberately never read or executed.
func readMaintenanceRegistrations(ctx context.Context) ([]maintenanceRegistration, error) {
	var rows []maintenanceRegistration
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		parent, err := registry.OpenKey(registry.LOCAL_MACHINE, maintenanceRegistryPath, registry.READ|view)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		names, err := parent.ReadSubKeyNames(-1)
		parent.Close()
		if err != nil || len(names) > 4096 {
			return nil, errors.Join(err, errors.New("Setup registration enumeration unavailable"))
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key, err := registry.OpenKey(registry.LOCAL_MACHINE, maintenanceRegistryPath+`\`+name, registry.READ|view)
			if err != nil {
				return nil, err
			}
			families, _, familyErr := key.GetStringsValue("BundleUpgradeCode")
			matches := false
			if familyErr == nil && len(families) == 1 {
				familyID, err := windows.GUIDFromString(families[0])
				matches = err == nil && familyID.String() == maintenanceFamily
			}
			if familyErr != nil || !matches {
				key.Close()
				continue
			}
			bundleID, idErr := windows.GUIDFromString(name)
			if idErr != nil {
				key.Close()
				return nil, errors.New("Setup registration has an invalid bundle code")
			}
			row := maintenanceRegistration{BundleCode: bundleID.String(), Family: maintenanceFamily}
			row.Title, _, err = key.GetStringValue("DisplayName")
			if err == nil {
				row.Version, _, err = key.GetStringValue("DisplayVersion")
			}
			if err == nil {
				row.Program, _, err = key.GetStringValue("BundleCachePath")
			}
			installed, _, installedErr := key.GetIntegerValue("Installed")
			row.Installed = installedErr == nil && installed == 1
			row.Trusted = trustedMaintenanceKey(key) == nil
			key.Close()
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// Registry authority requires a machine installer owner and no other writer.
// This is the registry-specific rights vocabulary; file ACLs remain owned by
// trustedpath. Unknown ACE kinds and an absent DACL are refused.
func trustedMaintenanceKey(key registry.Key) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return trustedMaintenanceDescriptor(sd)
}

func trustedMaintenanceDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	trusted := func(sid *windows.SID) bool {
		if sid == nil {
			return false
		}
		for _, text := range trustedpath.MachineOwners().SIDs {
			if sid.String() == text {
				return true
			}
		}
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return errors.New("Setup registration has an untrusted owner")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("Setup registration has no restrictive DACL")
	}
	const changes = registry.SET_VALUE | registry.CREATE_SUB_KEY | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&changes != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return errors.New("Setup registration permits an untrusted writer")
		}
	}
	return nil
}

func openMaintenanceImage(ctx context.Context, row maintenanceRegistration) (maintenanceImage, error) {
	var result maintenanceImage
	if err := trustedpath.CheckExecutable(row.Program, trustedpath.MachineOwners()); err != nil {
		return result, err
	}
	folders, err := programfiles.HoldDirectory(filepath.Dir(row.Program))
	if err != nil {
		return result, err
	}
	file, err := openArchiveSource(ctx, row.Program)
	if err != nil {
		return result, errors.Join(err, folders.Release())
	}
	close := func() error { return errors.Join(file.Close(), folders.Release()) }
	fail := func(err error) (maintenanceImage, error) { return maintenanceImage{}, errors.Join(err, close()) }
	if err := trustedpath.CheckExecutable(row.Program, trustedpath.MachineOwners()); err != nil {
		return fail(err)
	}
	code, proof, err := identity.VerifyFile(row.Program, nil)
	if err != nil {
		return fail(err)
	}
	bundle, err := maintenanceBundleCode(file)
	if err != nil {
		return fail(err)
	}
	key, err := fileid.FileKeyOf(file)
	if err != nil {
		return fail(err)
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	panelCode, panelProof, err := identity.VerifyFile(self, nil)
	if err != nil {
		return fail(err)
	}
	return maintenanceImage{BundleCode: bundle, FileKey: key, Code: code, Proof: proof, PanelCode: panelCode, PanelProof: panelProof, close: close,
		launch: func() error {
			home, err := CurrentHome()
			if err != nil {
				return err
			}
			root := filepath.Join(home.Config, "maintenance")
			if err := identity.LocalPath(root); err != nil {
				return err
			}
			if err := privatedir.Ensure(root); err != nil {
				return err
			}
			work, err := os.MkdirTemp(root, "run-")
			if err != nil {
				return err
			}
			if err := privatedir.Tighten(work); err != nil {
				return errors.Join(err, os.RemoveAll(work))
			}
			env, err := (supervision.CleanEnvironment{Temp: work, WindowsDefaultPath: true, Names: supervision.Computed()}).Build()
			if err != nil {
				return errors.Join(err, os.RemoveAll(work))
			}
			// CreateProcess receives one admitted executable and fixed arguments.
			// The interactive Burn UI owns all later selection/elevation/install work.
			command := maintenanceCommand(row.Program, env)
			if err := command.Start(); err != nil {
				return errors.Join(err, os.RemoveAll(work))
			}
			go func() {
				_ = command.Wait()
				_ = os.RemoveAll(work)
			}()
			return nil
		}}, nil
}

func maintenanceCommand(program string, env []string) *exec.Cmd {
	command := exec.Command(program, "/modify")
	command.Dir = filepath.Dir(program)
	command.Env = env
	return command
}

// WiX's signed .wixburn header names the bundle GUID at bytes 8..24. Join it
// to the protected machine registration; a signed unrelated OA image is refused.
func maintenanceBundleCode(input io.ReaderAt) (string, error) {
	image, err := pe.NewFile(input)
	if err != nil {
		return "", err
	}
	section := image.Section(".wixburn")
	if section == nil || section.Size < 52 {
		return "", errors.New("Setup image has no Burn identity")
	}
	var raw [24]byte
	if _, err := section.ReadAt(raw[:], 0); err != nil {
		return "", err
	}
	if binary.LittleEndian.Uint32(raw[:4]) != 0x00f14300 || binary.LittleEndian.Uint32(raw[4:8]) != 2 {
		return "", errors.New("unsupported Setup Burn identity")
	}
	guid := windows.GUID{Data1: binary.LittleEndian.Uint32(raw[8:12]), Data2: binary.LittleEndian.Uint16(raw[12:14]), Data3: binary.LittleEndian.Uint16(raw[14:16])}
	copy(guid.Data4[:], raw[16:24])
	if guid == (windows.GUID{}) {
		return "", errors.New("Setup Burn identity is empty")
	}
	return fmt.Sprint(guid), nil
}
