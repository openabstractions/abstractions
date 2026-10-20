package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// AgentUpdateRetry is the background update retry's last stop for approved
// updates: the native intents it tried and a short reason. The retry runs in
// the account's background process and the Panel reads it from another one.
// It grants nothing; the Applications service still owns every intent.
type AgentUpdateRetry struct {
	Intents []string `json:"intents"`
	Reason  string   `json:"reason"`
	At      int64    `json:"at"`
}

const agentUpdateRetryName = ".oa-agent-update-retry.json"

func agentUpdateRetryPath() (string, error) {
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	if home.Isolated {
		return "", errors.New("agent update retry record requires the installed runtime")
	}
	return filepath.Join(home.Config, "agent-integrations", agentUpdateRetryName), nil
}

// RecordAgentUpdateRetry replaces the record. An empty reason or intent list
// removes it: the retry finished or only waits for running apps.
func RecordAgentUpdateRetry(intents []string, reason string) error {
	path, err := agentUpdateRetryPath()
	if err != nil {
		return err
	}
	if err := privatedir.CheckDir(filepath.Dir(path)); err != nil {
		return err
	}
	return recordAgentUpdateRetryAt(path, intents, reason, time.Now())
}

func recordAgentUpdateRetryAt(path string, intents []string, reason string, at time.Time) error {
	if reason == "" || len(intents) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(AgentUpdateRetry{Intents: intents, Reason: reason, At: at.UnixMilli()})
	if err != nil {
		return err
	}
	return cas.Change(path, func([]byte) ([]byte, error) { return raw, nil })
}

// ReadAgentUpdateRetry returns the stop recorded for intent, if any.
func ReadAgentUpdateRetry(intent string) (string, bool) {
	path, err := agentUpdateRetryPath()
	if err != nil {
		return "", false
	}
	return readAgentUpdateRetryAt(path, intent)
}

func readAgentUpdateRetryAt(path, intent string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 4096 || intent == "" {
		return "", false
	}
	var record AgentUpdateRetry
	if json.Unmarshal(raw, &record) != nil || record.Reason == "" {
		return "", false
	}
	for _, recorded := range record.Intents {
		if recorded == intent {
			return record.Reason, true
		}
	}
	return "", false
}
