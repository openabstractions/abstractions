package startprofile

import (
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
)

// CustodyBinding is binding data on the existing private owned-start channel.
// It supplies no authority; native occurrence/enrollment admission stays with
// the runtime. It is never accepted from ordinary tool arguments.
type CustodyBinding struct {
	Account            string `json:"account"`
	ProgramKey         string `json:"program_key"`
	EnrollmentRevision string `json:"enrollment_revision"`
	RecipeDigest       string `json:"recipe_digest"`
}

const CustodyMetaKey = "oa_receiver_start"

func (b CustodyBinding) Valid() bool {
	bounded := func(value string, limit int) bool {
		return len(value) > 0 && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	digest, err := hex.DecodeString(b.RecipeDigest)
	return bounded(b.Account, 512) && bounded(b.ProgramKey, 4096) && bounded(b.EnrollmentRevision, 256) && err == nil && len(digest) == 32 && hex.EncodeToString(digest) == b.RecipeDigest
}

func (b CustodyBinding) Metadata() map[string]any {
	return map[string]any{CustodyMetaKey: map[string]any{"account": b.Account, "program_key": b.ProgramKey, "enrollment_revision": b.EnrollmentRevision, "recipe_digest": b.RecipeDigest}}
}

func ParseCustodyMetadata(meta map[string]any) (CustodyBinding, error) {
	var b CustodyBinding
	fields, ok := meta[CustodyMetaKey].(map[string]any)
	if !ok || len(fields) != 4 {
		return b, errors.New("owned start custody metadata unavailable")
	}
	values := map[string]*string{"account": &b.Account, "program_key": &b.ProgramKey, "enrollment_revision": &b.EnrollmentRevision, "recipe_digest": &b.RecipeDigest}
	for name, target := range values {
		value, ok := fields[name].(string)
		if !ok {
			return CustodyBinding{}, errors.New("owned start custody metadata invalid")
		}
		*target = value
	}
	if !b.Valid() {
		return CustodyBinding{}, errors.New("owned start custody metadata invalid")
	}
	return b, nil
}

// TargetMetaKey carries the runtime's chosen receiver context on the same
// private channel. Absent means the recipe's own context. It names a routing
// label only; the runtime already admitted the question that selected it.
const TargetMetaKey = "oa_receiver_session"

func ParseTargetMetadata(meta map[string]any) (string, error) {
	raw, present := meta[TargetMetaKey]
	if !present {
		return "", nil
	}
	target, ok := raw.(string)
	if !ok || !label.MatchString(target) {
		return "", errors.New("owned start target metadata invalid")
	}
	return target, nil
}
