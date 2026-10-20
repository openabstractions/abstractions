package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

type choiceAction struct {
	Action string                  `json:"action"`
	Shown  *askclient.ChoiceRecord `json:"shown"`
	Option string                  `json:"option,omitempty"`
}

// Ordinary choices use their own closed API and full shown-record comparison.
// Rights rules, permission delegation and legacy question responses remain in
// their existing handlers. The service performs the atomic authority check.
// sharedChoicePage is one page of app choices with the IDs of the pending
// ones their apps shared with an assistant (asks ASK-A12).
type sharedChoicePage struct {
	askclient.ChoicePage
	Shared []string `json:"Shared"`
}

// sharedChoiceIDs reads which pending choices on page an assistant may
// answer. A read that fails, or a runtime too old to answer it, marks none.
func sharedChoiceIDs(ctx context.Context, operator interface {
	ReadChoiceDetailsContext(context.Context, string) (askclient.ChoiceDetails, error)
}, page askclient.ChoicePage) []string {
	shared := []string{}
	for _, record := range page.Records {
		if record.State != askclient.ChoiceStatePending {
			continue
		}
		if details, err := operator.ReadChoiceDetailsContext(ctx, record.ID); err == nil && len(details.AssistantOptions) > 0 {
			shared = append(shared, record.ID)
		}
	}
	return shared
}

func (p *servicePanel) choices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var action choiceAction
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		var extra any
		if err := decoder.Decode(&action); err != nil || decoder.Decode(&extra) != io.EOF ||
			action.Shown == nil || !asks.ValidChoiceRecord(*action.Shown) || action.Shown.State != askclient.ChoiceStatePending ||
			(action.Action != "answer" && action.Action != "dismiss") ||
			(action.Action == "answer" && !asks.ValidChoiceOptionID(action.Option)) ||
			(action.Action == "dismiss" && action.Option != "") {
			http.Error(w, "invalid app choice action", http.StatusBadRequest)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid app choice cursor", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	if r.Method == http.MethodGet {
		var page askclient.ChoicePage
		if page, err = operator.ListChoicesContext(ctx, r.URL.Query().Get("cursor"), 16); err == nil {
			result = sharedChoicePage{ChoicePage: page, Shared: sharedChoiceIDs(ctx, operator, page)}
		}
	} else {
		shown := *action.Shown
		read, readErr := operator.ReadChoiceContext(ctx, shown.ID)
		err = readErr
		decision := askclient.ChoiceDecision{Outcome: askclient.ChoiceDecisionOutcomeConflict}
		if err == nil {
			switch {
			case read.Outcome == askclient.OperatorQuestionOutcomeUnknown:
				decision.Outcome = askclient.ChoiceDecisionOutcomeUnknown
			case read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil:
				decision.Outcome = askclient.ChoiceDecisionOutcomeUnavailable
				if read.Outcome == askclient.OperatorQuestionOutcomeForbidden {
					decision.Outcome = askclient.ChoiceDecisionOutcomeForbidden
				}
				if read.Outcome == askclient.OperatorQuestionOutcomeInvalid {
					decision.Outcome = askclient.ChoiceDecisionOutcomeInvalid
				}
			case !sameChoiceContent(shown, *read.Record):
			case !choicePendingAt(*read.Record, time.Now()):
				if read.Record.State == askclient.ChoiceStateExpired || time.Now().UnixMilli() >= read.Record.Request.PendingUntilMs {
					decision.Outcome = askclient.ChoiceDecisionOutcomeExpired
				}
			default:
				p.logAction(ctx, "choice."+action.Action, map[string]string{"choice.id": shown.ID})
				if action.Action == "dismiss" {
					decision, err = operator.DismissChoiceContext(ctx, shown.ID)
				} else {
					decision, err = operator.AnswerChoiceContext(ctx, shown.ID, action.Option)
				}
			}
		}
		result = decision
	}
	if ordinaryUnsupported(err) {
		panelJSON(w, map[string]string{"Outcome": "unsupported"})
		return
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
