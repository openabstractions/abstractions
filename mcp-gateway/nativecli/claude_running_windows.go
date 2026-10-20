package nativecli

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const claudeCodePackage = `@anthropic-ai[/\\]claude-code`

var (
	claudeNodeProgram = regexp.MustCompile(`(?i)^node\.exe$`)
	claudeCodeOperand = regexp.MustCompile(`(?i)(?:^|[/\\])(?:` + claudeCodePackage + `(?:[/\\]|$)|claude-code[/\\]cli)`)
	claudeEvalPackage = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_@.-])` + claudeCodePackage + `(?:$|[^a-z0-9_.-])`)
)

// ClaudeRunning reports whether a Claude desktop app or a Claude Code CLI of
// this account runs in this logon session: a claude.exe, or a node.exe
// whose command line runs the Claude Code package. A missing OA inbox proves
// no host absence, so while one runs a conversation may be open and OA never
// resumes the original in place. It reads the process inventory, owners and
// node command lines only. A candidate whose session, owner or command line
// cannot be read counts as running.
func ClaudeRunning(ctx context.Context) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snapshot)
	var own uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &own); err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for count := 0; err == nil; count++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if count >= 8192 {
			return false, errors.New("process inventory exceeded its bound")
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		claude, node := claudeCLIProgram.MatchString(name), claudeNodeProgram.MatchString(name)
		if (claude || node) && claudeCandidateRunning(entry.ProcessID, own, user.User.Sid, node) {
			return true, nil
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, err
	}
	return false, nil
}

// claudeCandidateRunning decides one candidate: another session or another
// account is not this person's Claude; a node.exe counts only when its
// command line names the Claude Code package. Unreadable facts count.
func claudeCandidateRunning(pid, session uint32, user *windows.SID, node bool) bool {
	var actual uint32
	if windows.ProcessIdToSessionId(pid, &actual) != nil {
		return true
	}
	if actual != session {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return true
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token) != nil {
		return true
	}
	owner, err := token.GetTokenUser()
	token.Close()
	if err != nil {
		return true
	}
	if !owner.User.Sid.Equals(user) {
		return false
	}
	if !node {
		return true
	}
	line, err := processCommandLine(process)
	if err != nil {
		return true
	}
	matches, err := claudeCodeCommandLine(line)
	return err != nil || matches
}

// claudeCodeCommandLine checks the Node launcher arguments for Claude Code.
func claudeCodeCommandLine(line string) (bool, error) {
	argv, err := windows.DecomposeCommandLine(line)
	if err != nil || len(argv) == 0 {
		return false, err
	}
	return claudeCodeArguments(argv[1:]), nil
}

func claudeCodeArguments(args []string) bool {
	var possibleOptionValue, evaluating bool
	for len(args) != 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			return !evaluating && len(args) != 0 && claudeCodeOperand.MatchString(args[0])
		}
		if arg == "-" {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			if !evaluating && claudeCodeOperand.MatchString(arg) {
				return true
			}
			if !possibleOptionValue {
				return false
			}
			possibleOptionValue = false
			continue
		}
		option, value, inline := strings.Cut(arg, "=")
		afterValue, hasValue := args, inline
		if !inline && len(args) != 0 && !strings.HasPrefix(args[0], "-") {
			value, afterValue, hasValue = args[0], args[1:], true
		}
		possibleOptionValue = false
		switch option {
		case "-e", "--eval", "-p", "--print", "-pe":
			args, evaluating = afterValue, evaluating || hasValue
			if claudeEvalPackage.MatchString(value) {
				return true
			}
		case "-r", "--require", "--import", "--loader", "--experimental-loader":
			args = afterValue
			if claudeCodeOperand.MatchString(value) {
				return true
			}
		case "-C", "--conditions", "--input-type", "--env-file", "--env-file-if-exists":
			args = afterValue
		case "--inspect", "--inspect-brk", "--no-warnings":
			// These switches take no separate argument.
		default:
			if inline && claudeCodeOperand.MatchString(value) {
				return true
			}
			// An option outside these startup forms can take one separate
			// value. A later explicit Claude hint still counts as running.
			possibleOptionValue = !inline
		}
	}
	return false
}

// processCommandLine reads a process's command line through
// ProcessCommandLineInformation (60), which PROCESS_QUERY_LIMITED_INFORMATION
// admits for a process of the same account.
func processCommandLine(process windows.Handle) (string, error) {
	size := uint32(4096)
	for range 4 {
		buffer := make([]byte, size)
		var needed uint32
		err := windows.NtQueryInformationProcess(process, 60, unsafe.Pointer(&buffer[0]), size, &needed)
		if err == nil {
			text := (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0]))
			if text.Buffer == nil || text.Length == 0 {
				return "", nil
			}
			return windows.UTF16ToString(unsafe.Slice(text.Buffer, text.Length/2)), nil
		}
		if needed <= size || needed > 1<<20 {
			return "", err
		}
		size = needed
	}
	return "", errors.New("process command line unavailable")
}
