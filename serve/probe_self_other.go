//go:build !windows

package main

func selfProgramPath(observed string) string { return observed }
