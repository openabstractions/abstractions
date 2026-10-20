package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"slices"

	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

var errPairingAgreement = errors.New("pairing: agreement refused")

// This is the closed first runtime meaning of the opaque /2 proposals.
// A field/definition/effect change requires new native consent.
type pairingProposal struct {
	Version      int                   `json:"version"`
	Role         byte                  `json:"role"`
	InitiatorKey string                `json:"initiator_key"`
	ResponderKey string                `json:"responder_key"`
	Request      string                `json:"request"`
	Offer        string                `json:"offer"`
	Host         string                `json:"host"`
	Address      string                `json:"address"`
	BindingName  string                `json:"binding_name"`
	Semantics    string                `json:"semantics"`
	Lifetime     string                `json:"lifetime"`
	Rules        []pairingProposalRule `json:"rules"`
}
type pairingProposalRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Permit   bool   `json:"permit"`
}

func pairingSemantics() (string, error) {
	catalogue, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		return "", err
	}
	var definitions []rwire.PermissionDefinition
	for _, action := range []string{remoteServeAction, inference.ActionComplete, routerservice.ActionInventory} {
		d, ok := grants.PermissionDefinition(action)
		if !ok || !d.Served || d.OwnerContract == "" || d.ResourceProfile != rights.LegacyExactResourceProfile {
			return "", errPairingAgreement
		}
		selector, ok := catalogue.SelectorDefinition(rwire.PermissionKey{Action: action, ResourceProfile: d.ResourceProfile})
		if !ok || len(selector.SupportedSelectors) != 0 {
			return "", errPairingAgreement
		}
		definitions = append(definitions, d)
	}
	data, err := json.Marshal(struct {
		Version     int
		Definitions []rwire.PermissionDefinition
	}{1, definitions})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func pairingRules(host string) ([]pairingProposalRule, error) {
	feature, err := grants.FeatureRules(grants.Chat, grants.For{Hosts: []string{host}})
	if err != nil {
		return nil, errPairingAgreement
	}
	rules := []pairingProposalRule{{remoteServeAction, "account", true}}
	for _, rule := range feature {
		rules = append(rules, pairingProposalRule{rule.Action, rule.Resource, true})
	}
	if len(rules) != 3 {
		return nil, errPairingAgreement
	}
	for _, rule := range rules {
		if !rights.ValidDecisionQuery(rule.Action, rule.Resource) {
			return nil, errPairingAgreement
		}
	}
	return rules, nil
}

func preparePairingProposal(preparation mwire.MachinePreparation, keys remote.PairKeys) ([]byte, error) {
	if keys.Role != 1 && keys.Role != 2 {
		return nil, errPairingAgreement
	}
	semantics, err := pairingSemantics()
	if err != nil {
		return nil, err
	}
	p := pairingProposal{Version: 1, Role: keys.Role, Request: preparation.Request.String(), Offer: preparation.Offer.String(), Host: preparation.OfferHost, Address: preparation.NormalServiceAddress, BindingName: preparation.BindingName, Semantics: semantics, Lifetime: "until_revoked", Rules: []pairingProposalRule{}}
	i, r := keys.LocalKey, keys.PeerKey
	if keys.Role == 2 {
		i, r = r, i
	}
	p.InitiatorKey, p.ResponderKey = hex.EncodeToString(i[:]), hex.EncodeToString(r[:])
	if p.Request != "none" && p.Request != "one_host_inference" || !providerName.MatchString(p.BindingName) {
		return nil, errPairingAgreement
	}
	switch p.Offer {
	case "none":
		if p.Host != "" || p.Address != "" {
			return nil, errPairingAgreement
		}
	case "one_host_inference":
		at, err := netip.ParseAddrPort(p.Address)
		if err != nil || at.String() != p.Address || !validPairingInvitationAddress(at) {
			return nil, errPairingAgreement
		}
		if _, err := remoteListenAddress(p.Address); err != nil {
			return nil, errPairingAgreement
		}
		p.Rules, err = pairingRules(p.Host)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errPairingAgreement
	}
	data, err := json.Marshal(p)
	if err != nil || len(data) > remote.PairingProposalLimit {
		return nil, errPairingAgreement
	}
	return data, nil
}

func decodePairingProposal(data []byte, keys remote.PairKeys, role byte) (pairingProposal, error) {
	var p pairingProposal
	if len(data) > remote.PairingProposalLimit {
		return p, errPairingAgreement
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errPairingAgreement
	}
	request, ok := mwire.ParseMachineScopeKind(p.Request)
	if !ok {
		return p, errPairingAgreement
	}
	offer, ok := mwire.ParseMachineScopeKind(p.Offer)
	if !ok {
		return p, errPairingAgreement
	}
	other := keys
	if role != keys.Role {
		other.LocalKey, other.PeerKey = other.PeerKey, other.LocalKey
		other.Role = role
	}
	expected, err := preparePairingProposal(mwire.MachinePreparation{Request: request, Offer: offer, OfferHost: p.Host, NormalServiceAddress: p.Address, BindingName: p.BindingName}, other)
	if err != nil || !bytes.Equal(data, expected) {
		return p, errPairingAgreement
	}
	return p, nil
}

func pairingAgreement(view remote.PairView) (mwire.MachineAgreement, pairingProposal, pairingProposal, error) {
	keys := remote.PairKeys{LocalKey: view.LocalKey, PeerKey: view.PeerKey, Role: view.Role}
	i, err := decodePairingProposal(view.InitiatorProposal, keys, 1)
	if err != nil {
		return mwire.MachineAgreement{}, i, i, err
	}
	r, err := decodePairingProposal(view.ResponderProposal, keys, 2)
	if err != nil {
		return mwire.MachineAgreement{}, i, r, err
	}
	if i.Request != r.Offer || r.Request != i.Offer {
		return mwire.MachineAgreement{}, i, r, errPairingAgreement
	}
	local, peer := i, r
	if view.Role == 2 {
		local, peer = r, i
	}
	direction := func(p pairingProposal) mwire.MachineDirection {
		kind, _ := mwire.ParseMachineScopeKind(p.Offer)
		return mwire.MachineDirection{Kind: kind, Host: p.Host, ServiceAddress: p.Address}
	}
	return mwire.MachineAgreement{PeerKey: hex.EncodeToString(view.PeerKey[:]), Check: view.Check, Transcript: hex.EncodeToString(view.Transcript[:]), Semantics: local.Semantics, Given: direction(local), Received: direction(peer)}, local, peer, nil
}

func pairingPolicyRules(account, key string, proposal pairingProposal) []rwire.PolicyRule {
	rules := make([]rwire.PolicyRule, 0, len(proposal.Rules))
	for _, rule := range slices.Clone(proposal.Rules) {
		rules = append(rules, rwire.PolicyRule{Subject: rwire.Subject{Account: account, Program: "remote:" + key}, Action: rule.Action, Resource: rule.Resource, Permit: true})
	}
	return rules
}
