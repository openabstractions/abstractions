package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/remote"
)

const pairingStateFile = "pairing/state.json"
const pairingStateLimit = 1 << 20
const pairingPeerLimit = 128

var errPairingState = errors.New("pairing: state unavailable or changed")
var errPairingRevision = errors.New("pairing: stale state revision")
var errPairingBusy = errors.New("pairing: state reconciliation busy")

type pairingPeerRecord struct {
	Certificate        []byte          `json:"certificate"`
	Transcript         [32]byte        `json:"transcript"`
	Label              string          `json:"label"`
	AddressHint        string          `json:"address_hint"`
	State              string          `json:"state"`
	RightsEffect       string          `json:"rights_effect"`
	ActivationKind     string          `json:"activation_kind,omitempty"`
	MinimumRevision    string          `json:"minimum_revision,omitempty"`
	BindingName        string          `json:"binding_name,omitempty"`
	PeerServiceAddress string          `json:"peer_service_address,omitempty"`
	Consent            *pairingConsent `json:"consent,omitempty"`
}

// All authority-bearing bytes are coherent in one owner-private CAS document.
// No file identity/rollback anchor is serialized into this replaceable value.
type pairingDocument struct {
	Version          int                          `json:"version"`
	Account          string                       `json:"account"`
	Revision         uint64                       `json:"revision"`
	PrivateKey       []byte                       `json:"private_key"`
	Certificate      []byte                       `json:"certificate"`
	Peers            map[string]pairingPeerRecord `json:"peers"`
	ReceivingAddress string                       `json:"receiving_address,omitempty"`
	Pending          *pairingConsent              `json:"pending,omitempty"`
}

type runtimePairingState struct {
	mu                       pairingStateLock
	root, path, account      string
	parentID, rootID, fileID fileid.StoredFileID
	digest                   [32]byte
	onChanged                func()
	onUnexpected             func()
	activityMu               sync.Mutex
	activity                 map[string]*pairingActivity
}

// The state has one lock. Reserved operations and authenticated admission wait
// within the caller's lifetime. Snapshot and edit probes retain TryLock.
type pairingStateLock struct {
	once sync.Once
	slot chan struct{}
}

func (m *pairingStateLock) init()   { m.once.Do(func() { m.slot = make(chan struct{}, 1) }) }
func (m *pairingStateLock) Lock()   { m.init(); m.slot <- struct{}{} }
func (m *pairingStateLock) Unlock() { <-m.slot }
func (m *pairingStateLock) TryLock() bool {
	m.init()
	select {
	case m.slot <- struct{}{}:
		return true
	default:
		return false
	}
}
func (m *pairingStateLock) lockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

// current is supplied only by a reserved operation. Recheck it after waking,
// before reading or conditionally changing any state.
func (s *runtimePairingState) acquire(ctx context.Context, current []func() error) error {
	if len(current) == 0 {
		if !s.mu.TryLock() {
			return errPairingBusy
		}
		return nil
	}
	if err := s.mu.lockContext(ctx); err != nil {
		return err
	}
	if err := current[0](); err != nil {
		s.mu.Unlock()
		return err
	}
	return nil
}

// Epoch-local observation only. The trust document remains the admission owner.
type pairingActivity struct {
	transcript [32]byte
	lastCallMs int64
}

func (s *runtimePairingState) unexpectedChange() error {
	s.activityMu.Lock()
	clear(s.activity)
	s.activityMu.Unlock()
	if s.onUnexpected != nil {
		s.onUnexpected() // The runtime callback only signals a bounded worker.
	}
	return errPairingState
}

func (s *runtimePairingState) retainActivity(peers map[string]pairingPeerRecord) {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	for key, activity := range s.activity {
		peer, ok := peers[key]
		if !ok || peer.State != "active" || peer.Transcript != activity.transcript {
			delete(s.activity, key)
		}
	}
}

func (s *runtimePairingState) observeCall(peer remote.Peer) func() {
	key := hex.EncodeToString(peer.Key[:])
	s.activityMu.Lock()
	activity := s.activity[key]
	s.activityMu.Unlock()
	observed := false
	return func() {
		if activity == nil {
			return
		}
		s.activityMu.Lock()
		defer s.activityMu.Unlock()
		if observed {
			return
		}
		observed = true
		if s.activity[key] == activity {
			activity.lastCallMs = time.Now().UnixMilli()
		}
	}
}

func (s *runtimePairingState) lastCall(key string, peer pairingPeerRecord) int64 {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if activity := s.activity[key]; activity != nil && peer.State == "active" && activity.transcript == peer.Transcript {
		return activity.lastCallMs
	}
	return 0
}

// createRuntimePairingState is explicit first creation, never a missing-file
// recovery path. Its future caller must hold the runtime settings lifecycle
// guard. Neither constructor is wired into the default listener/control yet.
func createRuntimePairingState(ctx context.Context, stateRoot string) (*runtimePairingState, error) {
	return openPairingState(ctx, stateRoot, true)
}
func loadRuntimePairingState(ctx context.Context, stateRoot string) (*runtimePairingState, error) {
	return openPairingState(ctx, stateRoot, false)
}

func openPairingState(ctx context.Context, stateRoot string, create bool) (*runtimePairingState, error) {
	if err := resetSafePath(ctx, stateRoot); err != nil {
		return nil, err
	}
	parentID, err := pairingFileIdentity(ctx, stateRoot, true)
	if err != nil {
		return nil, err
	}
	account, err := user.Current()
	if err != nil || account.Uid == "" {
		return nil, errPairingState
	}
	root := filepath.Join(stateRoot, "pairing")
	if err = resetSafePath(ctx, root); err != nil {
		return nil, err
	}
	if create {
		err = privatedir.Ensure(root)
	} else {
		err = privatedir.CheckDir(root)
	}
	if err != nil {
		return nil, err
	}
	s := &runtimePairingState{root: root, path: filepath.Join(root, "state.json"), account: account.Uid, parentID: parentID}
	s.rootID, err = pairingFileIdentity(ctx, root, true)
	if err != nil {
		return nil, err
	}
	var created []byte
	if create {
		key, cert, err := newPairingIdentity()
		if err != nil {
			return nil, err
		}
		doc := pairingDocument{Version: 1, Account: s.account, Revision: 1, PrivateKey: key, Certificate: cert, Peers: map[string]pairingPeerRecord{}}
		data, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		created = data
		err = cas.ChangeLimitContext(ctx, s.path, pairingStateLimit, func(current []byte) ([]byte, error) {
			if current != nil {
				return nil, errPairingState
			}
			if err := s.checkRoot(ctx); err != nil {
				return nil, err
			}
			return data, nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err = s.capture(ctx, created); err != nil {
		return nil, err
	}
	if _, _, err = s.read(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func newPairingIdentity() ([]byte, []byte, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "OA runtime"}, DNSNames: []string{"oa-runtime"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		return nil, nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	return private, der, err
}

func pairingFileIdentity(ctx context.Context, path string, directory bool) (fileid.StoredFileID, error) {
	h, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fileid.StoredFileID{}, err
	}
	defer h.Close()
	id := h.FileID()
	if id.IsDir() != directory || (!directory && id.Links() != 1) {
		return fileid.StoredFileID{}, errPairingState
	}
	if !directory {
		if err := privatedir.CheckFile(path); err != nil {
			return fileid.StoredFileID{}, err
		}
	}
	return id.Stored()
}

func (s *runtimePairingState) checkRoot(ctx context.Context) error {
	if err := resetSafePath(ctx, filepath.Dir(s.root)); err != nil {
		return err
	}
	_, parentState, err := fileid.Revalidate(ctx, s.parentID)
	if err != nil {
		return err
	}
	if parentState != fileid.StateSame {
		return errPairingState
	}
	if err := privatedir.CheckDir(s.root); err != nil {
		return err
	}
	if err := resetSafePath(ctx, s.path); err != nil {
		return err
	}
	_, state, err := fileid.Revalidate(ctx, s.rootID)
	if err != nil {
		return err
	}
	if state != fileid.StateSame {
		return errPairingState
	}
	return nil
}

func (s *runtimePairingState) capture(ctx context.Context, expected []byte) error {
	if err := s.checkRoot(ctx); err != nil {
		return err
	}
	id, err := pairingFileIdentity(ctx, s.path, false)
	if err != nil {
		return err
	}
	data, err := cas.ReadLimitContext(ctx, s.path, pairingStateLimit)
	if err != nil || data == nil {
		return errPairingState
	}
	if expected != nil && !bytes.Equal(data, expected) {
		return errPairingState
	}
	if _, err = decodePairingDocument(data, s.account); err != nil {
		return err
	}
	_, state, err := fileid.Revalidate(ctx, id)
	if err != nil || state != fileid.StateSame {
		return errPairingState
	}
	s.fileID = id
	s.digest = sha256.Sum256(data)
	return nil
}

// read checks the retained in-process object identity and exact bytes both
// before and after the bounded CAS read. Restart trusts the accepted private
// file assumption; it has no independent hostile-same-account rollback anchor.
func (s *runtimePairingState) read(ctx context.Context) (pairingDocument, []byte, error) {
	if err := s.checkRoot(ctx); err != nil {
		if errors.Is(err, errPairingState) {
			s.unexpectedChange()
		}
		return pairingDocument{}, nil, err
	}
	_, state, err := fileid.Revalidate(ctx, s.fileID)
	if err != nil {
		return pairingDocument{}, nil, fmt.Errorf("%w: %w", errPairingState, err)
	}
	if state != fileid.StateSame {
		return pairingDocument{}, nil, s.unexpectedChange()
	}
	if _, err = pairingFileIdentity(ctx, s.path, false); err != nil {
		return pairingDocument{}, nil, err
	}
	data, err := cas.ReadLimitContext(ctx, s.path, pairingStateLimit)
	if err != nil {
		return pairingDocument{}, nil, err
	}
	if sha256.Sum256(data) != s.digest {
		return pairingDocument{}, nil, s.unexpectedChange()
	}
	_, state, err = fileid.Revalidate(ctx, s.fileID)
	if err != nil {
		return pairingDocument{}, nil, errPairingState
	}
	if state != fileid.StateSame {
		return pairingDocument{}, nil, s.unexpectedChange()
	}
	doc, err := decodePairingDocument(data, s.account)
	return doc, data, err
}

func decodePairingDocument(data []byte, account string) (pairingDocument, error) {
	var d pairingDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil {
		return d, errPairingState
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return d, errPairingState
	}
	if d.Version != 1 || d.Account != account || d.Revision == 0 || d.Peers == nil || len(d.Peers) > pairingPeerLimit {
		return d, errPairingState
	}
	ownIdentity, err := pairingIdentity(d)
	if err != nil {
		return d, err
	}
	ownKey := sha256.Sum256(ownIdentity.Leaf.RawSubjectPublicKeyInfo)
	for key, p := range d.Peers {
		cert, err := x509.ParseCertificate(p.Certificate)
		if err != nil {
			return d, errPairingState
		}
		if pairingKey(cert) != key || p.Transcript == ([32]byte{}) || !pairingHint(p.Label) || !pairingHint(p.AddressHint) || len(p.RightsEffect) > 256 {
			return d, errPairingState
		}
		switch p.State {
		case "inactive":
			if p.RightsEffect != "" {
				return d, errPairingState
			}
		case "active":
			if p.RightsEffect == "" && p.ActivationKind != "native_no_grant" {
				return d, errPairingState
			}
		case "removing":
		default:
			return d, errPairingState
		}
		if err := validateStoredPairingPeer(p); err != nil {
			return d, err
		}
		if p.Consent != nil {
			if p.Consent.LocalKey != ownKey {
				return d, errPairingState
			}
		}
	}
	if d.Pending != nil {
		if err := validatePairingConsent(*d.Pending); err != nil {
			return d, err
		}
		if d.Pending.LocalKey != ownKey {
			return d, errPairingState
		}
	}
	if d.ReceivingAddress != "" {
		if !validPairingNormalAddress(d.ReceivingAddress) {
			return d, errPairingState
		}
	}
	return d, nil
}

func pairingIdentity(d pairingDocument) (tls.Certificate, error) {
	key, err := x509.ParsePKCS8PrivateKey(d.PrivateKey)
	if err != nil {
		return tls.Certificate{}, errPairingState
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return tls.Certificate{}, errPairingState
	}
	cert, err := x509.ParseCertificate(d.Certificate)
	if err != nil {
		return tls.Certificate{}, errPairingState
	}
	public, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(public, private.Public().(ed25519.PublicKey)) || cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil || !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) || !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) || cert.VerifyHostname("oa-runtime") != nil || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return tls.Certificate{}, errPairingState
	}
	return tls.Certificate{Certificate: [][]byte{append([]byte{}, d.Certificate...)}, PrivateKey: private, Leaf: cert}, nil
}

func pairingKey(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(h[:])
}
func pairingHint(value string) bool {
	return len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n")
}

func (s *runtimePairingState) identity(ctx context.Context, current ...func() error) (tls.Certificate, uint64, error) {
	if err := s.acquire(ctx, current); err != nil {
		return tls.Certificate{}, 0, err
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return tls.Certificate{}, 0, err
	}
	cert, err := pairingIdentity(d)
	return cert, d.Revision, err
}

func (s *runtimePairingState) snapshot(ctx context.Context, current ...func() error) (uint64, map[string]pairingPeerRecord, error) {
	if err := s.acquire(ctx, current); err != nil {
		return 0, nil, err
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	return d.Revision, d.Peers, err
}

func (s *runtimePairingState) change(ctx context.Context, expected uint64, edit func(*pairingDocument) error, current ...func() error) (uint64, error) {
	if err := s.acquire(ctx, current); err != nil {
		return 0, err
	}
	defer func() {
		s.mu.Unlock()
		if s.onChanged != nil {
			s.onChanged()
		}
	}()
	d, base, err := s.read(ctx)
	if err != nil {
		return 0, err
	}
	if d.Revision != expected {
		return d.Revision, errPairingRevision
	}
	var next []byte
	err = cas.ChangeLimitContext(ctx, s.path, pairingStateLimit, func(current []byte) ([]byte, error) {
		if err := s.checkRoot(ctx); err != nil {
			return nil, err
		}
		_, state, err := fileid.Revalidate(ctx, s.fileID)
		if err != nil || state != fileid.StateSame || !bytes.Equal(current, base) {
			return nil, errPairingState
		}
		before, _ := json.Marshal(d)
		if err = edit(&d); err != nil {
			return nil, err
		}
		// The trusted native/Rights closure may wait. Refuse an observed object
		// or privacy change during that wait even when its bytes stayed equal.
		if err = s.checkRoot(ctx); err != nil {
			return nil, err
		}
		_, state, err = fileid.Revalidate(ctx, s.fileID)
		if err != nil || state != fileid.StateSame {
			return nil, errPairingState
		}
		if _, err = pairingFileIdentity(ctx, s.path, false); err != nil {
			return nil, err
		}
		after, _ := json.Marshal(d)
		if bytes.Equal(before, after) {
			next = base
			return base, nil
		}
		if d.Revision == math.MaxUint64 {
			return nil, errPairingState
		}
		d.Revision++
		next, err = json.Marshal(d)
		if err != nil {
			return nil, err
		}
		if _, err = decodePairingDocument(next, s.account); err != nil {
			return nil, err
		}
		return next, nil
	})
	if err != nil {
		return 0, err
	}
	// Update the external anchor only after our successful CAS, including the
	// exact expected bytes. Failure here keeps the old anchor and denies reads.
	if !bytes.Equal(next, base) {
		if err = s.capture(ctx, next); err != nil {
			return 0, err
		}
	}
	s.retainActivity(d.Peers)
	return d.Revision, nil
}

func (s *runtimePairingState) stage(ctx context.Context, expected uint64, cert *x509.Certificate, transcript [32]byte, label, hint string) (uint64, error) {
	if cert == nil || transcript == ([32]byte{}) || !pairingHint(label) || !pairingHint(hint) {
		return 0, errPairingState
	}
	key := pairingKey(cert)
	return s.change(ctx, expected, func(d *pairingDocument) error {
		local, err := x509.ParseCertificate(d.Certificate)
		if err != nil || pairingKey(local) == key {
			return errPairingState
		}
		if _, exists := d.Peers[key]; exists || len(d.Peers) >= pairingPeerLimit {
			return errPairingState
		}
		d.Peers[key] = pairingPeerRecord{Certificate: append([]byte{}, cert.Raw...), Transcript: transcript, Label: label, AddressHint: hint, State: "inactive"}
		return nil
	})
}

// activate's trusted private closure must recheck native Asks+offer+exact T and
// the actual conditional Rights result. It runs under this owner's CAS lock;
// it must honor ctx, avoid recursive state calls and never infer approval from
// static PairCandidate fields. It returns the observed Rights effect identity.
func (s *runtimePairingState) activate(ctx context.Context, expected uint64, key string, recheck func(context.Context, pairingPeerRecord) (string, error)) (uint64, error) {
	if recheck == nil {
		return 0, errPairingState
	}
	return s.change(ctx, expected, func(d *pairingDocument) error {
		p, ok := d.Peers[key]
		if !ok || p.State != "inactive" {
			return errPairingState
		}
		effect, err := recheck(ctx, p)
		if err != nil {
			return err
		}
		if effect == "" || len(effect) > 256 {
			return errPairingState
		}
		p.State = "active"
		p.RightsEffect = effect
		d.Peers[key] = p
		return nil
	})
}

func (s *runtimePairingState) remove(ctx context.Context, expected uint64, key string, authorize func() error) (uint64, error) {
	return s.change(ctx, expected, func(d *pairingDocument) error {
		if authorize == nil {
			return errPairingState
		}
		if err := authorize(); err != nil {
			return err
		}
		p, ok := d.Peers[key]
		if !ok {
			return errPairingState
		}
		p.State = "removing"
		d.Peers[key] = p
		return nil
	})
}

// finishRemoval reconciles the existing Rights owner's all-subject cleanup.
// Failed/unknown cleanup leaves a disabled removing record, blocking re-pair.
func (s *runtimePairingState) finishRemoval(ctx context.Context, expected uint64, key string, recheck func(context.Context, pairingPeerRecord) error) (uint64, error) {
	if recheck == nil {
		return 0, errPairingState
	}
	return s.change(ctx, expected, func(d *pairingDocument) error {
		p, ok := d.Peers[key]
		if !ok || p.State != "removing" {
			return errPairingState
		}
		if err := recheck(ctx, p); err != nil {
			return err
		}
		delete(d.Peers, key)
		active := false
		for _, peer := range d.Peers {
			active = active || peer.State == "active"
		}
		if !active {
			d.ReceivingAddress = ""
		}
		return nil
	})
}

func pairingAdmissionKey(peer remote.Peer) (string, error) {
	if peer.Certificate == nil {
		return "", fmt.Errorf("pairing admission peer: %w", errPairingState)
	}
	key := pairingKey(peer.Certificate)
	if hex.EncodeToString(peer.Key[:]) != key {
		return "", fmt.Errorf("pairing admission peer: %w", errPairingState)
	}
	return key, nil
}

func (s *runtimePairingState) admitContext(ctx context.Context, peer remote.Peer) error {
	key, err := pairingAdmissionKey(peer)
	if err != nil {
		return err
	}
	if err := s.mu.lockContext(ctx); err != nil {
		return fmt.Errorf("pairing admission lock: %w: %w", errPairingBusy, err)
	}
	defer s.mu.Unlock()
	return s.admitRead(ctx, key)
}

// probeStanding observes existing work's authority without waiting behind an
// edit whose own synchronous recheck may be calling us. Busy is uncertainty,
// not withdrawal (INF-A6). New TLS/frame admissions use admitContext and wait.
func (s *runtimePairingState) probeStanding(ctx context.Context, peer remote.Peer) error {
	key, err := pairingAdmissionKey(peer)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("pairing standing caller: %w: %w", errPairingBusy, err)
	}
	if !s.mu.TryLock() {
		return fmt.Errorf("pairing standing lock: %w", errPairingBusy)
	}
	defer s.mu.Unlock()
	err = s.admitRead(ctx, key)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("pairing standing read: %w: %w", errPairingBusy, err)
	}
	return err
}

// admitRead requires the native state lock. Both admission owners retain the
// same certificate, active-peer and exact native metadata checks.
func (s *runtimePairingState) admitRead(ctx context.Context, key string) error {
	d, _, err := s.read(ctx)
	if err != nil {
		return fmt.Errorf("pairing admission read: %w", err)
	}
	p, ok := d.Peers[key]
	if !ok || p.State != "active" {
		return fmt.Errorf("pairing admission state: %w", errPairingState)
	}
	s.activityMu.Lock()
	if s.activity == nil {
		s.activity = make(map[string]*pairingActivity)
	}
	if old := s.activity[key]; old == nil || old.transcript != p.Transcript {
		s.activity[key] = &pairingActivity{transcript: p.Transcript}
	}
	s.activityMu.Unlock()
	return nil
}
