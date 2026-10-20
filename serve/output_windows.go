package main

import (
	"log"
	"os"
	"path/filepath"
)

// speakSomewhere gives openabstractionsw.exe, the -H=windowsgui link, somewhere
// to write. That image has no standard output or error: a line written there
// fails, and `openabstractionsw.exe start` run by the installer would report
// "runtime ready" as a failed write. When standard output cannot be inspected
// both streams go to the host log. A console image with working handles is
// unchanged. The file stays open for the life of the process.
func speakSomewhere() {
	if file := outputForState(os.Stdout, hostLogPath); file != nil {
		os.Stdout, os.Stderr = file, file
		log.SetOutput(file)
	}
}

func outputForState(stdout *os.File, path func() (string, error)) *os.File {
	if _, err := stdout.Stat(); err == nil {
		return nil
	}
	name, err := path()
	if err != nil {
		return nil
	}
	return outputWithoutConsole(stdout, name)
}

// A parsed private runtime supplies its own log destination. Resolve it only
// when stdout is unusable; console commands never consult the host log owner.
func speakRuntime(options runtimeFlags) {
	if _, err := os.Stdout.Stat(); err == nil {
		return
	}
	if options.stateDir == "" {
		speakSomewhere()
		return
	}
	if file := outputWithoutConsole(os.Stdout, filepath.Join(options.stateDir, "host.log")); file != nil {
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
