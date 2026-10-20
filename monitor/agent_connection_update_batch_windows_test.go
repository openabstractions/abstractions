package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
	"golang.org/x/sys/windows"
)

func TestAgentConnectionPublicationStopFieldsKeepClosedReasonsAndParties(t *testing.T) {
	const secret = `C:\private\person-secret\image.exe: native private message`
	stages := []*agentConnectionBatchStage{
		{previous: setup.AgentEnrollmentCapture{Application: "oa-claude-integration"}},
		{previous: setup.AgentEnrollmentCapture{Application: "oa-codex-integration"}},
		{previous: setup.AgentEnrollmentCapture{Application: "oa-claude-integration"}},
	}
	for _, row := range []struct {
		name, reason string
		err          error
	}{
		{"restart", "restart_required", fmt.Errorf("%s: %w", secret, setup.ErrAgentUpdateRestartRequired)},
		{"resume", "resume_proof_required", errors.Join(errors.New(secret), setup.ErrAgentUpdateResumeProofRequired)},
		{"directories", "directories_differ", fmt.Errorf("%s: %w", secret, setup.ErrAgentUpdateDirectoriesDiffer)},
		{"unknown", "other", errors.New(secret)},
		{"native", "other", fmt.Errorf("%s: %w", secret, windows.ERROR_ACCESS_DENIED)},
		{"text-is-not-authority", "other", errors.New("active connection image requires restart; staged image requires service-owned resume proof; connection update batch directories differ")},
	} {
		t.Run(row.name, func(t *testing.T) {
			fields := agentConnectionPublicationStopFields(stages, row.err)
			want := map[string]string{"connection.hosts": "codex,claude", "reason": row.reason}
			if !reflect.DeepEqual(fields, want) {
				t.Fatal("publication diagnostic escaped closed vocabulary", fields)
			}
		})
	}
	fields := agentConnectionPublicationStopFields([]*agentConnectionBatchStage{nil, {previous: setup.AgentEnrollmentCapture{Application: secret}}}, errors.New(secret))
	if !reflect.DeepEqual(fields, map[string]string{"connection.hosts": "other", "reason": "other"}) {
		t.Fatal("unknown stage metadata entered diagnostic", fields)
	}
}

func TestAgentConnectionPublicationPendingPreservesOriginalError(t *testing.T) {
	original := errors.Join(setup.ErrAgentUpdateRestartRequired, fmt.Errorf("private image path: %w", windows.ERROR_SHARING_VIOLATION))
	commits, ready := 0, 0
	err := finishAgentConnectionUpdateBatch(context.Background(), "same-intent", func(intent string) (applications.AgentUpdateResult, error) {
		commits++
		if intent != "same-intent" {
			t.Fatal(intent)
		}
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
	}, func() error { return original }, func(applications.AgentUpdateResult) error { ready++; return nil })
	var pending *agentConnectionUpdatePending
	if !errors.As(err, &pending) || pending.Err != original || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || pending.Intent != "same-intent" || !pending.RestartRequired || commits != 1 || ready != 0 {
		t.Fatal("safe logging changed original pending error or native authority", err, commits, ready)
	}
}

func TestAgentConnectionBatchPublishesOnlyServiceReady(t *testing.T) {
	for _, reason := range []string{"not_now", "changed_old_image", "permission_pending", "update_ready"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, published := 0, 0
			commit := func(intent string) (applications.AgentUpdateResult, error) {
				if intent != "service-intent" {
					t.Fatal(intent)
				}
				calls++
				if reason == "permission_pending" {
					cancel()
				}
				if calls == 2 {
					return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated"}, nil
				}
				return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: reason}, nil
			}
			err := finishAgentConnectionUpdateBatch(ctx, "service-intent", commit, func() error { published++; return nil })
			if reason == "update_ready" {
				if err != nil || published != 1 || calls != 2 {
					t.Fatal(published, calls, err)
				}
			} else if published != 0 {
				t.Fatal("published before native Update", reason)
			}
		})
	}
}

func TestAgentConnectionCurrentImageInspectsBoundClaudeCompanionDrift(t *testing.T) {
	for _, cache := range []string{"current", "absent", "unknown", "update"} {
		t.Run(cache, func(t *testing.T) {
			root := t.TempDir()
			row := claudeSavedConnection{Kind: "user", Registration: "saved", Project: root, ProjectKey: "project-key", Name: "openabstractions", Program: filepath.Join(root, "oa-claude-integration.exe"), ProgramKey: "held-key"}
			page, err := json.Marshal(struct {
				Outcome     string                  `json:"outcome"`
				Connections []claudeSavedConnection `json:"connections"`
			}{"page", []claudeSavedConnection{row}})
			if err != nil {
				t.Fatal(err)
			}
			state := cache
			c := &claudePanelConnections{userMod: true, state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, helperState: func(context.Context, string, string) agentHelperReading { return agentHelperReading{State: "current"} }, command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "connections":
					return page, nil
				case "plugin":
					return []byte(`{"outcome":"page","state":"` + state + `"}`), nil
				case "watch-permission":
					return []byte(`{"outcome":"page","state":"owned"}`), nil
				default:
					t.Fatal(args)
					return nil, nil
				}
			}}
			members, err := ownedAgentConnectionUpdatesWith(context.Background(), &servicePanel{claudeConnections: c}, func(_ context.Context, party setup.AgentParty) (string, string) {
				if party == setup.AgentClaude {
					return "current", ""
				}
				return "absent", ""
			})
			if err != nil {
				t.Fatal(err)
			}
			if cache != "update" {
				if len(members) != 0 {
					t.Fatal("unproved companion drift selected", cache)
				}
				return
			}
			if len(members) != 1 || members[0].claudeCompanionDrift == nil {
				t.Fatal("current-image skip hid positive loaded-cache drift", members)
			}
			if err := members[0].claudeCompanionDrift(context.Background(), row.Program, row.ProgramKey); err != nil {
				t.Fatal(err)
			}
			state = "current"
			if err := members[0].claudeCompanionDrift(context.Background(), row.Program, row.ProgramKey); err == nil {
				t.Fatal("cached drift flag bypassed owning status reread")
			}
		})
	}
}

// The archived Panel command fixture rejected two saved rows as ambiguous.
// The owning batch inventories all positively owned helpers and deduplicates
// repeated vendor rows for the same native image instead.
func TestAgentConnectionBatchInventoriesSavedClaudeRowsByProgramKey(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			root := t.TempDir()
			c := &claudePanelConnections{state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, helperState: func(context.Context, string, string) agentHelperReading { return agentHelperReading{State: "update"} }}
			rows := []claudeSavedConnection{
				{Kind: "user", Registration: "saved", Name: "one", Project: root, ProjectKey: "project-one", Program: filepath.Join(root, "helper-one.exe"), ProgramKey: "key-one"},
				{Kind: "plugin", Registration: "saved", Name: "two", Project: root, ProjectKey: "project-two", Program: filepath.Join(root, "helper-two.exe"), ProgramKey: "key-two"},
			}
			if sameKey {
				rows[1].Program, rows[1].ProgramKey = rows[0].Program, rows[0].ProgramKey
			}
			c.command = func(context.Context, string, ...string) ([]byte, error) {
				return json.Marshal(struct {
					Outcome     string                  `json:"outcome"`
					Connections []claudeSavedConnection `json:"connections"`
				}{"page", rows})
			}
			members, err := ownedAgentConnectionUpdatesWith(context.Background(), &servicePanel{claudeConnections: c}, func(_ context.Context, party setup.AgentParty) (string, string) {
				if party == setup.AgentClaude {
					return "update", ""
				}
				return "absent", ""
			}, setup.AgentClaude)
			want := 2
			if sameKey {
				want = 1
			}
			if err != nil || len(members) != want {
				t.Fatal(len(members), want, err)
			}
		})
	}
}

func TestAgentConnectionButtonProjectsOwningBatchOutcome(t *testing.T) {
	for _, reason := range []string{"permission_pending", "not_now", "updated", "update_ready"} {
		t.Run(reason, func(t *testing.T) {
			published, completed := 0, 0
			var result agentHelperUpdateResult
			commit := func(string) (applications.AgentUpdateResult, error) {
				state := reason
				if published != 0 {
					state = "updated"
				}
				return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: state, Intent: "same-intent"}, nil
			}
			err := finishAgentConnectionUpdateBatchWithResult(context.Background(), "same-intent", commit, func() error { published++; return nil }, func(value agentHelperUpdateResult) { result = value }, func(applications.AgentUpdateResult) error { return nil }, func(applications.AgentUpdateResult) error { completed++; return nil })
			want := reason
			if reason == "update_ready" {
				want = "updated"
			}
			if err != nil || result.Outcome != want {
				t.Fatal(result, err)
			}
			if reason == "permission_pending" || reason == "not_now" {
				if published != 0 || completed != 0 {
					t.Fatal("pending/declined update performed effects")
				}
			} else if completed != 1 {
				t.Fatal("completion omitted", completed)
			}
		})
	}
	var result agentHelperUpdateResult
	err := finishAgentConnectionUpdateBatchWithResult(context.Background(), "same-intent", func(string) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
	}, func() error { return setup.ErrAgentUpdateRestartRequired }, func(value agentHelperUpdateResult) { result = value })
	if err != nil || result.Outcome != "update_pending" || !result.RestartRequired {
		t.Fatal(result, err)
	}
}

func TestAgentConnectionButtonObservesLoginPendingIntent(t *testing.T) {
	for _, reason := range []string{"permission_pending", "not_now"} {
		finds, commits := 0, 0
		result, retained, err := retainedAgentConnectionUpdateResult(setup.AgentClaude, func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
			finds++
			if len(request.Members) != 1 || request.Members[0].Application != "oa-claude-integration" || request.SourceProgram != "" {
				t.Fatal(request)
			}
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "login-intent"}, nil
		}, func(intent string) (applications.AgentUpdateResult, error) {
			commits++
			if intent != "login-intent" {
				t.Fatal(intent)
			}
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: intent, Question: "actual-question", Reason: reason}, nil
		})
		if err != nil || !retained || result.Outcome != reason || result.Reason != reason || result.Intent != "login-intent" || result.Question != "actual-question" || finds != 1 || commits != 1 {
			t.Fatal(result, retained, finds, commits, err)
		}
	}
}

func TestAgentConnectionPrepareProjectsFreshSuppression(t *testing.T) {
	calls := 0
	var result agentHelperUpdateResult
	prepared, stopped, err := prepareAgentConnectionUpdate(applications.AgentUpdateRequest{}, func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		calls++
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "suppressed-intent"}, nil
	}, func(value agentHelperUpdateResult) { result = value })
	if err != nil || !stopped || calls != 1 || prepared.Intent != "suppressed-intent" || result.Outcome != "not_now" || result.Reason != "not_now" || result.Intent != "suppressed-intent" {
		t.Fatal(prepared, result, stopped, calls, err)
	}
}

func TestAgentConnectionCompletedIntentContinuesToSourceInventory(t *testing.T) {
	result, stopped, err := retainedAgentConnectionUpdateResult(setup.AgentClaude, func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "completed-undo"}, nil
	}, func(string) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "completed-undo", Reason: "updated"}, nil
	})
	if err != nil || stopped || result.Outcome != "updated" {
		t.Fatal(result, stopped, err)
	}
}

func TestAgentConnectionReviewStageRequiresExactServiceCapture(t *testing.T) {
	for _, mode := range []string{"missing", "member", "identity", "matching"} {
		prepared := applications.AgentUpdateResult{StagedImages: []applications.AgentUpdateImage{{Application: "oa-claude-integration", Program: "held-stage", FileKey: "held-key"}}}
		if mode == "missing" {
			prepared.StagedImages = nil
		}
		if mode == "member" {
			prepared.StagedImages[0].Application = "other-application"
		}
		calls := 0
		err := confirmAgentConnectionReviewStage(prepared, "oa-claude-integration", func(program, key string) error {
			calls++
			if mode == "identity" || program != "held-stage" || key != "held-key" {
				return errors.New("changed stage")
			}
			return nil
		})
		if (err == nil) != (mode == "matching") {
			t.Fatal(mode, calls, err)
		}
		if (mode == "missing" || mode == "member") && calls != 0 {
			t.Fatal("foreign stage adopted", calls)
		}
	}
}

func TestAgentConnectionReadyRetainedIntentResumesAllMembersWithoutPrepare(t *testing.T) {
	capture := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "login-or-review-intent", Reason: "update_ready", StagedImages: []applications.AgentUpdateImage{
		{Application: "oa-claude-integration", Program: "claude-stage", FileKey: "claude-key"},
		{Application: "oa-codex-integration", Program: "codex-stage", FileKey: "codex-key"},
	}}
	observed, stopped, err := retainedAgentConnectionUpdateResult(setup.AgentClaude, func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) { return capture, nil }, func(string) (applications.AgentUpdateResult, error) { return capture, nil })
	if err != nil || stopped || observed.ready == nil {
		t.Fatal(observed, stopped, err)
	}
	parties, err := agentConnectionRetainedParties(*observed.ready)
	if err != nil || len(parties) != 2 || !agentConnectionPartySelected(setup.AgentClaude, parties) || !agentConnectionPartySelected(setup.AgentCodex, parties) || agentConnectionPartySelected(setup.AgentOpenCode, parties) {
		t.Fatal(parties, err)
	}
	prepares := 0
	prepared, stopped, err := resolveAgentConnectionUpdate(applications.AgentUpdateRequest{}, observed.ready, func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		prepares++
		return applications.AgentUpdateResult{}, errors.New("new intent")
	}, nil)
	if err != nil || stopped || prepares != 0 || prepared.Intent != capture.Intent || !reflect.DeepEqual(prepared.StagedImages, capture.StagedImages) {
		t.Fatal(prepared, stopped, prepares, err)
	}
}

func TestAgentConnectionCommonRunnerResumesRetainedReviewForEveryConsumer(t *testing.T) {
	for _, consumer := range []string{"login", "opencode", "button", "login-pending"} {
		t.Run(consumer, func(t *testing.T) {
			only := setup.AgentParty("")
			parties := []setup.AgentParty{setup.AgentClaude, setup.AgentCodex}
			var report func(agentHelperUpdateResult)
			var result agentHelperUpdateResult
			if consumer == "button" {
				only = setup.AgentClaude
				report = func(value agentHelperUpdateResult) { result = value }
			}
			if consumer == "opencode" {
				only = setup.AgentOpenCode
				parties = []setup.AgentParty{setup.AgentOpenCode}
			}
			ops, counts := agentConnectionCommonRunnerFixture(t, parties, "complete", consumer == "login-pending")
			if err := runAgentConnectionUpdateBatchOwned(context.Background(), only, report, applications.AgentUpdateRequest{}, ops); err != nil {
				t.Fatal(err)
			}
			if counts.prepares != 0 || counts.published != 1 || counts.completed != 1 || counts.closed != len(parties) || counts.confirmed != len(parties) {
				t.Fatal(counts)
			}
			if consumer == "button" && result.Outcome != "updated" {
				t.Fatal(result)
			}
		})
	}
}

func TestAgentConnectionCommonRunnerDrainsIndependentKeptIntents(t *testing.T) {
	for _, mode := range []string{"login", "claude-only", "claude-declined"} {
		t.Run(mode, func(t *testing.T) {
			owners := map[setup.AgentParty]agentConnectionBatchOperations{}
			counts := map[setup.AgentParty]*agentConnectionRunnerCounts{}
			for _, party := range []setup.AgentParty{setup.AgentClaude, setup.AgentCodex} {
				op, count := agentConnectionCommonRunnerFixture(t, []setup.AgentParty{party}, "complete", false)
				find, commit := op.find, op.commit
				intent := "kept-" + string(party)
				op.find = func(r applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
					result, err := find(r)
					result.Intent = intent
					return result, err
				}
				op.commit = func(string) (applications.AgentUpdateResult, error) {
					result, err := commit("retained-review")
					result.Intent = intent
					if mode == "claude-declined" && party == setup.AgentClaude {
						result.Reason = "not_now"
					}
					return result, err
				}
				owners[party], counts[party] = op, count
			}
			partyFor := func(application string) setup.AgentParty {
				for party := range owners {
					if application == "oa-"+string(party)+"-integration" {
						return party
					}
				}
				return ""
			}
			ops := agentConnectionBatchOperations{
				find: func(r applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
					party := partyFor(r.Members[0].Application)
					if party == "" {
						return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
					}
					return owners[party].find(r)
				},
				prepare: func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
					t.Fatal("resume prepared a new question")
					return applications.AgentUpdateResult{}, nil
				},
				commit: func(intent string) (applications.AgentUpdateResult, error) {
					return owners[setup.AgentParty(strings.TrimPrefix(intent, "kept-"))].commit(intent)
				},
				inventory: func(ctx context.Context, selected ...setup.AgentParty) ([]agentConnectionBatchMember, error) {
					if len(selected) != 1 || owners[selected[0]].inventory == nil {
						t.Fatalf("independent roster widened: %v", selected)
					}
					return owners[selected[0]].inventory(ctx, selected...)
				},
				stage: func(ctx context.Context, m agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
					return owners[m.party].stage(ctx, m)
				},
				publish: func(ctx context.Context, stages []*agentConnectionBatchStage) error {
					return owners[partyFor(stages[0].previous.Application)].publish(ctx, stages)
				},
				ready: func(applications.AgentUpdateResult) error { return nil },
				complete: func(r applications.AgentUpdateResult) error {
					return owners[setup.AgentParty(strings.TrimPrefix(r.Intent, "kept-"))].complete(r)
				},
			}
			only := setup.AgentParty("")
			if mode == "claude-only" {
				only = setup.AgentClaude
			}
			if err := runAgentConnectionUpdateBatchOwned(context.Background(), only, nil, applications.AgentUpdateRequest{}, ops); err != nil {
				t.Fatal(err)
			}
			for party, count := range counts {
				want := 1
				if mode == "claude-only" && party == setup.AgentCodex || mode == "claude-declined" && party == setup.AgentClaude {
					want = 0
				}
				if count.published != want || count.completed != want || count.closed != want || count.prepares != 0 {
					t.Fatalf("%s: %+v; want %d publication/completion/close", party, count, want)
				}
			}
		})
	}
}

func TestAgentConnectionCommonRunnerAdmitsWholeCapturedRosterBeforePublication(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate-row", "extra", "duplicate-capture", "changed-key"} {
		t.Run(mode, func(t *testing.T) {
			ops, counts := agentConnectionCommonRunnerFixture(t, []setup.AgentParty{setup.AgentClaude, setup.AgentCodex}, mode, false)
			err := runAgentConnectionUpdateBatchOwned(context.Background(), setup.AgentClaude, nil, applications.AgentUpdateRequest{}, ops)
			if err == nil || counts.prepares != 0 || counts.published != 0 || counts.completed != 0 {
				t.Fatal(mode, counts, err)
			}
		})
	}
}

type agentConnectionRunnerCounts struct{ prepares, published, completed, closed, confirmed, commits int }

func agentConnectionCommonRunnerFixture(t *testing.T, parties []setup.AgentParty, mode string, pending bool) (agentConnectionBatchOperations, *agentConnectionRunnerCounts) {
	t.Helper()
	counts := &agentConnectionRunnerCounts{}
	capture := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "retained-review", Reason: "update_ready"}
	var members []agentConnectionBatchMember
	for _, party := range parties {
		name := "oa-" + string(party) + "-integration"
		capture.StagedImages = append(capture.StagedImages, applications.AgentUpdateImage{Application: name, Program: name + ".stage", FileKey: name + "-key"})
		members = append(members, agentConnectionBatchMember{party: party, program: string(party)})
	}
	if mode == "missing" {
		members = members[:1]
	}
	if mode == "duplicate-row" {
		members[1] = members[0]
	}
	if mode == "extra" {
		members = append(members, agentConnectionBatchMember{party: setup.AgentOpenCode, program: "opencode"})
	}
	if mode == "duplicate-capture" {
		capture.StagedImages[1] = capture.StagedImages[0]
	}
	ops := agentConnectionBatchOperations{
		find: func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
			// Exact Find of a newly reconstructed request without ReviewOf cannot
			// recover this intent. Only the actual member discovery can name it.
			if request.SourceProgram != "" || request.ReviewOf != "" {
				return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
			}
			return capture, nil
		},
		prepare: func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
			counts.prepares++
			return applications.AgentUpdateResult{}, errors.New("new Prepare replaced a retained review")
		},
		commit: func(intent string) (applications.AgentUpdateResult, error) {
			counts.commits++
			if intent != capture.Intent {
				t.Fatal(intent)
			}
			result := capture
			if pending && counts.commits <= 2 {
				result.Reason = "permission_pending"
			}
			if counts.published != 0 {
				result.Reason = "updated"
			}
			return result, nil
		},
		inventory: func(_ context.Context, selected ...setup.AgentParty) ([]agentConnectionBatchMember, error) {
			for _, party := range parties {
				if mode == "complete" && !agentConnectionPartySelected(party, selected) {
					t.Fatal("retained member omitted from inventory", party, selected)
				}
			}
			return members, nil
		},
		stage: func(_ context.Context, member agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
			name := "oa-" + member.program + "-integration"
			return &agentConnectionBatchStage{previous: setup.AgentEnrollmentCapture{Application: name, Revision: "kept-revision"}, source: "installed-source", program: name + ".stage", resume: true,
				confirm: func(program, key string) error {
					if mode == "changed-key" || program != name+".stage" || key != name+"-key" {
						return errors.New("stage identity differs")
					}
					counts.confirmed++
					return nil
				}, close: func() error { counts.closed++; return nil }}, nil
		},
		publish:  func(context.Context, []*agentConnectionBatchStage) error { counts.published++; return nil },
		ready:    func(applications.AgentUpdateResult) error { return nil },
		complete: func(applications.AgentUpdateResult) error { counts.completed++; return nil },
	}
	return ops, counts
}

func TestAgentConnectionCommonRunnerProjectsFreshPrepareSuppression(t *testing.T) {
	ops, counts := agentConnectionCommonRunnerFixture(t, []setup.AgentParty{setup.AgentClaude}, "complete", false)
	ops.find = func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
	}
	stage := ops.stage
	ops.stage = func(ctx context.Context, m agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
		s, err := stage(ctx, m)
		if s != nil {
			s.resume = false
		}
		return s, err
	}
	ops.prepare = func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		counts.prepares++
		if request.ReviewOf != "" {
			t.Fatal("background invented review", request)
		}
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "actual-suppressing-intent"}, nil
	}
	var result agentHelperUpdateResult
	err := runAgentConnectionUpdateBatchOwned(context.Background(), setup.AgentClaude, func(value agentHelperUpdateResult) { result = value }, applications.AgentUpdateRequest{}, ops)
	if err != nil || counts.prepares != 1 || counts.published != 0 || result.Outcome != "not_now" || result.Reason != "not_now" || result.Intent != "actual-suppressing-intent" {
		t.Fatal(result, counts, err)
	}
}

func TestAgentConnectionCommonRunnerReconcilesDeclinedReviewWithoutRenewing(t *testing.T) {
	ops, counts := agentConnectionCommonRunnerFixture(t, []setup.AgentParty{setup.AgentClaude}, "complete", false)
	declined := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "declined-review", StagedImages: []applications.AgentUpdateImage{{Application: "oa-claude-integration", Program: "oa-claude-integration.stage", FileKey: "oa-claude-integration-key"}}}
	ops.find = func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) { return declined, nil }
	ops.commit = func(string) (applications.AgentUpdateResult, error) { return declined, nil }
	ops.prepare = func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		counts.prepares++
		if request.ReviewOf != "" {
			t.Fatal("background renewed review", request)
		}
		return declined, nil
	}
	err := runAgentConnectionUpdateBatchOwned(context.Background(), setup.AgentClaude, nil, applications.AgentUpdateRequest{}, ops)
	if err != nil || counts.prepares != 1 || counts.published != 0 || counts.completed != 0 {
		t.Fatal(counts, err)
	}
}

func TestAgentConnectionWindowsUpdateRouteUsesBatch(t *testing.T) {
	for _, party := range []setup.AgentParty{setup.AgentCodex, setup.AgentClaude} {
		calls := 0
		p := &servicePanel{}
		mux := http.NewServeMux()
		p.agentHelperUpdateRoutesWithBatch(mux, "fixture", func(_ context.Context, got setup.AgentParty) (agentHelperUpdateResult, error) {
			calls++
			if got != party {
				t.Fatal(got, party)
			}
			return agentHelperUpdateResult{Outcome: "permission_pending"}, nil
		}, nil)
		r := httptest.NewRequest(http.MethodPost, "/connections/"+string(party)+"/update", strings.NewReader(`{"revision":"saved"}`))
		r.Header.Set("X-Panel-Key", "fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK || calls != 1 || !strings.Contains(w.Body.String(), `"permission_pending"`) {
			t.Fatal(w.Code, w.Body.String(), calls)
		}
		// No controller exists in this fixture: the legacy image swap path would
		// attempt runtime/vendor discovery rather than reach the injected owner.
		if p.codexConnections != nil || p.claudeConnections != nil {
			t.Fatal("legacy update controller constructed")
		}
	}
}

func TestAgentConnectionWindowsButtonNominatesActualDeclinedIntent(t *testing.T) {
	p := &servicePanel{}
	mux := http.NewServeMux()
	reviews := 0
	p.agentHelperUpdateRoutesWithBatch(mux, "fixture", func(context.Context, setup.AgentParty) (agentHelperUpdateResult, error) {
		return agentHelperUpdateResult{Outcome: "not_now", Intent: "retained-suppression"}, nil
	}, func(_ context.Context, party setup.AgentParty, request applications.AgentUpdateRequest) (agentHelperUpdateResult, error) {
		reviews++
		if party != setup.AgentClaude || request.ReviewOf != "retained-suppression" || request.UndoOf != "" || len(request.Members) != 0 || request.SourceProgram != "" {
			t.Fatal(party, request)
		}
		return agentHelperUpdateResult{Outcome: "permission_pending", Intent: "new-native-intent", Question: "new-native-question"}, nil
	})
	r := httptest.NewRequest(http.MethodPost, "/connections/claude/update", strings.NewReader(`{"revision":"saved"}`))
	r.Header.Set("X-Panel-Key", "fixture")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || reviews != 1 || !strings.Contains(w.Body.String(), `"new-native-question"`) {
		t.Fatal(w.Code, w.Body.String(), reviews)
	}
}

func TestAgentConnectionBatchWaitsForCompanionReadyAndNativeReadback(t *testing.T) {
	var order []string
	commits := 0
	commit := func(intent string) (applications.AgentUpdateResult, error) {
		if intent != "intent" {
			t.Fatal(intent)
		}
		commits++
		if commits == 1 {
			order = append(order, "native-ready")
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
		}
		order = append(order, "native-updated")
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated"}, nil
	}
	err := finishAgentConnectionUpdateBatch(context.Background(), "intent", commit, func() error { order = append(order, "publish"); return nil }, func(result applications.AgentUpdateResult) error {
		if result.Reason != "updated" {
			t.Fatal("companion ran before native enrollment")
		}
		order = append(order, "companion-ready")
		return nil
	})
	want := []string{"native-ready", "publish", "native-updated", "companion-ready", "native-updated"}
	if err != nil || !reflect.DeepEqual(order, want) {
		t.Fatal(order, err)
	}
}

func TestAgentConnectionRetainedApprovalRepairsFailedCompanionWithoutPublish(t *testing.T) {
	updated := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated"}
	commit := func(intent string) (applications.AgentUpdateResult, error) {
		if intent != "same-intent" {
			t.Fatal(intent)
		}
		return updated, nil
	}
	readyCalls := 0
	ready := func(applications.AgentUpdateResult) error {
		readyCalls++
		if readyCalls == 1 {
			return errors.New("loaded cache remains stale")
		}
		return nil
	}
	err := finishAgentConnectionUpdateBatch(context.Background(), "same-intent", commit, func() error { t.Fatal("completed native image republished"); return nil }, ready)
	if err == nil {
		t.Fatal("companion failure claimed success")
	}
	err = resumeAgentConnectionUpdates(func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "same-intent"}, nil
	}, commit, ready)
	if err != nil || readyCalls != 2 {
		t.Fatal("retained approval did not resume exact companion owner", readyCalls, err)
	}
}

func TestClaudeConnectionReadyRequiresActualLoadedCache(t *testing.T) {
	for _, cache := range []string{"current", "update"} {
		t.Run(cache, func(t *testing.T) {
			image := &claudePanelFixtureImage{}
			installs := 0
			c := &claudePanelConnections{userMod: true, state: func() (string, error) { return t.TempDir(), nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "watch-permission" {
					return []byte(`{"outcome":"page","state":"owned"}`), nil
				}
				if args[0] == "plugin" && args[2] == "install" {
					installs++
					return []byte(`{"outcome":"saved"}`), nil
				}
				if args[0] == "plugin" && args[2] == "status" {
					return []byte(`{"outcome":"page","state":"` + cache + `"}`), nil
				}
				t.Fatal(args)
				return nil, nil
			}}
			err := readyClaudeConnectionRows(context.Background(), c, image, []claudeSavedConnection{{Kind: "user", Registration: "saved", Program: image.Program(), ProgramKey: image.FileKey(), ModState: "update"}})
			if (err == nil) != (cache == "current") || installs != 1 {
				t.Fatal("CLI exit zero substituted for loaded cache proof", cache, installs, err)
			}
		})
	}
}

func TestAgentConnectionBatchBusyRetainsSameIntent(t *testing.T) {
	err := finishAgentConnectionUpdateBatch(context.Background(), "same-intent", func(string) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
	}, func() error { return setup.ErrAgentUpdateRestartRequired })
	var pending *agentConnectionUpdatePending
	if !errors.As(err, &pending) || pending.Intent != "same-intent" || !pending.RestartRequired {
		t.Fatal(err)
	}
}

func TestAgentConnectionBatchChangedInputStopsWithoutRestartClaim(t *testing.T) {
	err := finishAgentConnectionUpdateBatch(context.Background(), "same-intent", func(string) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
	}, func() error { return errors.New("changed staged image") })
	var pending *agentConnectionUpdatePending
	if err == nil || errors.As(err, &pending) {
		t.Fatal(err)
	}
}

func TestAgentConnectionFreshRunRecoversPublishBeforeLostFinalCommit(t *testing.T) {
	published, enrolled := false, false
	lostFinal := true
	commits := 0
	commit := func(intent string) (applications.AgentUpdateResult, error) {
		if intent != "retained-service-intent" {
			t.Fatal(intent)
		}
		commits++
		if !published {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
		}
		if lostFinal {
			return applications.AgentUpdateResult{}, errors.New("final Commit reply lost")
		}
		enrolled = true
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated"}, nil
	}
	err := finishAgentConnectionUpdateBatch(context.Background(), "retained-service-intent", commit, func() error { published = true; return nil })
	if err == nil || !published || enrolled {
		t.Fatal("missing reproduced published/uncommitted edge", published, enrolled, err)
	}
	lostFinal = false
	// New invocation has no staged object or old enrollment capture. Its
	// readonly service lookup discovers the retained batch by registered app.
	finds := 0
	err = resumeAgentConnectionUpdates(func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		finds++
		if len(request.Members) != 1 || request.Members[0].ExpectedRevision != "" || request.Members[0].StagedProgram != "" || request.SourceProgram != "" {
			t.Fatal(request)
		}
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "retained-service-intent"}, nil
	}, commit)
	if err != nil || !enrolled || commits != 5 || finds != len(agentHelperParties) {
		t.Fatal("fresh invocation did not finish retained service proof", enrolled, commits, finds, err)
	}
}

func TestAgentConnectionRecoveryNeverClaimsUpdatedFromCurrentBytes(t *testing.T) {
	for _, reason := range []string{"changed_staged_image", "changed_registration"} {
		err := resumeAgentConnectionUpdates(func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: "intent"}, nil
		}, func(string) (applications.AgentUpdateResult, error) {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeConflict, Reason: reason}, nil
		})
		if err == nil {
			t.Fatal("current bytes bypassed final service proof", reason)
		}
	}
}

func TestAgentConnectionBatchRetriesLostFinalCommitWithoutRepublishing(t *testing.T) {
	commits, published := 0, 0
	err := finishAgentConnectionUpdateBatch(context.Background(), "same-intent", func(string) (applications.AgentUpdateResult, error) {
		commits++
		if commits == 1 {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
		}
		if commits == 2 {
			return applications.AgentUpdateResult{}, errors.New("transport interrupted")
		}
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated"}, nil
	}, func() error { published++; return nil })
	if err != nil || published != 1 || commits != 3 {
		t.Fatal(published, commits, err)
	}
}

func TestAgentConnectionRecoveryRefusesFailedLookup(t *testing.T) {
	committed := false
	err := resumeAgentConnectionUpdates(func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeForbidden, Reason: "operator"}, nil
	}, func(string) (applications.AgentUpdateResult, error) {
		committed = true
		return applications.AgentUpdateResult{}, nil
	})
	if err == nil || committed {
		t.Fatal("failed lookup bypassed recovery authority", err, committed)
	}
}

// Owner R14, 2026-11-09: a copy staged before a refused Prepare blocked every
// later Update click with "existing staged connection has no service-owned
// update". The click replaces that copy and asks the update question.
func TestAgentConnectionOrphanedStagedCopyIsReplacedAndAsks(t *testing.T) {
	parties := []setup.AgentParty{setup.AgentClaude, setup.AgentCodex, setup.AgentOpenCode}
	ops, counts := agentConnectionCommonRunnerFixture(t, parties, "complete", false)
	ops.find = func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
	}
	discarded := map[string]bool{}
	stage := ops.stage
	ops.stage = func(ctx context.Context, m agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
		s, err := stage(ctx, m)
		if s == nil {
			return s, err
		}
		name := s.previous.Application
		s.resume = !discarded[name]
		s.discard = func() error { discarded[name] = true; counts.closed++; return nil }
		return s, err
	}
	var asked applications.AgentUpdateRequest
	fresh := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "permission_pending", Intent: "fresh-intent", Question: "fresh-question"}
	for _, party := range parties {
		name := "oa-" + string(party) + "-integration"
		fresh.StagedImages = append(fresh.StagedImages, applications.AgentUpdateImage{Application: name, Program: name + ".stage", FileKey: name + "-key"})
	}
	ops.prepare = func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		counts.prepares++
		asked = request
		return fresh, nil
	}
	ops.commit = func(intent string) (applications.AgentUpdateResult, error) {
		if intent != fresh.Intent {
			t.Fatal("commit named another intent", intent)
		}
		return fresh, nil
	}
	var result agentHelperUpdateResult
	err := runAgentConnectionUpdateBatchOwned(context.Background(), "", func(value agentHelperUpdateResult) { result = value }, applications.AgentUpdateRequest{}, ops)
	if err != nil || counts.prepares != 1 || result.Outcome != "permission_pending" || result.Question != "fresh-question" {
		t.Fatal("orphaned staged copy stopped the update question", result, counts, err)
	}
	if len(discarded) != len(parties) || len(asked.Members) != len(parties) {
		t.Fatal("not every orphaned copy was replaced", discarded, asked)
	}
	for _, member := range asked.Members {
		if !discarded[member.Application] || member.StagedProgram != member.Application+".stage" || member.ExpectedRevision != "kept-revision" {
			t.Fatal("Prepare named a stale stage", member)
		}
	}
}

func TestAgentConnectionPrepareRefusalIsLogged(t *testing.T) {
	request := applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: "oa-claude-integration"}, {Application: "oa-opencode-integration"}}}
	for _, quiet := range []applications.AgentUpdateResult{
		{Outcome: applications.OutcomeApplied, Reason: "permission_pending", Intent: "asked"},
		{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "declined"},
	} {
		if fields := agentConnectionPrepareRefusalFields(request, quiet, nil); fields != nil {
			t.Fatal("question logged as refusal", quiet, fields)
		}
	}
	fields := agentConnectionPrepareRefusalFields(request, applications.AgentUpdateResult{Outcome: applications.OutcomeConflict, Reason: "registration"}, nil)
	if fields["outcome"] != "conflict" || fields["reason"] != "registration" || fields["connection.members"] != "oa-claude-integration,oa-opencode-integration" {
		t.Fatal(fields)
	}
	fields = agentConnectionPrepareRefusalFields(request, applications.AgentUpdateResult{}, errors.New("pipe closed"))
	if fields["outcome"] != "error" || fields["error"] != "pipe closed" {
		t.Fatal(fields)
	}
}

// A transient lookup failure keeps an unclaimed staged copy for the next run.
func TestAgentConnectionStagedCopySurvivesUnavailableLookup(t *testing.T) {
	ops, counts := agentConnectionCommonRunnerFixture(t, []setup.AgentParty{setup.AgentClaude}, "complete", false)
	ops.find = func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		if request.SourceProgram == "" {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
		}
		return applications.AgentUpdateResult{Outcome: applications.OutcomeUnavailable, Reason: "state"}, nil
	}
	discarded := 0
	stage := ops.stage
	ops.stage = func(ctx context.Context, m agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
		s, err := stage(ctx, m)
		if s != nil {
			s.discard = func() error { discarded++; return nil }
		}
		return s, err
	}
	err := runAgentConnectionUpdateBatchOwned(context.Background(), "", nil, applications.AgentUpdateRequest{}, ops)
	if err == nil || !strings.Contains(err.Error(), "preserved") || discarded != 0 || counts.prepares != 0 {
		t.Fatal("unavailable lookup discarded a staged copy", err, discarded, counts)
	}
}
