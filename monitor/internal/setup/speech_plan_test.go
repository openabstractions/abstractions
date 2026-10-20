package setup

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// Kokoros review condition 11: the consent names GPL-3.0-or-later for koko
// and where its source is; every asset is a pinned download Setup's
// transfer accepts.
func TestApprovedSpeechPlanAndConsent(t *testing.T) {
	p := ApprovedSpeechPlan()
	if len(p.Assets) != 6 || !slices.Equal(p.Licenses, []string{"MIT", "GPL-3.0-or-later", "Apache-2.0"}) || p.ID == "" {
		t.Fatalf("plan %+v", p)
	}
	if !strings.Contains(p.Consent, "GPL-3.0-or-later") || !strings.Contains(p.Consent, "Kokoros") || !strings.Contains(p.Consent, "https://github.com/lemonade-sdk/Kokoros") {
		t.Fatalf("consent %q", p.Consent)
	}
	var total int64
	for _, a := range p.Assets {
		if err := a.Download.validate(); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		if a.ID == "kokoro-backend" && (a.License != "GPL-3.0-or-later" || !strings.Contains(a.Download.Label, "GPL-3.0-or-later")) {
			t.Fatalf("Kokoros asset %+v", a)
		}
		total += a.Download.Bytes
	}
	if total != p.DownloadBytes || p.DownloadBytes != 2018708765 || p.PeakBytes <= 2*p.DownloadBytes {
		t.Fatalf("bytes %d, peak %d", p.DownloadBytes, p.PeakBytes)
	}
}

// The plan's pins are the pin record's: installer/third-party.tsv records
// each speech archive and model at the same source and SHA-256, with the
// provider file that enforces it. The record ships in redist, so this is a
// check across the two repositories that the private workspace runs.
func TestSpeechPlanMatchesThePinRecord(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "..", "..", "installer", "third-party.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 9 && fields[0] == "lemonade" && (fields[2] == "archive" || fields[2] == "model") {
			recorded[fields[6]] = strings.TrimPrefix(fields[4], "sha256:")
		}
	}
	for _, a := range ApprovedSpeechPlan().Assets {
		if recorded[a.Download.URL] != a.Download.SHA256 {
			t.Fatalf("%s: plan %s, pin record %q", a.ID, a.Download.SHA256, recorded[a.Download.URL])
		}
	}
}
