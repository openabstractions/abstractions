package main

import "testing"

// nasShareSpelling reaches the local folder dir through the administrative
// share \\localhost\X$, the spelling a store on a network share has.
func nasShareSpelling(t *testing.T, dir string) string {
	t.Helper()
	return `\\localhost\` + dir[:1] + `$` + dir[2:]
}
