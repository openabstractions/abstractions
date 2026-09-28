//go:build !windows

package main

import "testing"

func shortSubjectProgramAlias(t *testing.T, program string) (string, bool) {
	t.Helper()
	return program, false
}
