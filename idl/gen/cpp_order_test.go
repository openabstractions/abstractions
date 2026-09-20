package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCppCompleteInferenceHeaderCompiles(t *testing.T) {
	raw, err := os.ReadFile(productionFile(t, "abstraction-inference/inference.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Skip("C++ requires CXX or g++")
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.h", genCpp(s))
	writeNamespaceFile(t, dir, "probe.cpp", `#include "rec.h"`+"\n")
	args := []string{"-std=c++17", "-fsyntax-only", "probe.cpp"}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/Zs", "probe.cpp"}
	}
	c := exec.Command(cxx, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("generated inference header: %v\n%s", err, out)
	}
}

func TestCppStructOrderSkipsImportedAliases(t *testing.T) {
	s, err := loadDefinition(productionFile(t, "abstraction-model/model.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	src := genCpp(importCarriers(s))
	if strings.Contains(src, "struct OAImported") {
		t.Fatal("imported aliases leaked synthetic struct declarations")
	}
	if !strings.Contains(src, "using OAImported0 =") || !strings.Contains(src, "using OAImported1 =") {
		t.Fatal("imported model dependencies lost their C++ aliases")
	}
}
