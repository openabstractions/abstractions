package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func openCodePairFixture(t *testing.T, kept bool) (*installedAgentConnectionOwner, *int, *int, *applications.AgentEnrollment) {
	t.Helper()
	root := t.TempDir()
	image := func(name string) *preparedIntegrationFixture {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("inert image"), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := fileid.Resolve(t.Context(), fileid.Self, p, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Fatal(err)
		}
		return &preparedIntegrationFixture{program: p, key: id.Key()}
	}
	host, helper := image("OpenCode.exe"), image("oa-opencode-integration.exe")
	descriptor := fwire.ApplicationDescriptor{Name: "opencode", Title: "OpenCode with OA", Program: host.Program()}
	var registered *fwire.ApplicationDescriptor
	sets, registrations := 0, 0
	entry := &applications.AgentEnrollment{Application: "oa-opencode-integration", Program: helper.Program(), Revision: "helper-r1", State: applications.AgentEnrollmentStateNone}
	if kept {
		entry.State = applications.AgentEnrollmentStateEnrolled
	}
	o := &installedAgentConnectionOwner{connection: installedConnection(), descriptor: descriptor, host: host, helper: helper}
	o.native = claudeContextEnrollmentClient{
		read: func(_ context.Context, name string) (applications.AgentEnrollmentResult, error) {
			if name != entry.Application {
				t.Fatalf("stock host must receive no enrollment: %s", name)
			}
			copy := *entry
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &copy}, nil
		}, set: func(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
			if r.Application != entry.Application || r.ExpectedRevision != entry.Revision || !r.Enabled {
				t.Fatal(r)
			}
			sets++
			entry.State = applications.AgentEnrollmentStateEnrolled
			copy := *entry
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &copy}, nil
		}}
	o.readHost = func(context.Context) (*fwire.ApplicationDescriptor, error) { return registered, nil }
	o.registerHost = func(_ context.Context, d fwire.ApplicationDescriptor, shown string) error {
		if shown != "" || registered != nil || entry.State != applications.AgentEnrollmentStateEnrolled {
			t.Fatal("stock registration preceded Keep")
		}
		registrations++
		copy := d
		registered = &copy
		return nil
	}
	o.register = func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
		t.Fatal("helper already registered")
		return fwire.ApplicationChange{}, nil
	}
	o.admit = func(_ context.Context, image claudePanelImage) (claudePanelImage, error) { return image, nil }
	return o, &sets, &registrations, entry
}

func TestOpenCodeCompositeConnectKeepsOneHelperAndRegistersOnlyStockMetadata(t *testing.T) {
	for _, kept := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy-helper-kept-stock-absent"}[kept], func(t *testing.T) {
			o, sets, registrations, _ := openCodePairFixture(t, kept)
			before, err := o.read(t.Context())
			if err != nil || before.Registration == "saved" {
				t.Fatal(before, err)
			}
			connected, err := o.connect(t.Context(), before.Revision)
			if err != nil || connected.Registration != "saved" || connected.HostRegistration != "registered" || connected.HelperEnrollment != "enrolled" {
				t.Fatal(connected, err)
			}
			var output bytes.Buffer
			if err := writeInstalledAgentConnectionResult(&output, connected); err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["outcome"] != "connected" || result["helperProgram"] != o.helper.Program() {
				t.Fatal("ready result lost the held helper image", result, err)
			}
			want := 1
			if kept {
				want = 0
			}
			if *sets != want || *registrations != 1 {
				t.Fatal(*sets, *registrations)
			}
			if _, err := o.connect(t.Context(), connected.Revision); err != nil {
				t.Fatal(err)
			}
			if *sets != want || *registrations != 1 {
				t.Fatal("connected pair was changed or prompted again", *sets, *registrations)
			}
		})
	}
}

func TestOpenCodeCompositeRevisionIgnoresUnenrolledHelperMaterialization(t *testing.T) {
	o, _, _, _ := openCodePairFixture(t, false)
	helper := o.helper
	o.helper = nil
	before, err := o.read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	o.helper = helper
	after, err := o.read(t.Context())
	if err != nil || before.Revision != after.Revision {
		t.Fatal(before, after, err)
	}
}

func TestOpenCodeExplicitConnectionReplacesOnlyTheReviewedEntryAfterKeep(t *testing.T) {
	for _, mode := range []string{"replace", "stale", "implicit", "keep-refused", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			o, sets, _, _ := openCodePairFixture(t, false)
			old := fwire.ApplicationDescriptor{Name: "opencode", Program: "person-owned", Title: "Person's OpenCode"}
			apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"opencode": old}}
			p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}, appRequests: applicationRequestsFor(t, nil)}
			o.readHost = func(context.Context) (*fwire.ApplicationDescriptor, error) {
				copy := apps.entries["opencode"]
				return &copy, nil
			}
			calls := 0
			o.registerHost = func(ctx context.Context, d fwire.ApplicationDescriptor, shown string) error {
				calls++
				if *sets != 1 || shown != applicationDigest(&old) {
					t.Fatal("replacement lost reviewed entry or Keep", shown, *sets)
				}
				if mode == "unknown" {
					return applicationUnknown("read failed")
				}
				_, err := p.registerApplication(ctx, d, shown)
				return err
			}
			before, err := o.read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			expected := before.Revision
			if mode == "stale" {
				expected = "stale"
			}
			if mode == "implicit" {
				expected = ""
			}
			if mode == "keep-refused" {
				o.admit = func(context.Context, claudePanelImage) (claudePanelImage, error) { return nil, errors.New("Not now") }
			}
			got, err := o.connect(t.Context(), expected)
			if mode == "replace" {
				if err != nil || got.Registration != "saved" || calls != 1 || apps.removes != 1 || apps.registers != 1 {
					t.Fatal(got, err, calls)
				}
			} else {
				if err == nil || apps.entries["opencode"] != old || apps.removes != 0 || apps.registers != 0 {
					t.Fatal("unapproved entry changed", got, err, apps.entries)
				}
				if mode == "unknown" {
					if got.Outcome != "unknown" || calls != 1 {
						t.Fatal(got, calls)
					}
				} else if calls != 0 {
					t.Fatal("unapproved replacement", calls)
				}
			}
		})
	}
}

func TestOpenCodeCompositeConnectRefusesStaleForeignChangedAndRevokedProof(t *testing.T) {
	for _, kind := range []string{"stale", "foreign", "changed-stock", "changed-helper", "revoked-after-register", "register-refused"} {
		t.Run(kind, func(t *testing.T) {
			o, sets, registrations, entry := openCodePairFixture(t, false)
			before, err := o.read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			expected := before.Revision
			switch kind {
			case "stale":
				expected = "stale"
			case "foreign":
				o.readHost = func(context.Context) (*fwire.ApplicationDescriptor, error) {
					return &fwire.ApplicationDescriptor{Name: "opencode", Program: "foreign"}, nil
				}
				expected = ""
			case "changed-stock":
				o.admit = func(_ context.Context, image claudePanelImage) (claudePanelImage, error) {
					o.host.(*preparedIntegrationFixture).err = errors.New("changed")
					return image, nil
				}
			case "changed-helper":
				o.helper.(*preparedIntegrationFixture).key = "changed"
			case "revoked-after-register":
				original := o.registerHost
				o.registerHost = func(ctx context.Context, d fwire.ApplicationDescriptor, s string) error {
					err := original(ctx, d, s)
					entry.State = applications.AgentEnrollmentStateNone
					return err
				}
			case "register-refused":
				o.registerHost = func(context.Context, fwire.ApplicationDescriptor, string) error {
					return errors.New("Register refused")
				}
			}
			view, err := o.connect(t.Context(), expected)
			if err == nil && view.Registration == "saved" {
				t.Fatal("unproven pair reported ready", kind, view)
			}
			var output bytes.Buffer
			if err := writeInstalledAgentConnectionResult(&output, view); err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["outcome"] == "connected" {
				t.Fatal(result)
			}
			if _, exposed := result["helperProgram"]; exposed {
				t.Fatal("unready native connection exposed a launch path", result)
			}
			if (kind == "stale" || kind == "foreign") && (*sets != 0 || *registrations != 0) {
				t.Fatal("refusal mutated connection", *sets, *registrations)
			}
		})
	}
}

// Owner R14, 2026-11-09: after an OA upgrade the installed connector differs
// from the enrolled helper. OpenCode keeps using that kept image with no
// question; only a Keep question that actually waits reports connect_pending.
func TestOpenCodeConnectionResultKeepsUpgradedHelperConnected(t *testing.T) {
	o, _, _, _ := openCodePairFixture(t, true)
	for _, state := range []string{"current", "update", "restart", "unknown"} {
		o.currency = func(context.Context) agentHelperReading { return agentHelperReading{State: state} }
		before, err := o.read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// The stock host registration is the remaining proof besides the helper.
		before.HostRegistration = "registered"
		var output bytes.Buffer
		if err := writeInstalledAgentConnectionResult(&output, before); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if state == "unknown" {
			if result["outcome"] != "not_connected" || result["helperProgram"] != nil {
				t.Fatal("unproven helper reported ready", result)
			}
			continue
		}
		if result["outcome"] != "connected" || result["helperProgram"] != o.helper.Program() {
			t.Fatal("kept helper disconnected by an available update", state, result)
		}
	}
	for _, view := range []struct {
		view    installedAgentConnectionView
		outcome string
	}{
		{installedAgentConnectionView{Outcome: "connect_pending", HelperEnrollment: "pending"}, "connect_pending"},
		{installedAgentConnectionView{Outcome: "page", HelperEnrollment: "pending"}, "connect_pending"},
		{installedAgentConnectionView{Outcome: "unavailable"}, "not_connected"},
		{installedAgentConnectionView{Outcome: "page", HelperEnrollment: "none"}, "not_connected"},
	} {
		var output bytes.Buffer
		if err := writeInstalledAgentConnectionResult(&output, view.view); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["outcome"] != view.outcome {
			t.Fatal(view, result, err)
		}
	}
}
