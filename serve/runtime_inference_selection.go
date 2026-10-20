package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

const selectionFile = "selection.json"

type selectionRecord struct {
	Account  string                  `json:"account"`
	Program  string                  `json:"program"`
	Policy   iwire.SelectionPolicyV2 `json:"policy"`
	Revision string                  `json:"revision"`
}

type selectionIndex struct {
	Version int               `json:"version"`
	Next    uint64            `json:"next"`
	Records []selectionRecord `json:"records"`
}

func decodeSelection(raw []byte) (selectionIndex, error) {
	index := selectionIndex{Version: 1, Records: []selectionRecord{}}
	if len(raw) == 0 {
		return index, nil
	}
	if err := json.Unmarshal(raw, &index); err != nil || index.Version != 1 {
		return selectionIndex{}, errors.New("inference: invalid selection index")
	}
	return index, nil
}

func (index *selectionIndex) find(subject inference.Subject, name string) int {
	for i, record := range index.Records {
		if record.Account == subject.Account && samePrograms(record.Program, subject.Program) && record.Policy.Name == name {
			return i
		}
	}
	return -1
}

func (o *inferenceOperator) selectionScopeV2(ctx context.Context, caller inference.Subject, target string) (inference.Subject, string) {
	if caller.Account == "" || caller.Program == "" || caller.Account != o.credentials.owner {
		return inference.Subject{}, "forbidden"
	}
	if target == "" {
		target = caller.Program
	}
	if !identity.ValidSubjectProgram(target) {
		return inference.Subject{}, "invalid"
	}
	target = identity.NormalizeSubjectProgram(target)
	action := inference.ActionSelectionManage
	if !samePrograms(target, caller.Program) {
		action = inference.ActionSelectionManageFor
	}
	if word := o.gate(ctx, caller, action); word != "" {
		return inference.Subject{}, word
	}
	return inference.Subject{Account: caller.Account, Program: target}, ""
}

func validSelectionPolicyV2(policy iwire.SelectionPolicyV2) string {
	if !inferenceHostName.MatchString(policy.Name) {
		return "name"
	}
	if len(policy.Candidates) < 1 || len(policy.Candidates) > 8 {
		return "candidates"
	}
	seen := map[string]bool{}
	for _, candidate := range policy.Candidates {
		if candidate.Model == "" || len(candidate.Model) > 256 || strings.TrimSpace(candidate.Model) != candidate.Model {
			return "candidate.model"
		}
		switch candidate.Placement {
		case iwire.CandidatePlacementLocal:
			if candidate.Credential != "" {
				return "candidate.credential"
			}
		case iwire.CandidatePlacementHosted:
			if !credentialName.MatchString(candidate.Credential) {
				return "candidate.credential"
			}
		default:
			return "candidate.placement"
		}
		if candidate.Provider != "" && !inferenceHostName.MatchString(candidate.Provider) {
			return "candidate.provider"
		}
		key := candidate.Placement.String() + "\x00" + candidate.Model + "\x00" + candidate.Credential + "\x00" + candidate.Provider
		if seen[key] {
			return "candidate.duplicate"
		}
		seen[key] = true
	}
	return ""
}

func (o *inferenceOperator) selectionPath() string { return filepath.Join(o.dir, selectionFile) }

func (o *inferenceOperator) ResolveSelectionV2(ctx context.Context, subject inference.Subject, name string) (iwire.SelectionPolicyV2, bool, error) {
	if subject.Account == "" || subject.Program == "" || subject.Account != o.credentials.owner {
		return iwire.SelectionPolicyV2{}, false, errors.New("caller")
	}
	raw, err := cas.ReadLimitContext(ctx, o.selectionPath(), 1<<20)
	if err != nil {
		return iwire.SelectionPolicyV2{}, false, err
	}
	index, err := decodeSelection(raw)
	if err != nil {
		return iwire.SelectionPolicyV2{}, false, err
	}
	i := index.find(subject, name)
	if i < 0 {
		return iwire.SelectionPolicyV2{}, false, nil
	}
	return index.Records[i].Policy, true, nil
}

func selectionReadRefusal(word string) iwire.SelectionReadOutcome {
	if word == "forbidden" {
		return iwire.SelectionReadOutcomeForbidden
	}
	return iwire.SelectionReadOutcomeUnavailable
}

// ownSelectionScope is the caller's own policy namespace when targetProgram
// is empty or names the caller's bound program: a program reads its own
// policies with no grant (INF-R1; VISION 2026-09-17, ownership scopes a
// program's own records). The scope is the bound program, never the target's
// spelling.
func (o *inferenceOperator) ownSelectionScope(caller inference.Subject, targetProgram string) (inference.Subject, bool) {
	if caller.Account == "" || caller.Program == "" || caller.Account != o.credentials.owner {
		return inference.Subject{}, false
	}
	if targetProgram != "" && !samePrograms(targetProgram, caller.Program) {
		return inference.Subject{}, false
	}
	return inference.Subject{Account: caller.Account, Program: caller.Program}, true
}

func (o *inferenceOperator) ReadSelectionV2(ctx context.Context, caller inference.Subject, targetProgram, name string) iwire.SelectionReadV2 {
	scope, own := o.ownSelectionScope(caller, targetProgram)
	word := ""
	if !own {
		scope, word = o.selectionScopeV2(ctx, caller, targetProgram)
	}
	if word == "invalid" {
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeInvalid}
	}
	if word != "" {
		return iwire.SelectionReadV2{Outcome: selectionReadRefusal(word)}
	}
	if !inferenceHostName.MatchString(name) {
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeInvalid}
	}
	raw, err := cas.ReadLimit(o.selectionPath(), 1<<20)
	if err != nil {
		o.report(err)
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnavailable}
	}
	index, err := decodeSelection(raw)
	if err != nil {
		o.report(err)
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnavailable}
	}
	i := index.find(scope, name)
	if i < 0 {
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}
	}
	record := index.Records[i]
	return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Revision: record.Revision, Policy: &record.Policy}
}

func (o *inferenceOperator) PutSelectionV2(ctx context.Context, caller inference.Subject, targetProgram, expected string, policy iwire.SelectionPolicyV2) iwire.SelectionChange {
	scope, word := o.selectionScopeV2(ctx, caller, targetProgram)
	if word == "invalid" {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeInvalid, Reason: "program"}
	}
	if word != "" {
		return iwire.SelectionChange{Outcome: inferenceEditRefusal(word)}
	}
	if field := validSelectionPolicyV2(policy); field != "" {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeInvalid, Reason: field}
	}
	change := iwire.SelectionChange{}
	err := cas.ChangeLimitContext(ctx, o.selectionPath(), 1<<20, func(raw []byte) ([]byte, error) {
		index, err := decodeSelection(raw)
		if err != nil {
			return nil, err
		}
		i := index.find(scope, policy.Name)
		current := ""
		if i >= 0 {
			current = index.Records[i].Revision
		}
		if expected != current {
			change = iwire.SelectionChange{Outcome: iwire.EditOutcomeConflict, Revision: current, Reason: "revision"}
			return raw, nil
		}
		if i < 0 && len(index.Records) >= 128 {
			change = iwire.SelectionChange{Outcome: iwire.EditOutcomeInvalid, Reason: "capacity"}
			return raw, nil
		}
		index.Next++
		revision := fmt.Sprintf("selection-v1:%016x", index.Next)
		record := selectionRecord{Account: scope.Account, Program: scope.Program, Policy: policy, Revision: revision}
		if i >= 0 {
			index.Records[i] = record
		} else {
			index.Records = append(index.Records, record)
		}
		change = iwire.SelectionChange{Outcome: iwire.EditOutcomeApplied, Revision: revision}
		return json.Marshal(index)
	})
	if err != nil {
		o.report(err)
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	return change
}

func (o *inferenceOperator) DeleteSelectionV2(ctx context.Context, caller inference.Subject, targetProgram, expected, name string) iwire.SelectionChange {
	scope, word := o.selectionScopeV2(ctx, caller, targetProgram)
	if word == "invalid" {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeInvalid, Reason: "program"}
	}
	if word != "" {
		return iwire.SelectionChange{Outcome: inferenceEditRefusal(word)}
	}
	if !inferenceHostName.MatchString(name) {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeInvalid, Reason: "name"}
	}
	change := iwire.SelectionChange{}
	err := cas.ChangeLimitContext(ctx, o.selectionPath(), 1<<20, func(raw []byte) ([]byte, error) {
		index, err := decodeSelection(raw)
		if err != nil {
			return nil, err
		}
		i := index.find(scope, name)
		current := ""
		if i >= 0 {
			current = index.Records[i].Revision
		}
		if expected != current {
			change = iwire.SelectionChange{Outcome: iwire.EditOutcomeConflict, Revision: current, Reason: "revision"}
			return raw, nil
		}
		if i < 0 {
			change = iwire.SelectionChange{Outcome: iwire.EditOutcomeUnknown}
			return raw, nil
		}
		index.Records = append(index.Records[:i], index.Records[i+1:]...)
		index.Next++
		change = iwire.SelectionChange{Outcome: iwire.EditOutcomeApplied, Revision: fmt.Sprintf("selection-v1:%016x", index.Next)}
		return json.Marshal(index)
	})
	if err != nil {
		o.report(err)
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeUnavailable}
	}
	return change
}
