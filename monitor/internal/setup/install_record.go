package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// InstallRecord is the person's one Install approval (hostile critic A1): the
// app-package plan, the picture-model plan and the permissions the apps get,
// approved together on one screen. It sits beside the plan journal, so an
// isolated runtime keeps it in its own state folder. After a restart the
// Setup page shows an unfinished approval as stopped, and the person's
// Install continues its steps; each step's own journal still decides
// whether that step is done.
type InstallRecord struct {
	PlanID string `json:"plan_id"`
	// ModelPlanID is the approved picture-model plan, or empty when the plan
	// has no picture goal.
	ModelPlanID string `json:"model_plan_id,omitempty"`
	// CodingModelPlanID is present only when the Install review explicitly
	// included that exact coding model. Older records approve no coding model.
	CodingModelPlanID string `json:"coding_model_plan_id,omitempty"`
	// PictureModelPlanIDs are the other picture models the Install review
	// ticked beside ModelPlanID, the recommended one.
	PictureModelPlanIDs []string `json:"picture_model_plan_ids,omitempty"`
	// Rules are the exact permission rules the screen stated and the Install
	// click approved, with their digest. The grant step writes these and
	// nothing a later Panel version would compute.
	Rules       []ApprovedRule `json:"rules,omitempty"`
	RulesDigest string         `json:"rules_digest,omitempty"`
	Granted     bool           `json:"granted,omitempty"`
	Opened      bool           `json:"opened,omitempty"`
	// Cancelled is the person's Cancel during the work: Setup starts no
	// further step of this approval until the next Install click.
	Cancelled  bool   `json:"cancelled,omitempty"`
	ApprovedAt string `json:"approved_at"`
	// Build is the Panel build (PanelBuild) that approved or last continued
	// the work. Records written before it existed have none.
	Build string `json:"build,omitempty"`
	// Interrupted is InterruptedByUpdate once another build read the
	// unfinished approval: the update ended the page that drove it. The
	// person's Install continues it (ContinueInstall).
	Interrupted string `json:"interrupted,omitempty"`
	// Failure is the last step's refusal, kept so a reloaded page names the
	// component and its reason. Continuing the install clears it.
	Failure *InstallFailure `json:"failure,omitempty"`
}

// InstallFailure is one refused step: the component's row id, a short plain
// sentence and the raw detail.
type InstallFailure struct {
	Component string `json:"component"`
	Sentence  string `json:"sentence"`
	Detail    string `json:"detail,omitempty"`
}

// RecordInstallFailure keeps failure on the approval of planID, trimming its
// text so the record stays within its size limit.
func (j Journal) RecordInstallFailure(ctx context.Context, planID string, failure InstallFailure) (InstallRecord, error) {
	clip := func(s string, n int) string {
		if len(s) > n {
			return strings.ToValidUTF8(s[:n], "")
		}
		return s
	}
	failure.Component, failure.Sentence, failure.Detail = clip(failure.Component, 64), clip(failure.Sentence, 300), clip(failure.Detail, 600)
	if failure.Component == "" || failure.Sentence == "" {
		return InstallRecord{}, errors.New("a failed component and its reason are required")
	}
	return j.MarkInstall(ctx, planID, func(r *InstallRecord) { r.Failure = &failure })
}

// NoteInstallUpdate reads the record and marks an uncancelled approval that
// another Panel build recorded as interrupted by an update. It starts
// nothing; the page shows the stopped install and its Install button.
func (j Journal) NoteInstallUpdate(ctx context.Context) (InstallRecord, error) {
	record, err := j.LoadInstall(ctx)
	if err != nil || !interruptedByBuild(record) {
		return record, err
	}
	err = j.changeInstall(ctx, func(current InstallRecord) (InstallRecord, error) {
		if interruptedByBuild(current) {
			current.Interrupted = InterruptedByUpdate
		}
		record = current
		return current, nil
	})
	return record, err
}

func interruptedByBuild(record InstallRecord) bool {
	return record.PlanID != "" && !record.Cancelled && record.Interrupted == "" && record.Build != PanelBuild()
}

// ContinueInstall is the person's Install on a stopped approval of planID:
// it clears the interruption and records this build as the one driving it.
// A cancelled or replaced approval is refused; it needs a new Install.
func (j Journal) ContinueInstall(ctx context.Context, planID string) (InstallRecord, error) {
	var out InstallRecord
	err := j.changeInstall(ctx, func(current InstallRecord) (InstallRecord, error) {
		if current.PlanID == "" || current.PlanID != planID || current.Cancelled {
			return InstallRecord{}, errors.New("the approved installation changed; review and install again")
		}
		current.Interrupted, current.Build, current.Failure = "", PanelBuild(), nil
		out = current
		return current, nil
	})
	return out, err
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
func (j Journal) ApproveInstall(ctx context.Context, planID, modelPlanID string, rules []ApprovedRule, now time.Time, codingPlanID ...string) (InstallRecord, error) {
	coding := ""
	if len(codingPlanID) > 1 {
		return InstallRecord{}, errors.New("one coding-model approval required")
	}
	if len(codingPlanID) == 1 {
		coding = codingPlanID[0]
	}
	return j.ApproveInstallRecord(ctx, InstallRecord{PlanID: planID, ModelPlanID: modelPlanID, CodingModelPlanID: coding, Rules: rules}, now)
}

// ApproveInstallRecord replaces the record with the approval of record's
// plan, models and rules, digesting the rules and stamping now.
func (j Journal) ApproveInstallRecord(ctx context.Context, record InstallRecord, now time.Time) (InstallRecord, error) {
	record = InstallRecord{PlanID: record.PlanID, ModelPlanID: record.ModelPlanID, PictureModelPlanIDs: record.PictureModelPlanIDs,
		CodingModelPlanID: record.CodingModelPlanID, Rules: record.Rules, RulesDigest: RulesDigest(record.Rules), ApprovedAt: now.UTC().Format(time.RFC3339), Build: PanelBuild()}
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
