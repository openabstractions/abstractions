package main

import (
	"fmt"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// providerStartFailure is how the supervisor treats a provider that cannot
// start. Each launch that ends before the child reads ready counts; after
// startFailures of them in a row the supervisor stops relaunching it, reads
// it not_ready with start_failed and the last exit reason, and writes one
// host log line. While stopped, the router lists the provider's models as
// not servable and routes nothing to it, and a request that still reaches
// it is refused at once with start_failed:<name>. The first request after
// startRetry launches the program once more; a new declaration or a runtime
// restart starts afresh. A provider that reads ready clears the count.
// A provider can exit at every launch when Setup has not configured it.
// Repeated launches left a picture request waiting without end (R34 live
// test, D1).
const (
	defaultStartFailures = 3
	defaultStartRetry    = time.Minute
)

// startFailedPrefix begins the reason word of a provider stopped after
// failed starts.
const startFailedPrefix = inference.StartFailedPrefix

func (s *providerSupervisor) startFailureLimit() int {
	if s.p != nil && s.p.timing.startFailures > 0 {
		return s.p.timing.startFailures
	}
	return defaultStartFailures
}

func (s *providerSupervisor) startRetryAfter() time.Duration {
	if s.p != nil && s.p.timing.startRetry > 0 {
		return s.p.timing.startRetry
	}
	return defaultStartRetry
}

// countStart records how a launch ended and reports whether the supervisor
// now stops relaunching the provider.
func (s *providerSupervisor) countStart(ready bool) bool {
	s.mu.Lock()
	if ready {
		s.failedStarts = 0
		s.mu.Unlock()
		return false
	}
	s.failedStarts++
	if s.failedStarts < s.startFailureLimit() {
		s.mu.Unlock()
		return false
	}
	first := s.stopped.IsZero() && s.failedStarts == s.startFailureLimit()
	s.stopped = time.Now()
	last := s.why
	s.mu.Unlock()
	s.set(wire.DeclarationReadinessNotReady, "start_failed:"+last)
	if first && s.p != nil && s.p.report != nil {
		s.p.report(fmt.Errorf("provider %s stopped after %d failed starts; a request after %s starts it once more", s.file.Declaration.Name, s.startFailureLimit(), s.startRetryAfter()))
	}
	return true
}

// startFailure is start_failed:<name> while the supervisor is stopped after
// failed starts and its retry time has not passed, else "".
func (s *providerSupervisor) startFailure() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped.IsZero() || time.Since(s.stopped) >= s.startRetryAfter() {
		return ""
	}
	return startFailedPrefix + s.file.Declaration.Name
}

// retryStopped wakes a supervisor stopped after failed starts for one more
// launch once its retry time has passed. It never blocks.
func (s *providerSupervisor) retryStopped() {
	s.mu.Lock()
	if s.stopped.IsZero() || time.Since(s.stopped) < s.startRetryAfter() {
		s.mu.Unlock()
		return
	}
	s.stopped = time.Time{}
	s.mu.Unlock()
	select {
	case s.retry <- struct{}{}:
	default:
	}
}
