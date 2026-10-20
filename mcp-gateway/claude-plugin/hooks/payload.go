package hooks

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
)

//go:embed register.js
var register []byte

//go:embed messages-tools.json
var messagesCatalogue []byte

type WakeTemplates struct {
	First      string `json:"first"`
	Repeat     string `json:"repeat"`
	Anonymous  string `json:"anonymous"`
	Unticketed string `json:"unticketed"`
	Legacy     string `json:"legacy"`
	// Asks is the fixed pointer for an answerable Asks occurrence (ASK-R14).
	Asks           string `json:"asks"`
	Summary        string `json:"summary"`
	AnswerableAsks string `json:"answerable_asks"`
	WaitingAsks    string `json:"waiting_asks"`
	AwaitedReplies string `json:"awaited_replies"`
}

// WakePrompt reads the catalogue producer's generated metadata. Both the
// compiled gateway and the projected Claude module consume these exact bytes.
func WakePrompt() (WakeTemplates, error) {
	var catalogue struct {
		WakePrompt WakeTemplates `json:"wake_prompt"`
	}
	if err := json.Unmarshal(messagesCatalogue, &catalogue); err != nil {
		return WakeTemplates{}, err
	}
	t := catalogue.WakePrompt
	if t.First == "" || t.Repeat == "" || t.Anonymous == "" || t.Unticketed == "" || t.Legacy == "" || t.Asks == "" ||
		t.Summary == "" || t.AnswerableAsks == "" || t.WaitingAsks == "" || t.AwaitedReplies == "" {
		return WakeTemplates{}, errors.New("generated wake prompt missing")
	}
	return catalogue.WakePrompt, nil
}

// ProjectRegister embeds generated metadata in plain Mod source, requiring
// no runtime file read or module-loader extension in Claude.
func ProjectRegister(source []byte) ([]byte, error) {
	templates, err := WakePrompt()
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(templates)
	if err != nil {
		return nil, err
	}
	return bytes.Replace(source, []byte("__OA_MESSAGES_WAKE_PROMPT__"), value, 1), nil
}

// Register returns the producer's compiled module source.
func Register() []byte { return append([]byte(nil), register...) }
