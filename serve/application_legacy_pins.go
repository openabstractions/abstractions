package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"reflect"

	rawcas "github.com/openabstractions/abstraction-cas/go"
	cas "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// An applications@2 entry names its program by path only. Its continuity
// owner is captured beside it, as applications@3 captures one in each
// manifest file: the trusted signer of the program, or the SHA-256 and size
// of its unsigned image. A registration under a taken name changes the entry
// silently only when the incoming program carries the recorded signer or the
// recorded image pin (sameApplicationV3Continuity); any other program is a
// different identity, meets conflict and leaves the entry untouched. A
// path-only entry recorded before pins existed gets its pin on the first
// registration that adopts it; until then a change to its start arguments or
// activation needs the person's Connect in Apps (VISION 2026-10-06, amended
// 2026-11-10). OA does not defend against code already running as the person;
// it never lets such code silently rewrite an entry OA will later start or
// name.

// applicationPinsFile holds each applications@2 entry's pin by name.
const applicationPinsFile = "application-pins.json"

// applicationLegacyPin is one entry's continuity owner: Code and Proof as the
// platform verified the program, and for an unsigned program its image.
type applicationLegacyPin struct {
	Program   string         `json:"program"`
	Code      identity.Code  `json:"code"`
	Proof     identity.Proof `json:"proof"`
	Image     string         `json:"image_sha256,omitempty"`
	ImageSize int64          `json:"image_size,omitempty"`
}

// file is the pin in the shape applications@3 compares, so one function owns
// the continuity rule for both stores.
func (p applicationLegacyPin) file() applicationV3ManifestFile {
	return applicationV3ManifestFile{Source: applicationV3Source{Program: p.Program, Code: p.Code, Proof: p.Proof}, Image: p.Image, ImageSize: p.ImageSize}
}

// valid requires exactly one continuity owner, as applicationV3ManifestFile.
func (p applicationLegacyPin) valid() bool {
	if !identity.ValidSubjectProgram(p.Program) {
		return false
	}
	if p.file().Source.signed() {
		return p.Image == "" && p.ImageSize == 0
	}
	return applicationImageUnsigned(p.Code, p.Proof) && applicationImageDigest(p.Image) && p.ImageSize > 0
}

// sameApplicationLegacyContinuity reports that incoming carries recorded's
// signer or image pin.
func sameApplicationLegacyContinuity(recorded, incoming applicationLegacyPin) bool {
	return recorded.valid() && incoming.valid() && sameApplicationV3Continuity(recorded.file(), incoming.file())
}

// applicationLegacyImagePin reads program's continuity owner from one held,
// write-excluding open of its image: the trusted signer, or the unsigned
// image's SHA-256 and size. A platform that cannot hold the image pins
// nothing.
func applicationLegacyImagePin(ctx context.Context, program string) (_ applicationLegacyPin, err error) {
	held, err := openHeldApplicationImage(ctx, fileid.Self, program)
	if err != nil {
		return applicationLegacyPin{}, err
	}
	defer func() { err = errors.Join(err, held.Close()) }()
	code, proof, err := identity.VerifyFile(applicationV3HeldProgram(held), nil)
	if err != nil {
		return applicationLegacyPin{}, err
	}
	pin := applicationLegacyPin{Program: identity.NormalizeSubjectProgram(program), Code: code, Proof: proof}
	if !pin.file().Source.signed() {
		if !applicationImageUnsigned(code, proof) {
			return applicationLegacyPin{}, errors.New("applications: unsupported signature verdict")
		}
		if pin.Image, pin.ImageSize, err = applicationHeldPin(ctx, held); err != nil {
			return applicationLegacyPin{}, err
		}
	}
	if err := held.Recheck(ctx); err != nil {
		return applicationLegacyPin{}, err
	}
	if !pin.valid() {
		return applicationLegacyPin{}, errors.New("applications: incomplete program pin")
	}
	return pin, nil
}

// observeLegacyPin reads the incoming program's pin before the directory
// lock: the image is read once, outside storage callbacks. nil means no pin
// could be read; the registration then proves no continuity.
func (d *applicationDirectory) observeLegacyPin(ctx context.Context, program string) *applicationLegacyPin {
	read := d.legacyPin
	if read == nil {
		read = applicationLegacyImagePin
	}
	pin, err := read(ctx, program)
	if err != nil || !pin.valid() {
		return nil
	}
	return &pin
}

// loadLegacyPins reads the pins at open. A missing file is no pins.
func (d *applicationDirectory) loadLegacyPins() error {
	var err error
	d.pins = map[string]applicationLegacyPin{}
	d.pinsBase, err = d.store.Read(filepath.Join(d.dir, applicationPinsFile))
	if err != nil || d.pinsBase.Data == nil {
		return err
	}
	if json.Unmarshal(d.pinsBase.Data, &d.pins) != nil || d.pins == nil || len(d.pins) > 64 {
		return errors.New("applications: invalid program pins")
	}
	for name, pin := range d.pins {
		if !providerName.MatchString(name) || !pin.valid() {
			return errors.New("applications: invalid program pin")
		}
	}
	return nil
}

// setLegacyPinLocked records pin for name, or removes name's pin for nil,
// writing only on a change. The caller holds the directory lock.
func (d *applicationDirectory) setLegacyPinLocked(ctx context.Context, name string, pin *applicationLegacyPin) error {
	old, had := d.pins[name]
	if pin == nil && !had || pin != nil && had && reflect.DeepEqual(old, *pin) {
		return nil
	}
	next := maps.Clone(d.pins)
	if next == nil {
		next = map[string]applicationLegacyPin{}
	}
	if pin == nil {
		delete(next, name)
	} else {
		next[name] = *pin
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := rawcas.ChangeLimitContext(ctx, filepath.Join(d.dir, applicationPinsFile), d.store.MaxBytes, func(current []byte) ([]byte, error) {
		if (current == nil) != (d.pinsBase.Data == nil) || !bytes.Equal(current, d.pinsBase.Data) {
			return nil, rawcas.ErrMoved
		}
		return raw, nil
	}); err != nil {
		return err
	}
	d.pinsBase, d.pins = cas.Value{Data: raw}, next
	return nil
}

// sameActivation reports equal start arguments and activation.
func sameActivation(a, b *wire.ApplicationActivationRecipe) bool {
	return reflect.DeepEqual(a, b)
}
