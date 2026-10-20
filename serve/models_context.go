package main

import (
	"context"
	"strconv"
	"time"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// modelContextKey is the configuration key holding the person's
// per-conversation context for each local model family, as decimal tokens;
// an absent family is Automatic (decision D, 2026-11-08). modelhostd reads
// the same key at each load.
const modelContextKey = "model_context"

// modelContextReadBudget bounds the inventory's read of the person's choice.
const modelContextReadBudget = 2 * time.Second

// conversationContext answers the context one conversation gets on this
// machine for a curated family, from the model owner's plan; zero when the
// family has no plan.
type conversationContext func(ctx context.Context, family string) int64

// planConversationContext composes the model owner's PlanContext with the
// machine's pool (the resource table's own rule, as Fit reads it) and the
// person's choice.
func planConversationContext(pool func() int64, choice func(context.Context, string) int64) conversationContext {
	return func(ctx context.Context, family string) int64 {
		var chosen int64
		if choice != nil {
			chosen = choice(ctx, family)
		}
		plan, ok := modelservice.PlanContext(family, pool(), chosen)
		if !ok {
			return 0
		}
		return plan.Context
	}
}

// tablePool is card:0's pool by the resource table's rule, the pool the model
// host's loads are sized against; zero when the machine reports none.
func tablePool(table *resourceservice.Table) func() int64 {
	return func() int64 {
		if table == nil {
			return 0
		}
		return max(table.Pool(instrument.Card0), 0)
	}
}

// typedContextReader is the runtime's own read of the typed configuration.
type typedContextReader interface {
	ReadTypedContext(context.Context, []string) (configwire.ReadResult, error)
}

// modelContextChoice reads the person's per-conversation context for a family
// from model_context; zero for Automatic or a value it cannot read.
func modelContextChoice(reader func() (typedContextReader, error)) func(context.Context, string) int64 {
	return func(ctx context.Context, family string) int64 {
		config, err := reader()
		if err != nil {
			return 0
		}
		ctx, cancel := context.WithTimeout(ctx, modelContextReadBudget)
		defer cancel()
		result, err := config.ReadTypedContext(ctx, []string{modelContextKey})
		if err != nil || result.Outcome != configwire.ReadOutcomeRead {
			return 0
		}
		for _, entry := range result.Entries {
			if entry.Key != modelContextKey || entry.Value == nil || entry.Value.Kind != configwire.ValueKindTextMap {
				continue
			}
			if tokens, err := strconv.ParseInt(entry.Value.TextMap[family], 10, 64); err == nil && tokens > 0 {
				return tokens
			}
		}
		return 0
	}
}

// hostedContexts is the router's context read for the model host's names: a
// name whose content pin is a curated catalogue entry reports the context one
// conversation gets, which the model host's engine adapter applies at its
// next load. Other names report none.
func hostedContexts(models []hostedModel, entries []modelwire.FitEntry, contexts conversationContext) func(context.Context, []string) map[string]int64 {
	families := map[string]string{}
	for _, m := range models {
		for _, entry := range entries {
			if m.Content != "" && m.Content == entry.Digest+":"+strconv.FormatInt(entry.DownloadBytes, 10) {
				families[m.Name] = entry.Family
			}
		}
	}
	return func(ctx context.Context, names []string) map[string]int64 {
		out := map[string]int64{}
		for _, name := range names {
			if family := families[name]; family != "" {
				if tokens := contexts(ctx, family); tokens > 0 {
					out[name] = tokens
				}
			}
		}
		return out
	}
}
