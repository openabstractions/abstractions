// Package startprofile validates the operator's literal registered start recipe.
// It grants no permission and performs no registry or vendor settings reads.
package startprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var label = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	MaxTimeoutMS   uint64 = 600000
	MaxOutputBytes uint64 = 1 << 20
	MaxMemoryBytes uint64 = 24 << 30
	MaxBudgetCents uint64 = 1000000
)

// Profile is supplied exclusively by an authenticated operator registration.
// BudgetCents is the approved vendor estimate bound, never a hard spend claim.
type Profile struct {
	Name, Context, Folder, StateDir, CLIFileID, Model string
	TimeoutMS, OutputBytes, MemoryBytes, BudgetCents  uint64
}

func (p Profile) Arguments() []string {
	return []string{"start", "--profile", p.Name, "--context", p.Context, "--folder", p.Folder, "--state-dir", p.StateDir, "--cli-file-id", p.CLIFileID, "--timeout-ms", strconv.FormatUint(p.TimeoutMS, 10), "--output-bytes", strconv.FormatUint(p.OutputBytes, 10), "--memory-bytes", strconv.FormatUint(p.MemoryBytes, 10), "--budget-cents", strconv.FormatUint(p.BudgetCents, 10), "--model", p.Model}
}

func Parse(args []string) (Profile, error) {
	var p Profile
	bad := func() (Profile, error) {
		return Profile{}, errors.New("unsupported or incomplete registered start profile")
	}
	if len(args) != 21 || args[0] != "start" {
		return bad()
	}
	names := []string{"--profile", "--context", "--folder", "--state-dir", "--cli-file-id", "--timeout-ms", "--output-bytes", "--memory-bytes", "--budget-cents", "--model"}
	// Fixed flag/value pairs plus the command; reject duplicates and aliases.
	if len(args) != 1+2*len(names) {
		return bad()
	}
	for i, n := range names {
		if args[1+2*i] != n {
			return bad()
		}
	}
	p.Name, p.Context, p.Folder, p.StateDir, p.CLIFileID = args[2], args[4], args[6], args[8], args[10]
	p.Model = args[20]
	if (p.Name != "claude-v1" && p.Name != "codex-v1") || !label.MatchString(p.Context) || !label.MatchString(p.Model) || !filepath.IsAbs(p.Folder) || !filepath.IsAbs(p.StateDir) || filepath.Clean(p.Folder) != p.Folder || filepath.Clean(p.StateDir) != p.StateDir || p.CLIFileID == "" || len(p.CLIFileID) > 4096 || strings.IndexByte(p.CLIFileID, 0) >= 0 {
		return bad()
	}
	values := []*uint64{&p.TimeoutMS, &p.OutputBytes, &p.MemoryBytes, &p.BudgetCents}
	for i, dst := range values {
		raw := args[12+2*i]
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || v == 0 || strconv.FormatUint(v, 10) != raw {
			return bad()
		}
		*dst = v
	}
	if p.TimeoutMS > MaxTimeoutMS || p.OutputBytes > MaxOutputBytes || p.MemoryBytes > MaxMemoryBytes || p.BudgetCents > MaxBudgetCents {
		return bad()
	}
	return p, nil
}

func (p Profile) Fingerprint(program string) string {
	raw, _ := json.Marshal(struct {
		Program   string
		Arguments []string
	}{program, p.Arguments()})
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}

// PermitFingerprint names the recipe a person's start permit is granted for:
// its kind, context, model and limits on this program. It leaves out the saved
// command's file identity and the folders, so a vendor self-update that only
// re-pins the command asks nothing again. Every start still revalidates the
// saved command against its current image.
func (p Profile) PermitFingerprint(program string) string {
	raw, _ := json.Marshal(struct {
		Program, Name, Context, Model                    string
		TimeoutMS, OutputBytes, MemoryBytes, BudgetCents uint64
	}{program, p.Name, p.Context, p.Model, p.TimeoutMS, p.OutputBytes, p.MemoryBytes, p.BudgetCents})
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}
