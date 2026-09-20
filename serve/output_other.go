//go:build !windows

package main

// Only the Windows windowless link lacks standard handles.
func speakSomewhere() {}
