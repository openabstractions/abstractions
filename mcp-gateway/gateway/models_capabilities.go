package gateway

import (
	"strconv"
	"strings"

	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

// capabilityLine is one alias's capabilities in the one format every entry
// of oa_models_list shares (architect ruling 2026-11-12, model
// capabilities), prefixed by its host: "local" on this machine, else the
// hosted provider's name. A fact no source states is left out.
//
//	local qwen3.6-35b-a3b: chat · in text+image · tools · 256k (served 32k) · reasoning none, low, medium, high (budget, default medium) · ext llama.cpp/enable_thinking
func capabilityLine(a routerwire.Alias) string {
	host := "local"
	if a.Hosted {
		host = a.Host
	}
	var facts []string
	if len(a.Profiles) > 0 {
		facts = append(facts, strings.Join(a.Profiles, "+"))
	}
	c := a.Capabilities
	if c != nil && len(c.Input) > 0 {
		facts = append(facts, "in "+strings.Join(c.Input, "+"))
	}
	if c != nil && len(c.Output) > 0 && (len(c.Output) != 1 || c.Output[0] != "text") {
		facts = append(facts, "out "+strings.Join(c.Output, "+"))
	}
	if c != nil && c.Tools != nil && *c.Tools {
		facts = append(facts, "tools")
	}
	if c != nil && c.StructuredOutput != nil && *c.StructuredOutput {
		facts = append(facts, "json")
	}
	if a.ContextLength > 0 {
		context := tokens(a.ContextLength)
		if a.EffectiveContextLength > 0 && a.EffectiveContextLength != a.ContextLength {
			context += " (served " + tokens(a.EffectiveContextLength) + ")"
		}
		facts = append(facts, context)
	} else if a.EffectiveContextLength > 0 {
		facts = append(facts, "served "+tokens(a.EffectiveContextLength))
	}
	if c != nil && c.Reasoning != nil && len(c.Reasoning.Efforts) > 0 {
		words := make([]string, 0, len(c.Reasoning.Efforts))
		for _, e := range c.Reasoning.Efforts {
			words = append(words, string(e))
		}
		var notes []string
		if c.Reasoning.Budget {
			notes = append(notes, "budget")
		}
		if d := c.Reasoning.DefaultEffort; d != nil {
			notes = append(notes, "default "+string(*d))
		}
		line := "reasoning " + strings.Join(words, ", ")
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, ", ") + ")"
		}
		facts = append(facts, line)
	}
	if c != nil && len(c.Extensions) > 0 {
		facts = append(facts, "ext "+strings.Join(c.Extensions, ", "))
	}
	return host + " " + a.Name + ": " + strings.Join(facts, " · ")
}

// tokens is a token count as a person reads it: binary sizes in binary
// units (262144 is "256k", 1048576 is "1M"), others in thousands.
func tokens(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "M"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + "k"
	case n >= 1_000_000:
		return strconv.FormatInt((n+500_000)/1_000_000, 10) + "M"
	case n >= 1000:
		return strconv.FormatInt((n+500)/1000, 10) + "k"
	}
	return strconv.FormatInt(n, 10)
}
