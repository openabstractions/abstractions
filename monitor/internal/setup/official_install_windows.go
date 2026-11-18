//go:build windows

package setup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The Windows Event Log API; golang.org/x/sys/windows carries no wevtapi
// wrappers.
var (
	installWevtapi   = windows.NewLazySystemDLL("wevtapi.dll")
	installEvtQuery  = installWevtapi.NewProc("EvtQuery")
	installEvtNext   = installWevtapi.NewProc("EvtNext")
	installEvtRender = installWevtapi.NewProc("EvtRender")
	installEvtClose  = installWevtapi.NewProc("EvtClose")
)

const (
	codeIntegrityLog  = "Microsoft-Windows-CodeIntegrity/Operational"
	codeIntegrityRead = 64
)

// ReadOfficialInstall reads what Windows recorded about app's official
// installer for this account. It changes nothing.
func ReadOfficialInstall(app AppID) OfficialInstallEvidence {
	var e OfficialInstallEvidence
	official, ok := OfficialProgramOf(app)
	if !ok || official.UninstallKey == "" {
		return e
	}
	if key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+official.UninstallKey, registry.QUERY_VALUE); err == nil {
		e.Registered = true
		e.Version, _, _ = key.GetStringValue("DisplayVersion")
		if info, err := key.Stat(); err == nil {
			e.Written = info.ModTime()
		}
		key.Close()
	}
	if !e.Registered {
		return e
	}
	e.SmartAppControl = smartAppControlState()
	blocks, err := codeIntegrityBlocks()
	if err != nil {
		e.EventsUnread = err.Error()
	}
	for _, block := range blocks {
		if e.Written.IsZero() || (block.At.After(e.Written.Add(-codeIntegrityWindow)) && block.At.Before(e.Written.Add(codeIntegrityWindow))) {
			e.Blocks = append(e.Blocks, block)
		}
	}
	return e
}

// smartAppControlState reads VerifiedAndReputablePolicyState: 0 off,
// 1 enforced, 2 evaluation.
func smartAppControlState() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\CI\Policy`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("VerifiedAndReputablePolicyState")
	if err != nil {
		return ""
	}
	switch value {
	case 0:
		return "off"
	case 1:
		return "on"
	case 2:
		return "evaluation"
	}
	return ""
}

type codeIntegrityEvent struct {
	System struct {
		EventID     uint32 `xml:"EventID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
		Correlation struct {
			ActivityID string `xml:"ActivityID,attr"`
		} `xml:"Correlation"`
	} `xml:"System"`
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

func (e codeIntegrityEvent) field(name string) string {
	for _, d := range e.Data {
		if d.Name == name {
			return strings.TrimSpace(d.Value)
		}
	}
	return ""
}

// codeIntegrityBlocks reads the newest 3077 and 3089 events of the last week.
// An account that may not read the log gets the error and no blocks.
func codeIntegrityBlocks() ([]CodeIntegrityBlock, error) {
	if err := installWevtapi.Load(); err != nil {
		return nil, err
	}
	channel, _ := windows.UTF16PtrFromString(codeIntegrityLog)
	query, _ := windows.UTF16PtrFromString("*[System[(EventID=3077 or EventID=3089) and TimeCreated[timediff(@SystemTime) <= 604800000]]]")
	const channelPath, reverse = 0x1, 0x200
	set, _, callErr := installEvtQuery.Call(0, uintptr(unsafe.Pointer(channel)), uintptr(unsafe.Pointer(query)), channelPath|reverse)
	if set == 0 {
		return nil, fmt.Errorf("EvtQuery: %w", callErr)
	}
	defer installEvtClose.Call(set)
	var events []codeIntegrityEvent
	for len(events) < codeIntegrityRead {
		var handles [16]uintptr
		var returned uint32
		r, _, err := installEvtNext.Call(set, uintptr(len(handles)), uintptr(unsafe.Pointer(&handles[0])), 1000, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) || errors.Is(err, windows.Errno(windows.WAIT_TIMEOUT)) {
				break
			}
			return nil, fmt.Errorf("EvtNext: %w", err)
		}
		for _, h := range handles[:returned] {
			text, renderErr := renderInstallEvent(h)
			//unchecked: closing a rendered event handle; a failure leaks one handle and loses nothing read
			installEvtClose.Call(h)
			var event codeIntegrityEvent
			if renderErr == nil && xml.Unmarshal(text, &event) == nil {
				events = append(events, event)
			}
		}
	}
	signatures := map[string]string{}
	for _, event := range events {
		if event.System.EventID == 3089 && event.System.Correlation.ActivityID != "" {
			signatures[event.System.Correlation.ActivityID] = event.field("TotalSignatureCount")
		}
	}
	var blocks []CodeIntegrityBlock
	for _, event := range events {
		if event.System.EventID != 3077 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.System.TimeCreated.SystemTime)
		if err != nil {
			continue
		}
		file := path.Base(strings.ReplaceAll(event.field("File Name"), `\`, "/"))
		blocks = append(blocks, CodeIntegrityBlock{At: at, File: file, Policy: event.field("PolicyName"),
			Signatures: signatures[event.System.Correlation.ActivityID]})
	}
	return blocks, nil
}

func renderInstallEvent(h uintptr) ([]byte, error) {
	const renderXML = 1
	var used, count uint32
	r, _, err := installEvtRender.Call(0, h, renderXML, 0, 0, uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&count)))
	if r == 0 && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, fmt.Errorf("EvtRender: %w", err)
	}
	if used == 0 {
		return nil, errors.New("EvtRender: empty record")
	}
	buf := make([]uint16, (used+1)/2)
	r, _, err = installEvtRender.Call(0, h, renderXML, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return nil, fmt.Errorf("EvtRender: %w", err)
	}
	return []byte(windows.UTF16ToString(buf)), nil
}
