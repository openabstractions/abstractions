package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"

	"github.com/openabstractions/abstraction-identity/remote"
)

// Native occurrence correlation lives with the machine identity and pins.
// Serialized values are data: activation still requires the actual Book pin.
type pairingConsent struct {
	Operation         string   `json:"operation"`
	Question          string   `json:"question"`
	Scope             string   `json:"scope"`
	Program           string   `json:"program"`
	TextHash          string   `json:"text_hash"`
	ExpectedRights    string   `json:"expected_rights"`
	Role              byte     `json:"role"`
	LocalKey          [32]byte `json:"local_key"`
	PeerKey           [32]byte `json:"peer_key"`
	Transcript        [32]byte `json:"transcript"`
	Check             string   `json:"check"`
	Initiator         []byte   `json:"initiator"`
	Responder         []byte   `json:"responder"`
	NativeYes         bool     `json:"native_yes"`
	TransportComplete bool     `json:"transport_complete"`
}

func validPairingNormalAddress(address string) bool {
	at, err := remoteListenAddress(address)
	return err == nil && at.String() == address && validPairingInvitationAddress(at)
}
func (p pairingConsent) view() remote.PairView {
	return remote.PairView{Role: p.Role, LocalKey: p.LocalKey, PeerKey: p.PeerKey, Transcript: p.Transcript, Check: p.Check, InitiatorProposal: bytes.Clone(p.Initiator), ResponderProposal: bytes.Clone(p.Responder)}
}
func validatePairingConsent(p pairingConsent) error {
	if p.Operation == "" || len(p.Operation) > 80 || p.Question == "" || len(p.Question) > 128 || !pairingHint(p.Scope) || p.Scope == "" || !pairingHint(p.Program) || p.Program == "" || len(p.TextHash) != 64 || p.ExpectedRights == "" || len(p.ExpectedRights) > 128 || p.Transcript == ([32]byte{}) || len(p.Check) != 6 || p.TransportComplete && !p.NativeYes {
		return errPairingState
	}
	if _, _, _, err := pairingAgreement(p.view()); err != nil {
		return errPairingState
	}
	return nil
}
func validateStoredPairingPeer(p pairingPeerRecord) error {
	if p.Consent == nil {
		// Earlier private source fixtures retain their explicitly tested shape.
		if p.ActivationKind != "" || p.BindingName != "" || p.PeerServiceAddress != "" || p.MinimumRevision != "" {
			return errPairingState
		}
		return nil
	}
	if validatePairingConsent(*p.Consent) != nil || !p.Consent.NativeYes || !p.Consent.TransportComplete || p.Consent.Transcript != p.Transcript || !providerName.MatchString(p.BindingName) {
		return errPairingState
	}
	cert, err := x509.ParseCertificate(p.Certificate)
	if err != nil || sha256.Sum256(cert.RawSubjectPublicKeyInfo) != p.Consent.PeerKey {
		return errPairingState
	}
	_, local, peer, err := pairingAgreement(p.Consent.view())
	if err != nil || p.BindingName != local.BindingName || p.PeerServiceAddress != peer.Address {
		return errPairingState
	}
	if p.State == "active" {
		switch p.ActivationKind {
		case "native_no_grant":
			if len(local.Rules) != 0 || p.RightsEffect != "" || p.MinimumRevision != "" {
				return errPairingState
			}
		case "rights_applied":
			if len(local.Rules) == 0 || p.RightsEffect != p.Consent.Question || p.MinimumRevision == "" {
				return errPairingState
			}
		default:
			return errPairingState
		}
	}
	return nil
}

func (s *runtimePairingState) prepareConsent(ctx context.Context, expected uint64, consent pairingConsent, current ...func() error) (uint64, error) {
	if validatePairingConsent(consent) != nil || consent.NativeYes || consent.TransportComplete {
		return 0, errPairingState
	}
	return s.change(ctx, expected, func(d *pairingDocument) error {
		if d.Pending != nil && d.Pending.NativeYes {
			return errPairingState
		}
		for _, p := range d.Peers {
			if p.Consent != nil && p.Consent.PeerKey == consent.PeerKey {
				return errPairingState
			}
		}
		copy := consent
		copy.Initiator = bytes.Clone(copy.Initiator)
		copy.Responder = bytes.Clone(copy.Responder)
		d.Pending = &copy
		return nil
	}, current...)
}
func (s *runtimePairingState) consentYes(ctx context.Context, expected uint64, exact pairingConsent, current ...func() error) (uint64, error) {
	return s.change(ctx, expected, func(d *pairingDocument) error {
		if d.Pending == nil || !samePairingConsent(*d.Pending, exact) || d.Pending.TransportComplete {
			return errPairingState
		}
		d.Pending.NativeYes = true
		return nil
	}, current...)
}
func samePairingConsent(a, b pairingConsent) bool {
	return a.Operation == b.Operation && a.Question == b.Question && a.Scope == b.Scope && a.Program == b.Program && a.TextHash == b.TextHash && a.ExpectedRights == b.ExpectedRights && a.Role == b.Role && a.LocalKey == b.LocalKey && a.PeerKey == b.PeerKey && a.Transcript == b.Transcript && a.Check == b.Check && bytes.Equal(a.Initiator, b.Initiator) && bytes.Equal(a.Responder, b.Responder)
}
func (s *runtimePairingState) stageCompleted(ctx context.Context, expected uint64, candidate remote.PairCandidate, exact pairingConsent, current ...func() error) (uint64, error) {
	if candidate.Certificate == nil || candidate.View.Transcript != exact.Transcript {
		return 0, errPairingState
	}
	key := pairingKey(candidate.Certificate)
	return s.change(ctx, expected, func(d *pairingDocument) error {
		if d.Pending == nil || !d.Pending.NativeYes || !samePairingConsent(*d.Pending, exact) || key != hex.EncodeToString(exact.PeerKey[:]) || len(d.Peers) >= pairingPeerLimit {
			return errPairingState
		}
		if _, ok := d.Peers[key]; ok {
			return errPairingState
		}
		_, local, peer, err := pairingAgreement(exact.view())
		if err != nil {
			return err
		}
		for _, old := range d.Peers {
			if old.BindingName == local.BindingName {
				return errPairingState
			}
		}
		consent := *d.Pending
		consent.TransportComplete = true
		d.Peers[key] = pairingPeerRecord{Certificate: bytes.Clone(candidate.Certificate.Raw), Transcript: exact.Transcript, State: "inactive", BindingName: local.BindingName, PeerServiceAddress: peer.Address, Consent: &consent}
		d.Pending = nil
		return nil
	}, current...)
}

type pairingActivation struct{ Kind, Receipt, Revision string }

func (s *runtimePairingState) activateConsent(ctx context.Context, expected uint64, key string, recheck func(context.Context, pairingPeerRecord) (pairingActivation, error), current ...func() error) (uint64, error) {
	if recheck == nil {
		return 0, errPairingState
	}
	return s.change(ctx, expected, func(d *pairingDocument) error {
		p, ok := d.Peers[key]
		if !ok || p.State != "inactive" || p.Consent == nil {
			return errPairingState
		}
		_, local, _, err := pairingAgreement(p.Consent.view())
		if err != nil {
			return err
		}
		if local.Address != "" && d.ReceivingAddress != "" && local.Address != d.ReceivingAddress {
			return errPairingState
		}
		evidence, err := recheck(ctx, p)
		if err != nil {
			return err
		}
		p.State, p.ActivationKind, p.RightsEffect, p.MinimumRevision = "active", evidence.Kind, evidence.Receipt, evidence.Revision
		if validateStoredPairingPeer(p) != nil {
			return errPairingState
		}
		d.Peers[key] = p
		if local.Address != "" {
			d.ReceivingAddress = local.Address
		}
		return nil
	}, current...)
}

// No TLS callback can mutate trust configuration. A busy or invalid owner
// refuses this handshake; the remaining handshake context bounds the read.
func (s *runtimePairingState) clientRoots(ctx context.Context) (*x509.CertPool, error) {
	if !s.mu.TryLock() {
		return nil, errPairingBusy
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	for _, p := range d.Peers {
		if p.State != "active" {
			continue
		}
		cert, err := x509.ParseCertificate(p.Certificate)
		if err != nil {
			return nil, err
		}
		roots.AddCert(cert)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return roots, nil
}
func (s *runtimePairingState) outgoingTLS(ctx context.Context, key string) (*tls.Config, string, error) {
	if !s.mu.TryLock() {
		return nil, "", errPairingBusy
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return nil, "", err
	}
	p, ok := d.Peers[key]
	if !ok || p.State != "active" || p.PeerServiceAddress == "" {
		return nil, "", errPairingState
	}
	cert, err := pairingIdentity(d)
	if err != nil {
		return nil, "", err
	}
	peer, err := x509.ParseCertificate(p.Certificate)
	if err != nil {
		return nil, "", err
	}
	roots := x509.NewCertPool()
	roots.AddCert(peer)
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: "oa-runtime"}, p.PeerServiceAddress, nil
}
func (s *runtimePairingState) minimum(ctx context.Context, peer remote.Peer) (string, error) {
	if err := s.admit(peer); err != nil {
		return "", err
	}
	if !s.mu.TryLock() {
		return "", errPairingBusy
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return "", err
	}
	p, ok := d.Peers[hex.EncodeToString(peer.Key[:])]
	if !ok || p.State != "active" {
		return "", errPairingState
	}
	return p.MinimumRevision, nil
}
func consentTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
