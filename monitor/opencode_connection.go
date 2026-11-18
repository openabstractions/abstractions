package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type installedAgentConnection struct {
	party setup.AgentParty
	title string
}

func installedConnection() installedAgentConnection {
	party := product.InstalledAgentConnectionParty()
	return installedAgentConnection{party: setup.AgentParty(party), title: product.AgentConnectionTitle(party)}
}

type installedAgentConnectionView struct {
	Supported        bool     `json:"supported"`
	Outcome          string   `json:"outcome"`
	Revision         string   `json:"revision"`
	Registration     string   `json:"registration"`
	Owned            bool     `json:"owned"`
	HelperState      string   `json:"helper_state"`
	RestartHosts     []string `json:"restart_hosts,omitempty"`
	Failure          string   `json:"update_failure,omitempty"`
	Available        bool     `json:"available"`
	HostRegistration string   `json:"host_registration"`
	HelperEnrollment string   `json:"helper_enrollment"`
	HostProgram      string   `json:"host_program"`
	HelperProgram    string   `json:"helper_program"`
	Reason           string   `json:"reason,omitempty"`
	// Guidance is the page's plain sentence for a failure; Reason is the
	// error for Technical details.
	Guidance   string `json:"guidance,omitempty"`
	hostDigest string
}

// This private composition grants no stock-host enrollment, answering or peer
// rights. Register owns stock metadata; Applications owns the helper's Keep.
type installedAgentConnectionOwner struct {
	connection   installedAgentConnection
	descriptor   fwire.ApplicationDescriptor
	host         claudePanelImage
	helper       installerIntegrationImage
	native       claudeEnrollmentClient
	readHost     func(context.Context) (*fwire.ApplicationDescriptor, error)
	register     func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error)
	registerHost func(context.Context, fwire.ApplicationDescriptor, string) error
	admit        func(context.Context, claudePanelImage) (claudePanelImage, error)
	currency     func(context.Context) agentHelperReading
}

func (o *installedAgentConnectionOwner) close() error {
	var helper error
	if o.helper != nil {
		helper = o.helper.Close()
	}
	return errors.Join(helper, o.host.Close())
}

func (o *installedAgentConnectionOwner) read(ctx context.Context) (installedAgentConnectionView, error) {
	v := installedAgentConnectionView{Supported: true, Outcome: "page", Available: true,
		Registration: "absent", HostRegistration: "absent", HelperEnrollment: "none",
		HelperState: "absent", HostProgram: o.host.Program()}
	if err := o.host.Check(ctx); err != nil {
		return v, err
	}
	registered, err := o.readHost(ctx)
	if err != nil {
		return v, err
	}
	hostDigest := applicationDigest(registered)
	v.hostDigest = hostDigest
	if registered != nil {
		v.HostRegistration = "conflict"
		if hostDigest == applicationDigest(&o.descriptor) {
			v.HostRegistration = "registered"
		}
	}
	name := nativecli.AgentApplicationForParty(string(o.connection.party))
	if o.helper != nil {
		if err := o.helper.Check(ctx); err != nil {
			return v, err
		}
		v.HelperProgram, v.HelperState = o.helper.Program(), "current"
		name = agentImageApplication(o.helper, name)
	}
	result, err := o.native.ReadAgentEnrollment(ctx, name)
	if err != nil {
		return v, err
	}
	enrollmentRevision := ""
	switch result.Outcome {
	case applications.OutcomeUnknown:
	case applications.OutcomePage:
		if result.Enrollment == nil {
			return v, fmt.Errorf("%s helper enrollment is unavailable", o.connection.title)
		}
		entry := result.Enrollment
		enrollmentRevision = entry.Revision
		v.HelperEnrollment = entry.State.String()
		if entry.State == applications.AgentEnrollmentStateEnrolled {
			if o.helper == nil {
				return v, fmt.Errorf("%s helper image is unavailable", o.connection.title)
			}
			if !agentEnrollmentMatchesImage(ctx, entry.Program, o.helper) {
				return v, fmt.Errorf("%s helper enrollment names a different image", o.connection.title)
			}
			if o.currency != nil {
				reading := o.currency(ctx)
				v.HelperState, v.RestartHosts, v.Failure = reading.State, reading.Restart, reading.Failure
			}
		}
	default:
		return v, fmt.Errorf("%s helper enrollment could not be read", o.connection.title)
	}
	// Physical helper creation supplies no saved authority and changes no view
	// revision until its native registration/enrollment changes.
	currency := ""
	if v.HelperEnrollment == "enrolled" {
		currency = v.HelperState
	}
	raw, _ := json.Marshal([]string{o.host.FileKey(), hostDigest, enrollmentRevision, v.HelperEnrollment, currency})
	sum := sha256.Sum256(raw)
	v.Revision = hex.EncodeToString(sum[:])
	if v.HostRegistration == "registered" && v.HelperEnrollment == "enrolled" && v.HelperState == "current" {
		v.Registration, v.Owned = "saved", true
	}
	return v, nil
}

func (o *installedAgentConnectionOwner) connect(ctx context.Context, expected string) (installedAgentConnectionView, error) {
	v, err := o.read(ctx)
	if err != nil {
		return v, err
	}
	if expected != "" && expected != v.Revision {
		v.Outcome = "conflict"
		return v, fmt.Errorf("%s connection changed; check it again", o.connection.title)
	}
	if v.Registration == "saved" {
		return v, nil
	}
	if v.HostRegistration == "conflict" && expected == "" {
		v.Outcome = "conflict"
		return v, fmt.Errorf("%s registration differs; the existing entry was preserved", o.connection.title)
	}
	if o.helper == nil {
		return v, fmt.Errorf("%s helper image is unavailable", o.connection.title)
	}
	state, err := registerPreparedAgentIntegration(ctx, o.connection, "", o.helper, o.native, o.register, o.admit, true)
	if err != nil {
		return v, err
	}
	if state != "enrolled" {
		v.Outcome, v.HelperEnrollment = "connect_pending", state
		return v, nil
	}
	if err := o.host.Check(ctx); err != nil {
		return v, err
	}
	// Keep precedes stock metadata creation or the person's explicit replacement
	// of the shown entry. Registration grants no stock or messaging rights.
	if v.HostRegistration != "registered" {
		if err := o.registerHost(ctx, o.descriptor, v.hostDigest); err != nil {
			var unknown applicationUnknown
			if errors.As(err, &unknown) {
				v.Outcome = "unknown"
			}
			return v, err
		}
	}
	return o.read(ctx)
}

func installedAgentConnectionOwnerFor(ctx context.Context, prepare bool) (*installedAgentConnectionOwner, error) {
	connection := installedConnection()
	descriptor, host, err := holdInstalledConnectionHost(ctx, connection)
	if err != nil {
		return nil, err
	}
	o := &installedAgentConnectionOwner{connection: connection, descriptor: descriptor, host: host}
	var image *setup.AgentImage
	if prepare {
		image, err = prepareInstalledAgentInstallerImage(ctx, connection.party)
	} else {
		image, err = setup.HoldAgentImageForReview(ctx, connection.party)
	}
	if err != nil && !(errors.Is(err, os.ErrNotExist) && !prepare) {
		return nil, errors.Join(err, o.close())
	}
	if image != nil {
		o.helper = image
	}
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, errors.Join(err, o.close())
	}
	o.native = claudeContextEnrollmentClient{
		read: func(ctx context.Context, name string) (applications.AgentEnrollmentResult, error) {
			return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadAgentEnrollment(name)
		}, set: applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).SetAgentEnrollment}
	p := &servicePanel{}
	o.readHost = func(ctx context.Context) (*fwire.ApplicationDescriptor, error) {
		apps, err := p.setupApplicationsForPanel(ctx)
		if err != nil {
			return nil, err
		}
		entry, readable := registeredApplication(ctx, apps, o.descriptor.Name)
		if !readable {
			return nil, fmt.Errorf("%s registration could not be read", o.connection.title)
		}
		return entry, nil
	}
	o.register = func(ctx context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
		directory, err := panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return fwire.ApplicationChange{}, err
		}
		return directory.Register(ctx, d)
	}
	o.registerHost = func(ctx context.Context, d fwire.ApplicationDescriptor, shown string) error {
		_, err := p.registerApplication(ctx, d, shown)
		return err
	}
	o.admit = func(ctx context.Context, image claudePanelImage) (claudePanelImage, error) {
		return admitAgentImageForConnection(ctx, image, connection.party)
	}
	o.currency = func(ctx context.Context) agentHelperReading {
		if o.helper == nil {
			return agentHelperReading{State: "absent"}
		}
		return panelAgentHelperReading(ctx, connection.party, o.helper.Program(), o.helper.FileKey())
	}
	return o, nil
}

func readInstalledAgentConnection(ctx context.Context) (view installedAgentConnectionView, err error) {
	o, err := installedAgentConnectionOwnerFor(ctx, false)
	if err != nil {
		return installedAgentConnectionView{Outcome: "unavailable"}, err
	}
	defer func() { err = errors.Join(err, o.close()) }()
	return o.read(ctx)
}

func connectInstalledAgentConnection(ctx context.Context, expectedRevision string) (view installedAgentConnectionView, err error) {
	if expectedRevision != "" {
		current, err := readInstalledAgentConnection(ctx)
		if err != nil {
			return current, err
		}
		if current.Revision != expectedRevision {
			current.Outcome = "conflict"
			return current, fmt.Errorf("%s connection changed; check it again", installedConnection().title)
		}
	}
	o, err := installedAgentConnectionOwnerFor(ctx, true)
	if err != nil {
		return installedAgentConnectionView{Outcome: "unavailable"}, err
	}
	defer func() { err = errors.Join(err, o.close()) }()
	return o.connect(ctx, expectedRevision)
}
