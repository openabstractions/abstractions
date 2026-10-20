package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/remote"
)

func TestPairingApprovedDirectionalCopy(t *testing.T) {
	q, ok := asks.Find(asks.PairingKey)
	if !ok {
		t.Fatal("runtime pairing question missing")
	}
	name := strings.Repeat("h", 64)
	preview := mwire.MachineAgreement{PeerKey: strings.Repeat("a", 64), Check: "123456", Transcript: strings.Repeat("b", 64), Given: mwire.MachineDirection{Kind: mwire.MachineScopeKindOneHostInference, Host: name, ServiceAddress: "[2001:db8:ffff:ffff:ffff:ffff:ffff:ffff]:65535"}, Received: mwire.MachineDirection{Kind: mwire.MachineScopeKindNone}}
	before, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	slots := pairingQuestionSlots(preview, strings.Repeat("c", 64), "2026-10-25T12:00:00Z")
	text, err := q.Render(asks.Ask{Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"Pair these computers?", "Check that both computers show 123456.", "This computer allows: Completion and embedding requests through selected model server " + name + ".", "Names/details of ALL model servers registered for that user account; OA connections needed for those operations.", "The other computer allows: No access.", "Permissions stay until removed in the Panel.", "Replacing the model server registered under " + name + " keeps this permission.", "Each computer approves separately.", preview.PeerKey, preview.Given.ServiceAddress} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("approved text missing %q: %s", fragment, text)
		}
	}
	if strings.Count(text, "Replacing the model server") != 1 {
		t.Fatal("empty reverse direction acquired a replacement permission")
	}
	slots["given"] = "mutated display copy"
	after, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || pairingQuestionSlots(preview, "local", "later")["given"] == slots["given"] {
		t.Fatal("display slots changed frozen facts")
	}
	if yes, ok := q.Option("once"); !ok || !yes.Yes || yes.Kept {
		t.Fatal("positive option changed authority")
	}
	if no, ok := q.Option("refuse"); !ok || no.Yes || no.Kept {
		t.Fatal("Cancel changed the existing negative decision")
	}
}

// These are the runtime interpretation controls. The separate actual /2 TLS
// receivers own exporter/transcript authentication; this view grants nothing.
func TestRuntimePairingCanonicalDirections(t *testing.T) {
	a, _ := pairingStatePeer(t)
	b, _ := pairingStatePeer(t)
	keys := remote.PairKeys{Role: 1, LocalKey: pairingStateAdmission(a).Key, PeerKey: pairingStateAdmission(b).Key}
	i := mwire.MachinePreparation{Request: mwire.MachineScopeKindOneHostInference, Offer: mwire.MachineScopeKindNone, BindingName: "machine-b"}
	r := mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindOneHostInference, OfferHost: strings.Repeat("h", 64), NormalServiceAddress: "[2001:db8:ffff:ffff:ffff:ffff:ffff:ffff]:65535", BindingName: strings.Repeat("b", 64)}
	ip, err := preparePairingProposal(i, keys)
	if err != nil {
		t.Fatal(err)
	}
	reverse := remote.PairKeys{Role: 2, LocalKey: keys.PeerKey, PeerKey: keys.LocalKey}
	rp, err := preparePairingProposal(r, reverse)
	if err != nil {
		t.Fatal(err)
	}
	if len(rp) > remote.PairingProposalLimit {
		t.Fatal("initial fixed three-rule maximum exceeds its admitted cap")
	}
	view := remote.PairView{Role: 1, LocalKey: keys.LocalKey, PeerKey: keys.PeerKey, InitiatorProposal: ip, ResponderProposal: rp}
	_, local, received, err := pairingAgreement(view)
	if err != nil || len(local.Rules) != 0 || len(received.Rules) != 3 {
		t.Fatal("directional effects implicitly reciprocated")
	}
	for _, kind := range []string{"unknown", "duplicate", "whitespace", "role", "key", "semantics", "rule", "permit", "scope", "extra-rule"} {
		t.Run(kind, func(t *testing.T) {
			bad := bytes.Clone(rp)
			var proposal pairingProposal
			if err := json.Unmarshal(bad, &proposal); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown":
				bad = append(append(bytes.Clone(bad[:len(bad)-1]), []byte(`,"unknown":true`)...), '}')
			case "duplicate":
				bad = append(append(bytes.Clone(bad[:len(bad)-1]), []byte(`,"version":1`)...), '}')
			case "whitespace":
				bad = append([]byte(" "), bad...)
			default:
				switch kind {
				case "role":
					proposal.Role = 1
				case "key":
					proposal.InitiatorKey = proposal.ResponderKey
				case "semantics":
					proposal.Semantics = strings.Repeat("0", 64)
				case "rule":
					proposal.Rules[0].Resource = "another-account"
				case "permit":
					proposal.Rules[0].Permit = false
				case "scope":
					proposal.Offer = "all"
				case "extra-rule":
					proposal.Rules = append(proposal.Rules, proposal.Rules[0])
				}
				bad, err = json.Marshal(proposal)
				if err != nil {
					t.Fatal(err)
				}
			}
			changed := view
			changed.ResponderProposal = bad
			if _, _, _, err := pairingAgreement(changed); err == nil {
				t.Fatal("noncanonical/changed receiving meaning accepted")
			}
		})
	}
	mismatch := view
	none := r
	none.Offer = mwire.MachineScopeKindNone
	none.OfferHost = ""
	none.NormalServiceAddress = ""
	mismatch.ResponderProposal, err = preparePairingProposal(none, reverse)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := pairingAgreement(mismatch); err == nil {
		t.Fatal("requested direction did not match receiving offer")
	}
}
