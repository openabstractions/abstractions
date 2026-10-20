package nativecli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

func TestClaudeSignedPublisherRequiresCurrentTrustedAnchorAndDescription(t *testing.T) {
	anchor := identity.Code{Trusted: true, Subject: "registered publisher", Issuer: "registered issuer"}
	for _, mode := range []string{"valid-empty-original-name", "foreign-signer", "foreign-issuer", "unsigned", "tampered", "wrong-description", "catalog", "untrusted-anchor", "unbound-anchor"} {
		t.Run(mode, func(t *testing.T) {
			code := anchor
			code.Description = "Claude Code"
			proof, anchorProof := identity.ProofBound, identity.ProofBound
			current := anchor
			switch mode {
			case "foreign-signer":
				code.Subject = "foreign publisher"
			case "foreign-issuer":
				code.Issuer = "foreign issuer"
			case "unsigned":
				proof = identity.ProofUnsigned
				code.Trusted = false
			case "tampered":
				proof = identity.ProofInvalid
				code.Trusted = false
			case "wrong-description":
				code.Description = "Claude"
			case "catalog":
				code.Catalog = true
			case "untrusted-anchor":
				current.Trusted = false
			case "unbound-anchor":
				anchorProof = identity.ProofClaimed
			}
			if got := matchesClaudePublisher(code, proof, current, anchorProof); got != (mode == "valid-empty-original-name") {
				t.Fatal(mode, got)
			}
		})
	}
}

func TestClaudeSignedPackageAbsenceIsExplicitAndForeignPackageRefuses(t *testing.T) {
	for _, row := range []struct {
		name, family     string
		err              error
		allowed, foreign bool
	}{
		{"native-package", claudeProductFamily, nil, true, false},
		{"unpackaged", "", appModelNoPackage, true, false},
		{"same-name-foreign-package", "Other_abcdefghijklm", nil, false, true},
		{"unknown-package", "", windows.ERROR_ACCESS_DENIED, false, false},
		{"empty-unproven", "", nil, false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := claudeCandidatePackage(row.family, row.err)
			if (err == nil) != row.allowed || errors.Is(err, errOtherClaudeImage) != row.foreign {
				t.Fatal(err)
			}
		})
	}
}

type claudeAnchorFixture struct {
	nativeProductImage
	changed bool
}

func (a *claudeAnchorFixture) Check(ctx context.Context) error {
	if a.changed {
		return errors.New("registered package or anchor changed")
	}
	return a.nativeProductImage.Check(ctx)
}

func TestClaudeSignedSelectionRechecksPackageAnchorAndImage(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "cli.exe")
	anchorProgram := filepath.Join(root, "gui.exe")
	for _, path := range []string{program, anchorProgram} {
		if err := os.WriteFile(path, []byte("inert held fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	image := claudeCLIImageFixture(t, program)
	anchor := &claudeAnchorFixture{nativeProductImage: claudeCLIImageFixture(t, anchorProgram)}
	alive := true
	row := claudeProcessImage{image: &anchoredClaudeImage{nativeProductImage: image, anchor: anchor}, current: func() error {
		if !alive {
			return errProductProcessEnded
		}
		return nil
	}, close: func() error { return errors.Join(image.Close(), anchor.Close()) }}
	selected, err := selectClaudeImages(context.Background(), []claudeProcessImage{row}, func(context.Context, string) ([]byte, error) { return []byte("2.1.309 (Claude Code)\n"), nil })
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if err := os.WriteFile(program, []byte("tampered"), 0600); err == nil {
		t.Fatal("CLI write fence lost")
	}
	if err := os.Rename(program, program+".moved"); err == nil {
		t.Fatal("CLI rename fence lost")
	}
	anchor.changed = true
	if err := selected.Check(context.Background()); err == nil {
		t.Fatal("package/image anchor update stayed admitted")
	}
	anchor.changed = false
	alive = false
	if err := selected.Check(context.Background()); err == nil {
		t.Fatal("ended native process stayed admitted")
	}
}

func TestClaudeSignedActualSignatureAndTamperRefusal(t *testing.T) {
	if os.Getenv("OA_AGENT_SELECTION1784_DIAG") != "1" {
		t.Skip("explicit actual native signature receiving")
	}
	ctx := context.Background()
	rows, err := currentSignedClaudeImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, row := range rows {
			row.close()
		}
	}()
	if len(rows) == 0 {
		t.Fatal("no native CLI to receive")
	}
	anchor, err := packaged.OpenImage(ctx, claudeProductFamily, "app/claude.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	anchorCode, anchorProof, err := identity.VerifyFile(anchor.Program(), nil)
	if err != nil {
		t.Fatal(err)
	}
	code, proof, err := identity.VerifyFile(rows[0].image.Program(), nil)
	if err != nil || !matchesClaudePublisher(code, proof, anchorCode, anchorProof) {
		t.Fatal("native signed pair", code, proof, err)
	}
	t.Logf("actual_native_signature_bound=%t trusted=%t description=%q current_registered_subject_issuer_match=true", proof == identity.ProofBound, code.Trusted, code.Description)
	source, err := os.Open(rows[0].image.Program())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil || stat.Size() > 512<<20 {
		t.Fatal("bounded native signed copy", err)
	}
	copyPath := filepath.Join(t.TempDir(), "tampered.exe")
	copy, err := os.OpenFile(copyPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(copy, source); err != nil {
		copy.Close()
		t.Fatal(err)
	}
	var value [1]byte
	if _, err := copy.ReadAt(value[:], 0x40); err != nil {
		copy.Close()
		t.Fatal(err)
	}
	value[0] ^= 1
	if _, err := copy.WriteAt(value[:], 0x40); err != nil {
		copy.Close()
		t.Fatal(err)
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	tampered, tamperedProof, verifyErr := identity.VerifyFile(copyPath, nil)
	if matchesClaudePublisher(tampered, tamperedProof, anchorCode, anchorProof) {
		t.Fatal("tampered signed bytes admitted", tampered, tamperedProof, verifyErr)
	}
	t.Logf("actual_tampered_copy_refused=true proof=%v trusted=%t", tamperedProof, tampered.Trusted)
}
