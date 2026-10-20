package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/modelobserver"
)

// Only the existing native observer supplies program evidence here. Explicit
// endpoint declarations without that evidence remain registered but cannot use
// this installed HTTP path. No product name becomes an executable identity.
func (p *runtimeProviders) expectedEngineProgram(ctx context.Context, target string) (string, error) {
	if p == nil || ctx.Err() != nil {
		return "", errors.New("engine program unavailable")
	}
	request, err := url.Parse(target)
	if err != nil || request.User != nil {
		return "", errors.New("invalid engine endpoint")
	}
	program := ""
	for _, file := range p.hostFiles() {
		host := file.Declaration.Host
		if file.Disabled || host == nil || host.Hosted {
			continue
		}
		base, err := url.Parse(host.Base)
		if err != nil || base.Scheme != request.Scheme || base.Host != request.Host || base.User != nil {
			continue
		}
		path := strings.TrimRight(base.Path, "/")
		if path != "" && request.Path != path && !strings.HasPrefix(request.Path, path+"/") {
			continue
		}
		if file.Source != declarationSourceProduct || file.ProductPresence != "verified" || file.ProductProgram == "" {
			return "", errors.New("engine program unverified")
		}
		if program != "" && !identity.SameSubjectProgram(program, file.ProductProgram) {
			return "", errors.New("ambiguous engine program")
		}
		program = file.ProductProgram
	}
	if program == "" {
		return "", errors.New("engine program unavailable")
	}
	return program, nil
}

func runtimeEngineHTTPClient(providers *runtimeProviders, timeout time.Duration) *http.Client {
	client := modelobserver.NewClient(providers.expectedEngineProgram)
	client.Timeout = timeout
	return client
}
