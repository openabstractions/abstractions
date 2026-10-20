package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
)

type readerReply struct {
	result wire.SecretReadResult
	err    error
	names  []string
}

func (r *readerReply) Read(_ context.Context, name string) (wire.SecretReadResult, error) {
	r.names = append(r.names, name)
	return r.result, r.err
}

func TestRawReadComparesPrivatelyAndWipesEveryReturnedCopy(t *testing.T) {
	f, _ := fixtureFor("alpha")
	for _, test := range []struct {
		name    string
		outcome wire.SecretReadOutcome
		value   []byte
		err     error
		success bool
	}{
		{"read", wire.SecretReadOutcomeRead, append([]byte(nil), f.secret...), nil, true},
		{"wrong-value", wire.SecretReadOutcomeRead, []byte("different dummy"), nil, false},
		{"refusal-carries-value", wire.SecretReadOutcomeForbidden, append([]byte(nil), f.secret...), nil, false},
		{"transport-error", wire.SecretReadOutcomeRead, append([]byte(nil), f.secret...), errors.New("private diagnostic"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &readerReply{result: wire.SecretReadResult{Outcome: test.outcome, Value: &wire.SecretValue{Name: f.name, Revision: "current", Secret: test.value}}, err: test.err}
			var out bytes.Buffer
			err := readWithClient(context.Background(), r, f, &out)
			if (err == nil) != test.success || len(r.names) != 1 || r.names[0] != f.name {
				t.Fatal("raw Read must use only the exact requested name")
			}
			if strings.Contains(out.String(), string(f.secret)) || strings.Contains(out.String(), "private diagnostic") {
				t.Fatal("raw bytes/diagnostics entered output")
			}
			for _, b := range test.value {
				if b != 0 {
					t.Fatal("returned raw copy survived app processing")
				}
			}
		})
	}
}
