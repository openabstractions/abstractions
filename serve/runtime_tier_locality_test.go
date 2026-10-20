package main

import (
	"testing"

	router "github.com/openabstractions/abstraction-router/go"
)

func TestAnswererRoutineTierRequiresOnMachineEndpoint(t *testing.T) {
	declared := router.ProbeOllama(router.ProbeEnv{Getenv: func(name string) string {
		if name == "OLLAMA_HOST" {
			return "http://192.0.2.10:11434"
		}
		return ""
	}})
	remoteOllama := router.Ollama(declared.Base)
	remoteOperator := router.OpenAICompatible("operator-remote", "https://192.0.2.11:443", "/v1", []string{"model"})
	loopback := router.Ollama("http://127.0.0.1:11434")
	loopback.Name = "loopback"
	var a runtimeAsks
	a.useTierFacts(func() []*router.Host { return []*router.Host{remoteOllama, remoteOperator, loopback} }, nil)
	if a.tierFacts.LocalServer("ollama") || a.tierFacts.LocalServer("operator-remote") {
		t.Fatal("remote HTTP host was classified as routine on-machine inference")
	}
	if !a.tierFacts.LocalServer("loopback") {
		t.Fatal("literal loopback host was not classified on-machine")
	}
}
