package main

import inference "github.com/openabstractions/abstraction-inference/go"

// Budgets count usage per credential. Every active declaration's nonzero
// limits must hold, so shared credentials take the strictest limit per unit.
func declarationBudgets(files []providerFile) map[string]inference.Ceiling {
	limits := map[string]inference.Ceiling{}
	for _, file := range files {
		host := file.Declaration.Host
		if file.Disabled || host == nil || host.Credential == "" || host.Ceiling == nil {
			continue
		}
		limit := limits[host.Credential]
		incoming := *host.Ceiling
		for _, field := range []struct {
			current  *int64
			incoming int64
		}{
			{&limit.TokensPerDay, incoming.TokensPerDay},
			{&limit.MicrosPerDay, incoming.MicrosPerDay},
			{&limit.RequestsPerDay, incoming.RequestsPerDay},
			{&limit.ImagesPerDay, incoming.ImagesPerDay},
			{&limit.AudioSecondsPerDay, incoming.AudioSecondsPerDay},
			{&limit.CharactersPerDay, incoming.CharactersPerDay},
		} {
			if field.incoming > 0 && (*field.current == 0 || field.incoming < *field.current) {
				*field.current = field.incoming
			}
		}
		limits[host.Credential] = limit
	}
	return limits
}
