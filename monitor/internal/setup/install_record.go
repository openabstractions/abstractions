package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// InstallRecord is the person's one Install approval (hostile critic A1): the
// app-package plan, the picture-model plan and the permissions the apps get,
// approved together on one screen. It sits beside the plan journal, so an
// isolated runtime keeps it in its own state folder. The Panel resumes the
// approved steps from it after a restart; each step's own journal still
// decides whether that step is done.
type InstallRecord struct {
	PlanID string `json:"plan_id"`
	// ModelPlanID is the approved picture-model plan, or empty when the plan
	// has no picture goal.
	ModelPlanID string `json:"model_plan_id,omitempty"`
	// Rules are the exact permission rules the screen stated and the Install
	// click approved, with their digest. The grant step writes these and
	// nothing a later Panel version would compute.
	Rules       []ApprovedRule `json:"rules,omitempty"`
	RulesDigest string         `json:"rules_digest,omitempty"`
	Granted     bool           `json:"granted,omitempty"`
	Opened      bool           `json:"opened,omitempty"`
	ApprovedAt  string         `json:"approved_at"`
}

// ApprovedRule is one exact permission rule of an Install approval.
type ApprovedRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// RulesDigest is the SHA-256 of the rules' canonical JSON, the identity the
// approval screen shows the rules under.
func RulesDigest(rules []ApprovedRule) string {
	if len(rules) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(rules)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

const maxInstallRecordBytes = 4096

// InstallRecordPath is the record's file beside the plan journal.
func (j Journal) InstallRecordPath() (string, error) {
	if j.Path == "" {
		return "", errors.New("setup journal path is empty")
	}
	return filepath.Join(filepath.Dir(j.Path), "install.json"), nil
}

// LoadInstall reads the record; a missing record is the zero value.
func (j Journal) LoadInstall(ctx context.Context) (InstallRecord, error) {
	path, err := j.InstallRecordPath()
	if err != nil {
		return InstallRecord{}, err
	}
	raw, err := cas.ReadLimitContext(ctx, path, maxInstallRecordBytes)
	if err != nil || raw == nil {
		return InstallRecord{}, err
	}
	var record InstallRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return InstallRecord{}, err
	}
	return record, nil
}

// ApproveInstall replaces the record with a new approval of planID.
func (j Journal) ApproveInstall(ctx context.Context, planID, modelPlanID string, rules []ApprovedRule, now time.Time) (InstallRecord, error) {
	record := InstallRecord{PlanID: planID, ModelPlanID: modelPlanID, Rules: rules, RulesDigest: RulesDigest(rules), ApprovedAt: now.UTC().Format(time.RFC3339)}
	return record, j.changeInstall(ctx, func(InstallRecord) (InstallRecord, error) { return record, nil })
}

// MarkInstall records a finished step of the approval of planID. It refuses
// a record that names another plan.
func (j Journal) MarkInstall(ctx context.Context, planID string, mark func(*InstallRecord)) (InstallRecord, error) {
	var out InstallRecord
	err := j.changeInstall(ctx, func(current InstallRecord) (InstallRecord, error) {
		if current.PlanID == "" || current.PlanID != planID {
			return InstallRecord{}, errors.New("the approved installation changed; review and install again")
		}
		mark(&current)
		out = current
		return current, nil
	})
	return out, err
}

func (j Journal) changeInstall(ctx context.Context, change func(InstallRecord) (InstallRecord, error)) error {
	path, err := j.InstallRecordPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, path, maxInstallRecordBytes, func(raw []byte) ([]byte, error) {
		var current InstallRecord
		if raw != nil {
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, err
			}
		}
		next, err := change(current)
		if err != nil {
			return nil, err
		}
		return json.Marshal(next)
	})
}
