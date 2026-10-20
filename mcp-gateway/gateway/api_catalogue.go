package gateway

import (
	"encoding/json"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	inference "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	job "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	logging "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

// Each migrated definition supplies its complete generated catalogue and codec
// join. Slice 1 admits readonly Rights/Logging and operator profiles.
var apiCatalogue = buildAPIMethods()

func apiMethods() []apiMethod { return apiCatalogue }

func buildAPIMethods() []apiMethod {
	var out []apiMethod
	add := func(contract, method, class, trait, key, doc, args, result string, invoke func(apiTransport, string, string, []byte) ([]byte, error), serviceError func(error) ([]byte, bool)) {
		operator := strings.Contains(contract, "/operator@") || strings.Contains(contract, "-operator@")
		routerGuide := contract == "abstraction.router/router@1" && (method == "Models" || method == "Hosts")
		if class != "read" || !(routerGuide || operator || strings.HasPrefix(contract, "abstraction.rights/") || strings.HasPrefix(contract, "abstraction.logging/")) {
			return
		}
		out = append(out, apiMethod{Contract: contract, Method: method, Class: class, Trait: trait, IdempotencyKey: key, Documentation: doc, ArgumentsSchema: json.RawMessage(args), ResultSchema: json.RawMessage(result), invoke: invoke, serviceError: serviceError})
	}
	for _, m := range rights.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return rights.MCPInvoke(t, c, m, a) }, rights.MCPServiceError)
	}
	for _, m := range logging.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return logging.MCPInvoke(t, c, m, a) }, logging.MCPServiceError)
	}
	for _, m := range asks.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return asks.MCPInvoke(t, c, m, a) }, asks.MCPServiceError)
	}
	for _, m := range inference.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return inference.MCPInvoke(t, c, m, a) }, inference.MCPServiceError)
	}
	for _, m := range job.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return job.MCPInvoke(t, c, m, a) }, job.MCPServiceError)
	}
	for _, m := range router.MCPMethods() {
		add(m.Contract, m.Method, m.Class, m.Trait, m.IdempotencyKey, m.Documentation, m.ArgumentsSchema, m.ResultSchema, func(t apiTransport, c, m string, a []byte) ([]byte, error) { return router.MCPInvoke(t, c, m, a) }, router.MCPServiceError)
	}
	return out
}
