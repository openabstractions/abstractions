package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func archiveRequest(t *testing.T, p *servicePanel, method, body string) (int, settingsArchiveReply) {
	t.Helper()
	target := "/settings/archive"
	if method == "PROBE" {
		method, target = http.MethodGet, "/settings/archive?probe=1"
	}
	recorder := httptest.NewRecorder()
	p.settingsArchive(recorder, httptest.NewRequest(method, target, strings.NewReader(body)))
	var reply settingsArchiveReply
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
	}
	return recorder.Code, reply
}

// The Download archive row reads the stored setting on arrival without
// touching the folder; a probe adds whether it answers. A relative folder is
// refused.
func TestSettingsArchiveStateOnArrivalAndSave(t *testing.T) {
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	p := &servicePanel{}
	if code, reply := archiveRequest(t, p, http.MethodGet, ""); code != http.StatusOK || reply != (settingsArchiveReply{Keep: true}) {
		t.Fatalf("arrival %d %+v, want Not set", code, reply)
	}
	folder := t.TempDir()
	body, _ := json.Marshal(map[string]string{"folder": folder})
	if code, reply := archiveRequest(t, p, http.MethodPost, string(body)); code != http.StatusOK || reply.Folder != folder || reply.Network || reply.Reachable != nil || !reply.Keep {
		t.Fatalf("save %d %+v", code, reply)
	}
	listed := archiveListed(t, p)
	if listed.WalkedAt == "" || listed.Found != 0 || listed.Other != 0 || listed.Incomplete || listed.Unanswered {
		t.Fatalf("a chosen empty folder lists %+v", listed)
	}
	if code, reply := archiveRequest(t, p, "PROBE", ""); code != http.StatusOK || reply.Reachable == nil || !*reply.Reachable {
		t.Fatalf("probe %d %+v", code, reply)
	}
	missing := filepath.Join(folder, "asleep")
	body, _ = json.Marshal(map[string]string{"folder": missing})
	archiveRequest(t, p, http.MethodPost, string(body))
	if code, reply := archiveRequest(t, p, http.MethodGet, ""); code != http.StatusOK || reply.Folder != missing || reply.Reachable != nil {
		t.Fatalf("arrival probed the folder: %d %+v", code, reply)
	}
	if code, reply := archiveRequest(t, p, "PROBE", ""); code != http.StatusOK || reply.Reachable == nil || *reply.Reachable {
		t.Fatalf("an unreachable folder probes %d %+v", code, reply)
	}
	if runtime.GOOS == "windows" {
		body, _ = json.Marshal(map[string]string{"folder": `\\nas.test\archive`})
		if code, reply := archiveRequest(t, p, http.MethodPost, string(body)); code != http.StatusOK || !reply.Network {
			t.Fatalf("a share is not a network folder: %d %+v", code, reply)
		}
	}
	if code, _ := archiveRequest(t, p, http.MethodPost, `{"folder":"relative"}`); code != http.StatusBadRequest {
		t.Fatalf("a relative folder was accepted: %d", code)
	}
	if code, _ := archiveRequest(t, p, http.MethodPost, `{}`); code != http.StatusBadRequest {
		t.Fatalf("an empty change was accepted: %d", code)
	}
	if code, reply := archiveRequest(t, p, http.MethodPost, `{"folder":""}`); code != http.StatusOK || reply.Folder != "" || reply.WalkedAt != "" {
		t.Fatalf("stop %d %+v", code, reply)
	}
}

// archiveListed polls the row until the listing a change started ends.
func archiveListed(t *testing.T, p *servicePanel) settingsArchiveReply {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, reply := archiveRequest(t, p, http.MethodGet, "")
		if code != http.StatusOK {
			t.Fatalf("read %d", code)
		}
		if !reply.Looking {
			return reply
		}
		if time.Now().After(deadline) {
			t.Fatal("the listing did not end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Look again lists the folder: a model file that matches no reviewed pin
// counts as other. Keep a copy of new downloads here saves either way.
func TestSettingsArchiveLookAgainAndKeep(t *testing.T) {
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	p := &servicePanel{}
	folder := t.TempDir()
	body, _ := json.Marshal(map[string]string{"folder": folder})
	archiveRequest(t, p, http.MethodPost, string(body))
	archiveListed(t, p)
	if err := os.MkdirAll(filepath.Join(folder, "models", "qwen"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "models", "qwen", "x.gguf"), make([]byte, 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, reply := archiveRequest(t, p, http.MethodPost, `{"look":true}`); code != http.StatusOK || reply.Folder != folder {
		t.Fatalf("look %d %+v", code, reply)
	}
	if listed := archiveListed(t, p); listed.Other != 1 || listed.Found != 0 || listed.WalkedAt == "" {
		t.Fatalf("Look again listed %+v", listed)
	}
	if code, reply := archiveRequest(t, p, http.MethodPost, `{"keep":false}`); code != http.StatusOK || reply.Keep {
		t.Fatalf("keep off %d %+v", code, reply)
	}
	if code, reply := archiveRequest(t, p, http.MethodGet, ""); code != http.StatusOK || reply.Keep || reply.Folder != folder {
		t.Fatalf("keep off did not hold %d %+v", code, reply)
	}
	if code, reply := archiveRequest(t, p, http.MethodPost, `{"keep":true}`); code != http.StatusOK || !reply.Keep {
		t.Fatalf("keep on %d %+v", code, reply)
	}
	missing := filepath.Join(folder, "asleep")
	body, _ = json.Marshal(map[string]string{"folder": missing})
	archiveRequest(t, p, http.MethodPost, string(body))
	if listed := archiveListed(t, p); !listed.Unanswered || listed.WalkedAt != "" {
		t.Fatalf("a silent folder lists %+v", listed)
	}
	archiveRequest(t, p, http.MethodPost, `{"folder":""}`)
}
