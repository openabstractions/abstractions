package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	identity "github.com/openabstractions/abstraction-identity"
)

// The catalogue pin is the package's trust root: an unsigned launch helper
// runs on it, a signed one also needs a trusted signature.
func TestNativeImageAdmissionTrustsThePinForUnsignedHelpers(t *testing.T) {
	program := filepath.Join(t.TempDir(), "launch-helper.exe")
	for _, test := range []struct {
		code  identity.Code
		proof identity.Proof
		ok    bool
	}{
		{identity.Code{}, identity.ProofUnsigned, true},
		{identity.Code{Trusted: true, Subject: "OpenAbstractions"}, identity.ProofBound, true},
		{identity.Code{Subject: "Someone"}, identity.ProofInvalid, false},
		{identity.Code{Subject: "Someone"}, identity.ProofBound, false},
		// A trusted verdict without a signature is not a helper's own image.
		{identity.Code{Trusted: true}, identity.ProofUnsigned, false},
	} {
		if err := nativeImageAdmitted(program, test.code, test.proof); (err == nil) != test.ok {
			t.Fatalf("%+v %v: %v", test.code, test.proof, err)
		}
	}
}

// Bytes that differ from the pin refuse with one plain sentence, before the
// helper is held or run.
func TestNativeImageMismatchRefusesWithOneSentence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch-helper.exe")
	if err := os.WriteFile(path, []byte("changed helper"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("pinned helper"))
	_, err := runNativeArchiveCommandWith(context.Background(), sevenZipPin{Path: path, Bytes: int64(len("changed helper")), SHA256: hex.EncodeToString(sum[:])}, nativeArchiveRegister, "", "")
	if !errors.Is(err, errNativeImagePin) || err.Error() != "the ComfyUI launch helper differs from its package pin, so Setup did not run it" {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestNativeImageAdmissionRefusesASharedInterpreter(t *testing.T) {
	program := filepath.Join(t.TempDir(), "python.exe")
	if !identity.SharedInterpreterProgram(program, identity.Code{}) {
		t.Skip("this identity build does not name python.exe a shared interpreter")
	}
	if err := nativeImageAdmitted(program, identity.Code{}, identity.ProofUnsigned); err == nil {
		t.Fatal("an unsigned shared interpreter was admitted")
	}
}

// A refused launch helper operation names the helper's own reason: the
// service outcome register-self prints, else its last stderr line.
func TestNativeArchiveRefusalNamesTheHelperReason(t *testing.T) {
	err := nativeArchiveRefusal(nativeArchiveRegister, "register-self", 2,
		[]byte(`{"outcome":"conflict","reason":"another program holds comfyui"}`+"\n"), []byte("self-registration conflict\n"))
	if err == nil || err.Error() != "the ComfyUI launch helper's register-self stopped (exit 2): conflict: another program holds comfyui" {
		t.Fatalf("register-self refusal: %v", err)
	}
	err = nativeArchiveRefusal(nativeArchiveRegister, "register-self", 2, []byte(`{"outcome":"conflict","reason":"identity"}`+"\n"), nil)
	if !errors.Is(err, ErrRegistrationIdentity) || err.Error() != "the ComfyUI launch helper's register-self stopped (exit 2): conflict: identity" {
		t.Fatalf("identity refusal: %v", err)
	}
	err = nativeArchiveRefusal(nativeArchiveSeal, "seal-native-install", 2, nil, []byte("warming\nOA native Comfy host unavailable: seal requires a new metadata record\n"))
	if err == nil || err.Error() != "the ComfyUI launch helper's seal-native-install stopped (exit 2): OA native Comfy host unavailable: seal requires a new metadata record" {
		t.Fatalf("seal refusal: %v", err)
	}
	if err := nativeArchiveRefusal(nativeArchiveCheck, "check-native-install", 3, nil, nil); err == nil || strings.Contains(err.Error(), "verification") || err.Error() != "the ComfyUI launch helper's check-native-install stopped (exit 3)" {
		t.Fatalf("silent refusal: %v", err)
	}
}

// A long reason is cut on a rune boundary.
func TestNativeArchiveRefusalCutsOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 399) + "é" + strings.Repeat("b", 10)
	err := nativeArchiveRefusal(nativeArchiveSeal, "seal-native-install", 2, nil, []byte(long))
	if err == nil || !utf8.ValidString(err.Error()) || !strings.HasSuffix(err.Error(), strings.Repeat("a", 399)+"…") {
		t.Fatalf("cut reason: %q", err)
	}
}
