package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/redact"
)

// A launched provider's standard output and error go to one log file per
// declaration under the runtime's state, beside the declarations folder, so
// a provider that exits before it answers leaves its reason where the
// operator can read it. The file is bounded: past providerLogLimit bytes the
// file becomes <name>.log.1, replacing the one before, and a new file starts.
// A line is cut at providerLineLimit bytes.
const (
	providerLogsDir   = "provider-logs"
	providerLogLimit  = 1 << 20
	providerLineLimit = 4 << 10
	// providerTailLimit bounds the last line an exit reason carries.
	providerTailLimit = 240
	// providerTailWait bounds how long an exit waits for the reader.
	providerTailWait = 500 * time.Millisecond
)

// providerLogPath is the log file of the declaration name in the runtime
// state whose declarations live in providersDir, or "" with no state.
func providerLogPath(providersDir, name string) string {
	if providersDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(providersDir), providerLogsDir, name+".log")
}

// providerOutput reads one launched child's output into its log file and
// keeps the last non-empty line.
type providerOutput struct {
	path string
	// write is the end the child writes; the runtime closes its own copy
	// once the child has started.
	write *os.File

	mu   sync.Mutex
	last string
	done chan struct{}
}

// openProviderOutput makes the pipe a child's output goes through and starts
// reading it. A log file that cannot be opened loses the lines but never the
// launch; the last line is still kept.
func openProviderOutput(path string, report func(error)) (*providerOutput, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	o := &providerOutput{path: path, write: write, done: make(chan struct{})}
	go o.drain(read, report)
	return o, nil
}

// started closes the runtime's copy of the write end, so the reader ends
// when the child and every process it handed the pipe to have exited.
func (o *providerOutput) started() {
	//unchecked: the child holds its own copy of the pipe; closing ours only lets the reader see the end
	o.write.Close()
}

// safeTail is a provider's output line as an exit reason may carry it: one
// line made safe by redact.Line, the owner of the rules for showing provider
// and engine text, cut to providerTailLimit bytes on a character boundary.
// The whole line stays only in the provider's log file.
func safeTail(line string) string {
	return redact.Line(line, providerTailLimit)
}

// tail is the last non-empty line the child wrote, made safe, once the reader
// has finished or after a short wait for it: a process the child started can
// hold the pipe open after the child itself exited.
func (o *providerOutput) tail() string {
	timer := time.NewTimer(providerTailWait)
	select {
	case <-o.done:
	case <-timer.C:
	}
	timer.Stop()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last
}

func (o *providerOutput) drain(read *os.File, report func(error)) {
	defer close(o.done)
	defer read.Close()
	log := &boundedLog{path: o.path}
	defer log.close()
	scanner := bufio.NewScanner(read)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	reported := false
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if len(line) > providerLineLimit {
			line = line[:providerLineLimit]
		}
		if err := log.writeLine(line); err != nil && !reported && report != nil {
			reported = true
			report(fmt.Errorf("provider log %s: %w", o.path, err))
		}
		if safe := safeTail(line); safe != "" {
			o.mu.Lock()
			o.last = safe
			o.mu.Unlock()
		}
	}
}

// boundedLog appends lines to one file and rotates it once at
// providerLogLimit.
type boundedLog struct {
	path string
	file *os.File
	size int64
}

func (l *boundedLog) writeLine(line string) error {
	if l.path == "" {
		return nil
	}
	if l.file == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			//unchecked: the Stat error is the one reported
			f.Close()
			return err
		}
		l.file, l.size = f, info.Size()
	}
	if l.size > 0 && l.size+int64(len(line))+1 > providerLogLimit {
		//unchecked: the file is rotated next; a failed close loses at most its last lines
		l.file.Close()
		l.file = nil
		older := l.path + ".1"
		//unchecked: a missing earlier rotation is the usual case; Rename below reports what matters
		os.Remove(older)
		if err := os.Rename(l.path, older); err != nil {
			return err
		}
		return l.writeLine(line)
	}
	n, err := l.file.WriteString(line + "\n")
	l.size += int64(n)
	return err
}

func (l *boundedLog) close() {
	if l.file != nil {
		//unchecked: the log is append-only text; a failed close loses at most its last lines
		l.file.Close()
	}
}
