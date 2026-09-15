package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
)

// questionAction is one operator decision the person made in the panel.
type questionAction struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Option string `json:"option"`
}

func questionWord(s string) bool {
	return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// questions lists, answers and retires application questions through the
// resolved operator service. The service's operator policy decides whether this
// panel may act; its typed outcomes are returned unchanged for display.
// Resolution and transport failures report unavailability without a local
// question store.
func (p *servicePanel) questions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var action questionAction
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&action); err != nil {
			http.Error(w, "invalid question action", 400)
			return
		}
		switch {
		case action.Action == "answer" && questionWord(action.ID) && questionWord(action.Option):
		case action.Action == "retire" && questionWord(action.ID) && action.Option == "":
		default:
			http.Error(w, "invalid question action", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid question cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		panelError(w, err)
		return
	}
	var result any
	switch action.Action {
	case "":
		result, err = operator.ListQuestionsContext(ctx, r.URL.Query().Get("cursor"), 16)
	case "answer":
		result, err = operator.AnswerQuestionContext(ctx, action.ID, action.Option)
	case "retire":
		result, err = operator.RetireQuestionContext(ctx, action.ID)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
