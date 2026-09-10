package main

import (
	"strings"
	"testing"
)

func TestNoSelectionIsTheWholeDefinition(t *testing.T) {
	def := exampleDefinition(t)
	got, err := selected(def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != def {
		t.Fatal("an absent selection narrowed the definition")
	}
}

func TestEverySurfaceSelectedEqualsNoSelection(t *testing.T) {
	def := exampleDefinition(t)
	got, err := selected(def, surfaces(def))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if b.emit(got) != b.emit(def) {
			t.Fatalf("the %s backend emits different bytes when every surface is named", b.lang)
		}
	}
}

func TestAnUnclosedSelectionIsRefused(t *testing.T) {
	def := exampleDefinition(t)
	_, err := selected(def, []string{def.Proto.Name})
	if err == nil {
		t.Fatal("a protocol without its envelope was accepted")
	}
	for _, want := range []string{def.Proto.Request, def.Proto.Response, def.Proto.Verdicts} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %s: %v", want, err)
		}
	}
}

func TestADocumentWithoutItsVocabularyIsRefused(t *testing.T) {
	def := exampleDefinition(t)
	only := []string{def.Document}
	for _, st := range def.Structs {
		if st.Name != def.Document {
			only = append(only, st.Name)
		}
	}
	if _, err := selected(def, only); err == nil {
		t.Fatal("a document codec without its derivation was accepted")
	}
}

func TestAnUnknownSurfaceIsRefused(t *testing.T) {
	if _, err := selected(exampleDefinition(t), []string{"Recrod"}); err == nil {
		t.Fatal("a misspelt surface was accepted, which would have narrowed the artefact in silence")
	}
}

// A backend that stops emitting the code a check reads must not thereby pass
// the check. The refusal words are declared once for the whole definition, so
// the narrowest selection still owes every one of them.
func TestASelectionCannotHideAnUnreachableRefusal(t *testing.T) {
	def := exampleDefinition(t)
	narrow, err := selected(def, []string{def.Proto.Name, def.Proto.Request, def.Proto.Response, def.Proto.Verdicts})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if !b.emitsCode {
			continue
		}
		body := b.emit(narrow)
		if err := checkWords(emitted{def: narrow, lang: b.lang, body: body, whole: body}); err == nil {
			t.Fatalf("the %s backend answered for words its selection cannot reach", b.lang)
		}
		if err := checkWords(emitted{def: narrow, lang: b.lang, body: body, whole: b.emit(def)}); err != nil {
			t.Fatalf("the %s backend was refused for a word the whole definition reaches: %v", b.lang, err)
		}
	}
}
