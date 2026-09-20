package main

import (
	"log"
	"os"
)

// speakSomewhere gives openabstractionsw.exe, the -H=windowsgui link, somewhere
// to write. That image has no standard output or error: a line written there
// fails, and `openabstractionsw.exe start` run by the installer would report
// "runtime ready" as a failed write. When standard output cannot be inspected
// both streams go to the host log. A console image with working handles is
// unchanged. The file stays open for the life of the process.
func speakSomewhere() {
	path, err := hostLogPath()
	if err != nil {
		return
	}
	if file := outputWithoutConsole(os.Stdout, path); file != nil {
		os.Stdout, os.Stderr = file, file
		log.SetOutput(file)
	}
}

// outputWithoutConsole returns the log file to write to when stdout is not a
// usable handle, and nil when stdout works or the log cannot be opened.
func outputWithoutConsole(stdout *os.File, path string) *os.File {
	if _, err := stdout.Stat(); err == nil {
		return nil
	}
	file, _ := openHostLogAt(path).file.(*os.File)
	return file
}
