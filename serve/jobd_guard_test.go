package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestServeJobdChecksTheUpgradeExclusionBeforeSupervising(t *testing.T) {
	refused := &exitError{code: exitUpgradeInProgress, err: errors.New("serve jobd refused: an upgrade of this installation is in progress")}
	for _, c := range []struct {
		name    string
		args    []string
		guard   error
		checked bool
		ran     bool
	}{
		{name: "no upgrade", args: []string{"--interval", "5s"}, checked: true, ran: true},
		{name: "upgrade in progress", args: []string{"--interval", "5s"}, guard: refused, checked: true},
		{name: "check failed", guard: errors.New("upgrade-check could not run"), checked: true},
		{name: "help during upgrade", args: []string{"--help"}, guard: refused, ran: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var order []string
			var got []string
			h := jobdHost{
				guard: func(context.Context) error { order = append(order, "guard"); return c.guard },
				jobs:  func(args []string) error { order = append(order, "jobs"); got = args; return nil },
			}
			err := h.run(c.args)
			want := []string{}
			if c.checked {
				want = append(want, "guard")
			}
			if c.ran {
				want = append(want, "jobs")
			}
			if !reflect.DeepEqual(append([]string{}, order...), want) {
				t.Fatalf("order %v, want %v", order, want)
			}
			if c.ran && (err != nil || !reflect.DeepEqual(got, c.args)) {
				t.Fatalf("supervisor did not run with its arguments: %v %v", err, got)
			}
			if !c.ran && !errors.Is(err, c.guard) {
				t.Fatalf("refusal lost: %v", err)
			}
		})
	}
	var exit *exitError
	if err := (jobdHost{guard: func(context.Context) error { return refused }, jobs: nil}).run(nil); !errors.As(err, &exit) || exit.code != 3 {
		t.Fatalf("an upgrade refusal must carry exit status 3: %v", err)
	}
}
