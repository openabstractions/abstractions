package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestRuntimeAsksScopeUsesObservedExecutable(t *testing.T) {
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	packaged := identity.PackagedProgramPrefix + "OpenAbstractions_123456789abcd"
	if !identity.ValidSubjectProgram(packaged) {
		t.Fatal("invalid package-family fixture")
	}
	for _, program := range []string{packaged, filepath.Dir(executable) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(executable)} {
		t.Run(program, func(t *testing.T) {
			r := &runtimeRights{kind: "windows", owner: owner.Uid, operators: []string{program}}
			a, err := composeAsks(runtimeFlags{stateDir: t.TempDir(), endpoint: fullEndpoint(t, "asks-scope")}, r, func(err error) { t.Error(err) })
			if err != nil {
				t.Fatal(err)
			}
			// The receiving application profile derives this value from the
			// platform's executable proof, independently of subject Program.
			data, _ := json.Marshal([]string{"asks-owner-program@1", r.kind, r.owner, filepath.Clean(executable)})
			sum := sha256.Sum256(data)
			want := "asks-owner-program@1:" + hex.EncodeToString(sum[:])
			if a.scope != want {
				t.Fatalf("runtime scope %q; receiving executable scope %q", a.scope, want)
			}
			subject := rwire.Subject{Account: r.owner, Program: filepath.Clean(executable)}
			id := a.admitFirstUse(subject, nil, host.ModelLookupAction, "hf")
			if id == "" {
				t.Fatal("runtime question was not admitted")
			}
			receivingScope := asks.ApplicationOwnerProgramScope(r.kind, r.owner, executable)
			record, outcome, err := a.book.ObserveApplication(receivingScope, firstUseKey(expectedSubjectProgram(subject.Program), host.ModelLookupAction, "hf"))
			if err != nil || outcome != "pending" || record.ID != id {
				t.Fatalf("same caller lookup: %+v %q %v", record, outcome, err)
			}
		})
	}
}
