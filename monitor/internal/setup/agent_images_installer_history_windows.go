package setup

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	"golang.org/x/sys/windows"
)

type installerGatewayPredecessor struct {
	ProductCode  string `json:"product_code"`
	MSIHash      string `json:"msi_sha256"`
	ReceiptHash  string `json:"receipt_sha256"`
	BundleHash   string `json:"bundle_sha256"`
	GatewayID    string `json:"gateway_id"`
	GatewayPath  string `json:"gateway_path"`
	GatewayHash  string `json:"gateway_sha256"`
	GatewayBytes int64  `json:"gateway_bytes"`
}
type installerGatewayHistory struct {
	Schema       int                           `json:"schema"`
	Predecessors []installerGatewayPredecessor `json:"predecessors"`
}

func decodeInstallerGatewayHistory(body []byte) (installerGatewayHistory, error) {
	var history installerGatewayHistory
	if len(body) == 0 || len(body) > 16384 {
		return history, errors.New("installer predecessor pins unavailable; Connect or remove the unknown helper")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&history); err != nil {
		return history, err
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return history, err
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, body) || history.Schema != 1 || len(history.Predecessors) > 8 {
		return history, errors.New("invalid installer predecessor pins")
	}
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	product := regexp.MustCompile(`^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$`)
	seen := map[string]bool{}
	for _, pin := range history.Predecessors {
		if !product.MatchString(pin.ProductCode) || seen[pin.ProductCode] || !digest.MatchString(pin.MSIHash) || !digest.MatchString(pin.ReceiptHash) || !digest.MatchString(pin.BundleHash) || !digest.MatchString(pin.GatewayHash) || pin.GatewayBytes < 1 || pin.GatewayBytes > 256<<20 || pin.GatewayID == "" || strings.ContainsAny(pin.GatewayID, "/\\:\x00") || !strings.HasSuffix(pin.GatewayPath, "/OpenAbstractions/tools/openabstractions-mcp.exe") {
			return history, errors.New("invalid installer predecessor authority")
		}
		seen[pin.ProductCode] = true
	}
	return history, nil
}

// Read only this admitted Panel's own resource. Runtime paths and account files
// never nominate predecessor authority; its producer validated the prior MSI.
func currentInstallerGatewayHistory() (installerGatewayHistory, error) {
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	name, _ := windows.UTF16PtrFromString("OA_INSTALLER_GATEWAY_HISTORY")
	resource, _, err := kernel.NewProc("FindResourceW").Call(0, uintptr(unsafe.Pointer(name)), 10)
	if resource == 0 {
		return installerGatewayHistory{}, fmt.Errorf("installer predecessor resource unavailable: %w", err)
	}
	size, _, _ := kernel.NewProc("SizeofResource").Call(0, resource)
	if size == 0 || size > 16384 {
		return installerGatewayHistory{}, errors.New("installer predecessor resource exceeds bound")
	}
	loaded, _, err := kernel.NewProc("LoadResource").Call(0, resource)
	if loaded == 0 {
		return installerGatewayHistory{}, err
	}
	pointer, _, err := kernel.NewProc("LockResource").Call(loaded)
	if pointer == 0 {
		return installerGatewayHistory{}, err
	}
	return decodeInstallerGatewayHistory(append([]byte(nil), unsafe.Slice((*byte)(nativePointer(pointer)), int(size))...))
}

// This physical installer operation grants no enrollment, consent or Rights.
// Existing enrolled images continue through Hold/Update at the caller.
func ReplaceUnenrolledInstallerAgentImage(ctx context.Context, machine *client.Machine, party AgentParty) (*AgentImage, error) {
	if party != AgentOpenCode || machine == nil {
		return nil, errors.New("installer replacement target unavailable")
	}
	application, err := agentApplicationName(party, agentImageName(party))
	if err != nil {
		return nil, err
	}
	history, err := currentInstallerGatewayHistory()
	if err != nil {
		return nil, err
	}
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("installer replacement requires the installed account")
	}
	binding, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return nil, err
	}
	read := func(ctx context.Context) (applications.AgentEnrollmentResult, error) {
		return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadAgentEnrollment(application)
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return replaceUnenrolledInstallerImage(ctx, filepath.Join(home.Config, "agent-integrations"), party, application, source, history, read)
}

func requireNoInstallerEnrollment(ctx context.Context, application string, read func(context.Context) (applications.AgentEnrollmentResult, error)) error {
	result, err := read(ctx)
	if err != nil {
		return err
	}
	if result.Outcome == applications.OutcomeUnknown && result.Enrollment == nil {
		return nil
	}
	if result.Outcome == applications.OutcomePage && result.Enrollment != nil && result.Enrollment.Application == application && result.Enrollment.State == applications.AgentEnrollmentStateNone {
		return nil
	}
	return errors.New("integration enrollment exists or is unavailable; use Connect/Update")
}

func replaceUnenrolledInstallerImage(ctx context.Context, root string, party AgentParty, application string, source *AgentImage, history installerGatewayHistory, read func(context.Context) (applications.AgentEnrollmentResult, error)) (*AgentImage, error) {
	if err := requireNoInstallerEnrollment(ctx, application, read); err != nil {
		return nil, err
	}
	old, err := holdAgentImageForReview(ctx, root, party)
	if err != nil {
		return nil, err
	}
	defer old.Close()
	oldPin, err := pinAgentImage(ctx, old)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, prior := range history.Predecessors {
		if prior.GatewayBytes == oldPin.Bytes && prior.GatewayHash == hex.EncodeToString(oldPin.Hash[:]) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, errors.New("existing integration image has no authenticated prior bundle match; use Connect or remove the unknown helper")
	}
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return nil, err
	}
	staged, err := copyAgentImageNamed(ctx, source.Program(), root, party, agentImageName(party)+".installer-"+hex.EncodeToString(installed.Hash[:8])+".tmp")
	if err != nil {
		return nil, err
	}
	defer staged.Close()
	stagedPin, err := pinAgentImage(ctx, staged)
	if err != nil {
		return nil, err
	}
	if stagedPin.Hash != installed.Hash || stagedPin.Bytes != installed.Bytes {
		return nil, errors.New("installer staged source changed")
	}
	if err := errors.Join(old.Close(), staged.Close()); err != nil {
		return nil, err
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	var absenceErr error
	result, err := replaceAgentImage(work, old.Program(), oldPin, staged.Program(), stagedPin, func(boundary string) {
		if boundary == "admitted" {
			absenceErr = requireNoInstallerEnrollment(ctx, application, read)
			if absenceErr != nil {
				cancel()
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("installer previous image retained at %s: %w", result.Backup, errors.Join(absenceErr, err))
	}
	removeSetAsideAgentImages(ctx, root, nil)
	current, err := holdAgentImageForReview(ctx, root, party)
	if err != nil {
		return nil, err
	}
	if err := sameInstalledAgentImage(ctx, current, source); err != nil {
		return nil, errors.Join(err, current.Close())
	}
	return current, nil
}
