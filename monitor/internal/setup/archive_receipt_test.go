package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveReceiptCannotSupplyItsOwnExpectedExecutableBytes(t *testing.T) {
	for _, kind := range []string{"forged-file", "substituted-source", "historical-hint"} {
		t.Run(kind, func(t *testing.T) {
			source, pin := archiveFixture(t, archiveEntry{name: "program.exe", data: "catalog executable"})
			dest := filepath.Join(t.TempDir(), "installed")
			if err := InstallArchive(context.Background(), pin, source, dest); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dest, archiveMarker)
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			var receipt archiveReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "forged-file":
				foreign := []byte("foreign executable")
				if err := os.WriteFile(filepath.Join(dest, "program.exe"), foreign, 0600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(foreign)
				receipt.Files[0].Size = int64(len(foreign))
				receipt.Files[0].SHA256 = hex.EncodeToString(digest[:])
			case "substituted-source":
				other, _ := archiveFixture(t, archiveEntry{name: "program.exe", data: "another archive"})
				receipt.ArchivePath = other
			case "historical-hint":
				receipt.Format = 1
				receipt.ArchivePath = ""
			}
			raw, err = json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if installed, err := ArchiveInstalled(pin, dest); err == nil && installed {
				t.Fatal("mutable marker supplied executable expectations")
			}
			if _, err := matchingReceipt(pin, dest); err == nil {
				t.Fatal("unbound marker authorized removal")
			}
			if _, err := os.Stat(filepath.Join(dest, "program.exe")); err != nil {
				t.Fatalf("refusal changed installed files: %v", err)
			}
		})
	}
}
