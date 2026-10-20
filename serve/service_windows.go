package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"golang.org/x/sys/windows"
)

// `openabstractions service …` are the commands Windows Installer runs around
// an upgrade. The MSI runs them from its Binary table copy of the incoming
// openabstractions.exe.

const serviceUsage = `Usage: openabstractions service <command>

  begin-upgrade --user|--machine <install folder> --related <product codes>
        hold the upgrade exclusion for <install folder> and the folders the
        related products recorded; activation from those folders exits 3
        until it is released (installer, early in the script)
  end-upgrade --user|--machine
        release it (installer commit)
  start --related <product codes>
        release the user exclusion and run each related per-user product's
        own activation from the folder it recorded (installer rollback)
  start --machine
        release the machine exclusion and start every stopped per-session
        instance of the host service (installer rollback)
  start-presenter --user|--machine
        dispatch the optional installed native presenter through this account's
        unelevated desktop shell after committed installation; unavailable or
        foreign desktops refuse delivery and retain an installer diagnostic
  upgrade-check
        exit 3 while an upgrade of this program's installation is in progress
  remove-sandboxes
        take back every application sandbox a session of this account left
        in its runtime state (apps/*/sandbox.json): the file grants, the
        ancestors' attribute entries and the profile, each list compared
        with its recorded snapshot. A sandbox a session still runs in is
        left to that session. An elevated token is refused with one line;
        run it as the account itself (installer removal)

A failure of an installer command is also appended to installer-actions.txt
beside its scope's exclusion record, because Windows Installer discards the
output of custom actions.
`

type serviceRequest struct {
	command string
	scope   string
	folder  string
	related string
}

func scopeFlag(flag string) string {
	switch flag {
	case "--user":
		return "user"
	case "--machine":
		return "machine"
	}
	return ""
}

func serviceArguments(args []string) (serviceRequest, error) {
	if len(args) == 2 && args[0] == "start-presenter" && scopeFlag(args[1]) != "" {
		return serviceRequest{command: "start-presenter", scope: scopeFlag(args[1])}, nil
	}
	if len(args) == 5 && args[0] == "begin-upgrade" && scopeFlag(args[1]) != "" && args[2] != "" && args[3] == "--related" && args[4] != "" {
		return serviceRequest{command: "begin-upgrade", scope: scopeFlag(args[1]), folder: args[2], related: args[4]}, nil
	}
	if len(args) == 2 && args[0] == "end-upgrade" && scopeFlag(args[1]) != "" {
		return serviceRequest{command: "end-upgrade", scope: scopeFlag(args[1])}, nil
	}
	if len(args) == 3 && args[0] == "start" && args[1] == "--related" && args[2] != "" {
		return serviceRequest{command: "start", scope: "user", related: args[2]}, nil
	}
	if len(args) == 2 && args[0] == "start" && args[1] == "--machine" {
		return serviceRequest{command: "start", scope: "machine"}, nil
	}
	if len(args) == 1 && args[0] == "upgrade-check" {
		return serviceRequest{command: "upgrade-check"}, nil
	}
	if len(args) == 1 && args[0] == "remove-sandboxes" {
		return serviceRequest{command: "remove-sandboxes", scope: "user"}, nil
	}
	return serviceRequest{}, errors.New("service begin-upgrade --user|--machine <install folder> --related <product codes>, " +
		"service end-upgrade --user|--machine, service start --related <product codes> | --machine, service upgrade-check, " +
		"or service remove-sandboxes")
}

func serviceCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, serviceUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "service: a command is required", "openabstractions service --help")
	}
	request, err := serviceArguments(args)
	if err != nil {
		return commandMistake(diagnostics, "service: "+err.Error(), "openabstractions service --help")
	}
	switch request.command {
	case "start-presenter":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = activateInstalledPresenter(ctx, output)
	case "begin-upgrade":
		err = serviceBeginUpgrade(request.scope, request.folder, request.related, output)
	case "end-upgrade":
		err = serviceEndUpgrade(request.scope, output)
	case "start":
		if request.scope == "machine" {
			err = serviceStartMachine(output)
		} else {
			err = serviceStartUser(request.related, output)
		}
	case "upgrade-check":
		err = refuseDuringUpgrade()
		if err == nil {
			err = printLine(output, "no upgrade of this installation is in progress")
		}
	case "remove-sandboxes":
		var state string
		if state, err = runtimeStateDir("windows", os.Getenv, os.UserHomeDir); err == nil {
			err = removeLeftoverSandboxes(state, output, windows.GetCurrentProcessToken().IsElevated, sandbox.Remove)
		}
	}
	if err != nil {
		if command, scope := installerAction(request); command != "" {
			recordInstallerFailure(installerActionsPath, scope, "service "+command, err, time.Now())
		}
		return upgradeRefusal(err)
	}
	return nil
}

// installerAction names the commands the MSI runs, with their scope.
func installerAction(request serviceRequest) (command, scope string) {
	switch {
	case request.command == "start-presenter":
		return "start-presenter --" + request.scope, request.scope
	case request.command == "begin-upgrade" || request.command == "end-upgrade":
		return request.command + " --" + request.scope, request.scope
	case request.command == "start" && request.scope == "machine":
		return "start --machine", "machine"
	case request.command == "start":
		return "start --related", "user"
	case request.command == "remove-sandboxes":
		return "remove-sandboxes", "user"
	}
	return "", ""
}

func serviceBeginUpgrade(scope, folder, related string, output io.Writer) error {
	info := msiProductInfo
	if scope == "machine" {
		info = msiMachineProductInfo
	}
	products, err := relatedProducts(related, info)
	if err != nil {
		return err
	}
	folders, notes, err := upgradeFolders(folder, products)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if err := printLine(output, note); err != nil {
			return err
		}
	}
	holder, err := installerIdentity()
	if err != nil {
		return fmt.Errorf("cannot identify the installer that holds the upgrade exclusion: %w", err)
	}
	if err := systemExclusionEnv().begin(scope, folders, holder); err != nil {
		return err
	}
	return printLine(output, fmt.Sprintf("%s upgrade exclusion held by %s (pid %d) for %s", scope, holder.Image, holder.PID, strings.Join(folders, "; ")))
}

func serviceEndUpgrade(scope string, output io.Writer) error {
	path, err := exclusionPath(scope)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return printLine(output, scope+" upgrade exclusion released")
}
