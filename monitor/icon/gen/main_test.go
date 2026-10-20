package main

import (
	"bytes"
	"github.com/openabstractions/abstractions/monitor/internal/personfolders"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeCOFFRejectsUnselectedOrMalformedObject(t *testing.T) {
	if err := sanitizeCOFF("relative.syso"); err == nil {
		t.Fatal("relative output accepted")
	}
	for _, name := range []string{"wrong.o", "malformed.syso"} {
		path := filepath.Join(t.TempDir(), name)
		before := make([]byte, 20)
		if err := os.WriteFile(path, before, 0600); err != nil {
			t.Fatal(err)
		}
		if err := sanitizeCOFF(path); err == nil {
			t.Fatal("unselected or malformed object accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("rejected object changed: %v", err)
		}
	}
}

func TestResourceVersionComesFromSelectedInput(t *testing.T) {
	for _, version := range []string{"0.8.0", "0.9.7", "12.34.56"} {
		data, err := resourceScript(version)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		numeric := strings.ReplaceAll(version, ".", ",") + ",0"
		for _, field := range []string{"FILEVERSION     ", "PRODUCTVERSION  "} {
			if strings.Count(text, field+numeric) != 1 {
				t.Fatalf("%s missing selected numeric version %s", field, version)
			}
		}
		if strings.Count(text, `"`+version+`.0"`) != 2 {
			t.Fatalf("selected string versions missing: %s", version)
		}
		again, _ := resourceScript(version)
		if !bytes.Equal(data, again) {
			t.Fatal("resource text is nondeterministic")
		}
	}
}

func TestResourceVersionRefusesMissingMalformedAndOverflow(t *testing.T) {
	for _, version := range []string{"", "v0.8.0", "0.08.0", "0.8", "-1.0.0", "1.2.3.4", "65536.0.0", "0.8.0\n"} {
		if _, err := resourceScript(version); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}

func TestResourceOnlyWritePreservesArtwork(t *testing.T) {
	root := t.TempDir()
	icon := filepath.Join(root, "panel.ico")
	original := []byte("owned icon sentinel")
	if err := os.WriteFile(icon, original, 0600); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(root, "panel.rc")
	if err := writeRC(rc, "0.9.7"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(icon)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("resource version write changed artwork")
	}
	expected, _ := resourceScript("0.9.7")
	actual, err := os.ReadFile(rc)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatal("written resource differs")
	}
}

func TestGenerateDirectiveForwardsExplicitSelectedVersion(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "doc.go"))
	if err != nil {
		t.Fatal(err)
	}
	directive := "//go:generate go run ./gen -version $OA_RELEASE_VERSION"
	if !strings.Contains(string(data), directive+"\n") {
		t.Fatal("actual go:generate directive does not forward selected version")
	}
	for _, version := range []string{"0.9.7", ""} {
		t.Setenv("OA_RELEASE_VERSION", version)
		command := os.ExpandEnv(strings.TrimPrefix(directive, "//go:generate "))
		if command != "go run ./gen -version "+version {
			t.Fatal("directive changed selected version")
		}
		_, err := resourceScript(version)
		if (err != nil) != (version == "") {
			t.Fatal("missing directive input must refuse")
		}
	}
}

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }

func TestPersonFoldersStandIn(t *testing.T) {
	person := os.Getenv("OA_TEST_PERSON")
	if person == "" {
		t.Fatal("test package has no stand-in person")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		rel, err := filepath.Rel(person, os.Getenv(key))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Fatalf("%s escapes stand-in: %q (%v)", key, rel, err)
		}
	}
}
