package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestPairingInvitationWireVector(t *testing.T) {
	secret := [32]byte{31: 1}
	code, err := encodePairingInvitation(netip.MustParseAddrPort("192.0.2.1:1234"), secret)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{2, 4, 192, 0, 2, 1, 4, 210}, secret[:]...)
	raw, err := base64.RawURLEncoding.Strict().DecodeString(code[4:])
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("wire bytes differ", err)
	}
}

func TestPairingInvitationRoundTrip(t *testing.T) {
	var secret [32]byte
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	for _, text := range []string{"192.0.2.1:1", "127.0.0.1:65535", "[2001:db8::1234]:443", "[::1]:65535", "[::ffff:192.0.2.1]:1234", "[fe80::1]:42"} {
		t.Run(text, func(t *testing.T) {
			at := netip.MustParseAddrPort(text)
			code, err := encodePairingInvitation(at, secret)
			if err != nil {
				t.Fatal(err)
			}
			want := netip.AddrPortFrom(at.Addr().Unmap(), at.Port())
			got, key, err := decodePairingInvitation(code)
			if err != nil || got != want || key != secret {
				t.Fatal("roundtrip mismatch", err)
			}
			length := 74
			if want.Addr().Is4() {
				length = 58
			}
			if len(code) != length || len(code) > pairingInvitationMaxBytes {
				t.Fatal("wrong bound", len(code))
			}
			again, err := encodePairingInvitation(got, key)
			if err != nil || again != code {
				t.Fatal("noncanonical roundtrip")
			}
			raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(code, "oa2:"))
			if err != nil || raw[0] != 2 || raw[1] != byte(map[bool]int{true: 4, false: 6}[want.Addr().Is4()]) {
				t.Fatal("wire prefix", err)
			}
		})
	}
}

func TestPairingInvitationMalformedAndNoSecretEcho(t *testing.T) {
	secret := [32]byte{0x71, 0x42, 0x13}
	code, err := encodePairingInvitation(netip.MustParseAddrPort("192.0.2.1:1234"), secret)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(code[4:])
	malformed := []string{"", "oa2:", "OA2:" + code[4:], "oa1:" + code[4:], code + "=", " " + code, code + " ", code[:12] + "\n" + code[12:], code[:12] + "\r" + code[12:], code[:12] + "\t" + code[12:], code[:12] + "/" + code[13:], code[:12] + "Ω" + code[13:], strings.Repeat("x", 75)}
	mutate := func(change func([]byte) []byte) {
		b := append([]byte{}, raw...)
		malformed = append(malformed, "oa2:"+base64.RawURLEncoding.EncodeToString(change(b)))
	}
	mutate(func(b []byte) []byte { b[0] = 1; return b })
	mutate(func(b []byte) []byte { b[1] = 5; return b })
	mutate(func(b []byte) []byte { b[1] = 6; return b })
	mutate(func(b []byte) []byte { return b[:len(b)-1] })
	mutate(func(b []byte) []byte { return append(b, 1) })
	mutate(func(b []byte) []byte { b[6] = 0; b[7] = 0; return b })
	mutate(func(b []byte) []byte { clear(b[2:6]); return b })
	mutate(func(b []byte) []byte { b[2] = 224; return b })
	mutate(func(b []byte) []byte { clear(b[8:]); return b })
	// The final base64url sextet has unused low bits. Alternate trailing bits
	// must not decode as the same bytes (ordinary non-strict Decode accepts it).
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	i := strings.IndexByte(alphabet, code[len(code)-1])
	malformed = append(malformed, code[:len(code)-1]+string(alphabet[i|1]))
	v6 := netip.MustParseAddr("::ffff:192.0.2.1").As16()
	mapped := append([]byte{2, 6}, v6[:]...)
	mapped = append(mapped, 0, 42)
	mapped = append(mapped, secret[:]...)
	malformed = append(malformed, "oa2:"+base64.RawURLEncoding.EncodeToString(mapped))
	for _, text := range []string{"::", "ff02::1"} {
		a := netip.MustParseAddr(text).As16()
		b := append([]byte{2, 6}, a[:]...)
		b = append(b, 0, 42)
		b = append(b, secret[:]...)
		malformed = append(malformed, "oa2:"+base64.RawURLEncoding.EncodeToString(b))
	}
	for i, bad := range malformed {
		at, key, err := decodePairingInvitation(bad)
		if !errors.Is(err, errPairingInvitation) || err.Error() != "pairing: invalid invitation" || at.IsValid() || key != ([32]byte{}) {
			t.Fatalf("case %d did not refuse safely", i)
		}
	}
}

func TestPairingInvitationAddressAndSecretRefusals(t *testing.T) {
	for _, text := range []string{"0.0.0.0:1", "[::]:1", "224.0.0.1:42", "[ff02::1]:42", "[fe80::1%zone]:42", "192.0.2.1:0", "[::1]:0"} {
		if code, err := encodePairingInvitation(netip.MustParseAddrPort(text), [32]byte{1}); !errors.Is(err, errPairingInvitation) || code != "" {
			t.Fatal("address accepted", text)
		}
	}
	for _, at := range []netip.AddrPort{{}, netip.MustParseAddrPort("127.0.0.1:1")} {
		if code, err := encodePairingInvitation(at, [32]byte{}); !errors.Is(err, errPairingInvitation) || code != "" {
			t.Fatal("empty accepted")
		}
	}
	// Mapped unspecified/multicast values must be rejected after unmapping too.
	for _, text := range []string{"[::ffff:0.0.0.0]:1", "[::ffff:224.0.0.1]:1"} {
		if code, err := encodePairingInvitation(netip.MustParseAddrPort(text), [32]byte{1}); !errors.Is(err, errPairingInvitation) || code != "" {
			t.Fatal("mapped invalid address accepted", text)
		}
	}
}
