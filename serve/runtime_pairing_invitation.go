package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"

	"github.com/openabstractions/abstraction-identity/remote"
)

const pairingInvitationPrefix = "oa2:"
const pairingInvitationMaxBytes = 74

// This closed error never incorporates the submitted invitation or secret.
var errPairingInvitation = errors.New("pairing: invalid invitation")

func validPairingInvitationAddress(at netip.AddrPort) bool {
	a := at.Addr()
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	return !a.IsUnspecified() && !a.IsMulticast() && at.Port() != 0
}

// encodePairingInvitation carries possession for one consumed pairing attempt.
// It grants no peer trust and performs no network or persistence operation.
func encodePairingInvitation(at netip.AddrPort, secret [32]byte) (string, error) {
	if !validPairingInvitationAddress(at) || secret == ([32]byte{}) {
		return "", errPairingInvitation
	}
	a := at.Addr().Unmap()
	family := byte(6)
	if a.Is4() {
		family = 4
	}
	raw := []byte{remote.PairingVersion, family}
	raw = append(raw, a.AsSlice()...)
	raw = binary.BigEndian.AppendUint16(raw, at.Port())
	raw = append(raw, secret[:]...)
	return pairingInvitationPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodePairingInvitation(code string) (netip.AddrPort, [32]byte, error) {
	refuse := func() (netip.AddrPort, [32]byte, error) { return netip.AddrPort{}, [32]byte{}, errPairingInvitation }
	if len(code) > pairingInvitationMaxBytes || !strings.HasPrefix(code, pairingInvitationPrefix) {
		return refuse()
	}
	text := code[len(pairingInvitationPrefix):]
	// encoding/base64 deliberately ignores CR/LF even in strict mode. This
	// exact alphabet check also refuses whitespace, padding and alternate alphabets.
	for _, c := range []byte(text) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return refuse()
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(raw) < 2 || raw[0] != remote.PairingVersion {
		return refuse()
	}
	n := 0
	switch raw[1] {
	case 4:
		n = 4
	case 6:
		n = 16
	default:
		return refuse()
	}
	if len(raw) != 2+n+2+32 {
		return refuse()
	}
	a, ok := netip.AddrFromSlice(raw[2 : 2+n])
	if !ok || raw[1] == 6 && a.Is4In6() {
		return refuse()
	}
	at := netip.AddrPortFrom(a, binary.BigEndian.Uint16(raw[2+n:4+n]))
	var secret [32]byte
	copy(secret[:], raw[4+n:])
	if !validPairingInvitationAddress(at) || secret == ([32]byte{}) {
		return refuse()
	}
	return at, secret, nil
}
