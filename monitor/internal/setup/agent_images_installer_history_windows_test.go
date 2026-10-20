package setup

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
)

func TestInstallerPredecessorReplacementUsesRecordedBytesWithoutEnrollment(t *testing.T) {
	for _, mode := range []string{"recorded", "foreign", "enrolled", "unavailable", "enrolled-before-publish", "other-application"} {
		t.Run(mode, func(t *testing.T) {
			stage := batchImageFixtureAt(t, "", AgentOpenCode)
			root := filepath.Dir(stage.Old.Program())
			target := stage.Old.Program()
			oldBytes, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			pin, err := pinAgentImage(t.Context(), stage.Old.image)
			if err != nil {
				t.Fatal(err)
			}
			history := installerGatewayHistory{Schema: 1, Predecessors: []installerGatewayPredecessor{{GatewayBytes: pin.Bytes, GatewayHash: hex.EncodeToString(pin.Hash[:])}}}
			if mode == "foreign" {
				history.Predecessors[0].GatewayHash = "unknown"
			}
			stage.Old.Close()
			stage.Image.Close()
			reads := 0
			read := func(context.Context) (applications.AgentEnrollmentResult, error) {
				reads++
				state := applications.AgentEnrollmentStateNone
				if mode == "enrolled" || mode == "enrolled-before-publish" && reads > 1 {
					state = applications.AgentEnrollmentStateEnrolled
				}
				if mode == "unavailable" {
					return applications.AgentEnrollmentResult{Outcome: applications.OutcomeForbidden}, nil
				}
				application := "oa-opencode-integration"
				if mode == "other-application" {
					application = "oa-codex-integration"
				}
				return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: application, State: state}}, nil
			}
			image, err := replaceUnenrolledInstallerImage(t.Context(), root, AgentOpenCode, "oa-opencode-integration", stage.Source, history, read)
			if mode == "recorded" {
				if err != nil || image == nil {
					t.Fatal(err)
				}
				defer image.Close()
				if err := sameInstalledAgentImage(t.Context(), image, stage.Source); err != nil {
					t.Fatal(err)
				}
				result, _ := read(t.Context())
				if result.Enrollment.State != applications.AgentEnrollmentStateNone {
					t.Fatal("physical replacement enrolled image")
				}
			} else {
				if err == nil || image != nil {
					t.Fatal("unknown/enrolled image replaced", mode)
				}
				current, readErr := os.ReadFile(target)
				if readErr != nil || string(current) != string(oldBytes) {
					t.Fatal("refusal changed current image", readErr)
				}
			}
		})
	}
}

func TestInstallerPredecessorReplacementRefusesOtherParties(t *testing.T) {
	for _, party := range []AgentParty{AgentClaude, AgentCodex, ""} {
		image, err := ReplaceUnenrolledInstallerAgentImage(t.Context(), new(client.Machine), party)
		if image != nil || err == nil || err.Error() != "installer replacement target unavailable" {
			t.Fatalf("party %q reached installer replacement: %v", party, err)
		}
	}
}

func TestInstallerPredecessorResourceRefusesUnknownAuthority(t *testing.T) {
	for _, body := range []string{"", `{"schema":1,"predecessors":[{"gateway_sha256":"observed-only"}]}`, `{"schema":1,"schema":1,"predecessors":[]}`} {
		if _, err := decodeInstallerGatewayHistory([]byte(body)); err == nil {
			t.Fatal("untrusted or noncanonical predecessor data accepted", body)
		}
	}
	if _, err := decodeInstallerGatewayHistory([]byte(`{"predecessors":[],"schema":1}`)); err != nil {
		t.Fatal(err)
	}
}
