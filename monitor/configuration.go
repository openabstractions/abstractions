package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"reflect"
	"sync"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// editConfiguration translates the retained panel controls into a conditional
// service update. It reads only the editable user rung; effective machine and
// environment values never get copied into the user's stored settings.
func (w *window) editConfiguration(change func(*config.Config) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	editor, err := panelMachine().ResolveConfigEditor(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		return fmt.Errorf("configuration service: %w", err)
	}
	current, err := editor.ReadUserContext(ctx)
	if err != nil {
		return err
	}
	values := current.Values
	settings := config.Config{NASStore: values.NasStore, Store: values.Store,
		LogSink: values.LogSink, LogService: values.LogService, Off: maps.Clone(values.Off)}
	if err := change(&settings); err != nil {
		return err
	}
	if settings.Off == nil {
		settings.Off = map[string]string{}
	}
	result, err := editor.ReplaceUserContext(ctx, current.Revision, wire.UserSettings{
		NasStore: settings.NASStore, Store: settings.Store, LogSink: settings.LogSink,
		LogService: settings.LogService, Off: settings.Off,
	})
	if err != nil {
		return err
	}
	if result.Outcome != wire.UserReplaceOutcomeApplied {
		return fmt.Errorf("configuration changed while editing (%s); refresh and retry", result.Outcome)
	}
	return nil
}

// configurationView preserves the retained display adapter without file access.
type configurationView interface {
	Current() config.Config
	Changes() <-chan config.Config
	How() string
	Close() error
}
type configurationObservation struct {
	mu      sync.Mutex
	value   config.Config
	err     error
	changes chan config.Config
	cancel  context.CancelFunc
	done    chan struct{}
}

// watchConfiguration receives latest snapshots through bounded long polls.
// Intermediate revisions may coalesce; a failed wait retains the last good view.
func watchConfiguration(parent context.Context) *configurationObservation {
	return observeConfiguration(parent, 2*time.Second)
}

type configurationObserver interface {
	ObserveContext(context.Context, wire.RunOverrides, string, int64) (wire.ConfigObservation, error)
}
type configurationResolver func(context.Context) (configurationObserver, error)

func observeConfiguration(parent context.Context, retryDelay time.Duration) *configurationObservation {
	return observeConfigurationWith(parent, retryDelay, func(ctx context.Context) (configurationObserver, error) {
		observer, err := panelMachine().ResolveConfigObserver(ctx, facade.Requirements{Scope: "local"})
		if err != nil {
			return nil, err
		}
		return observer.WithTimeout(35 * time.Second)
	}, wire.RunOverrides{NasStore: os.Getenv("ABSTRACTION_NAS_STORE"), Store: os.Getenv("ABSTRACTION_STORE"), LogSink: os.Getenv("ABSTRACTION_LOG"), LogService: os.Getenv("ABSTRACTION_LOG_SERVICE")})
}
func observeConfigurationWith(parent context.Context, retryDelay time.Duration, resolve configurationResolver, overrides wire.RunOverrides) *configurationObservation {
	ctx, cancel := context.WithCancel(parent)
	s := &configurationObservation{err: fmt.Errorf("configuration service has not answered"), changes: make(chan config.Config, 1), cancel: cancel, done: make(chan struct{})}
	publish := func(snapshot *wire.Snapshot, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		changed := fmt.Sprint(s.err) != fmt.Sprint(err)
		if snapshot != nil {
			value := configurationDisplay(*snapshot)
			changed = changed || !reflect.DeepEqual(s.value, value)
			s.value = value
		}
		s.err = err
		if changed {
			select {
			case <-s.changes:
			default:
			}
			s.changes <- cloneConfiguration(s.value)
		}
	}
	go func() {
		defer close(s.done)
		defer close(s.changes)
		var observer configurationObserver
		cursor := ""
		for {
			if ctx.Err() != nil {
				return
			}
			call, stop := context.WithTimeout(ctx, 35*time.Second)
			var err error
			if observer == nil {
				observer, err = resolve(call)
			}
			var result wire.ConfigObservation
			if err == nil {
				result, err = observer.ObserveContext(call, overrides, cursor, 30000)
			}
			stop()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				switch result.Outcome {
				case wire.ConfigObservationOutcomeSnapshot:
					if result.Snapshot == nil || result.Cursor == "" || result.Cursor == cursor {
						err = fmt.Errorf("configuration service returned an invalid update")
					} else {
						cursor = result.Cursor
						publish(result.Snapshot, nil)
					}
				case wire.ConfigObservationOutcomeUnchanged:
					if cursor == "" || result.Cursor != cursor || result.Snapshot != nil {
						err = fmt.Errorf("configuration service returned an invalid continuation")
					} else {
						publish(nil, nil)
					}
				case wire.ConfigObservationOutcomeGap:
					if cursor == "" {
						err = fmt.Errorf("configuration service could not restart updates")
					} else {
						cursor = ""
						continue
					}
				default:
					err = fmt.Errorf("configuration updates unavailable (%s)", result.Outcome)
				}
			}
			if err != nil {
				publish(nil, err)
				observer = nil
				timer := time.NewTimer(retryDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}()
	return s
}
func cloneConfiguration(c config.Config) config.Config {
	c.Off = maps.Clone(c.Off)
	c.Origins = maps.Clone(c.Origins)
	return c
}
func configurationDisplay(s wire.Snapshot) config.Config {
	c := config.Config{NASStore: s.NasStore, Store: s.Store, LogSink: s.LogSink, LogService: s.LogService, Off: maps.Clone(s.Off), Origins: map[string]config.Origin{}}
	for key, o := range map[string]wire.Origin{"nas_store": s.Origins.NasStore, "store": s.Origins.Store, "log_sink": s.Origins.LogSink, "log_service": s.Origins.LogService, "off": s.Origins.Off} {
		c.Origins[key] = config.Origin{Rung: o.Rung, Path: o.Path}
	}
	return c
}
func (s *configurationObservation) Current() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConfiguration(s.value)
}
func (s *configurationObservation) Error() error                  { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *configurationObservation) Changes() <-chan config.Config { return s.changes }
func (s *configurationObservation) How() string {
	if err := s.Error(); err != nil {
		if err.Error() == "configuration service has not answered" {
			return "waiting for configuration service"
		}
		return "configuration unavailable; updates will retry"
	}
	return "configuration updates (latest settings; changes may coalesce)"
}
func (s *configurationObservation) Close() error { s.cancel(); <-s.done; return nil }
