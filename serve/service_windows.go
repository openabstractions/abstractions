package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
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
  upgrade-check
        exit 3 while an upgrade of this program's installation is in progress

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
	return serviceRequest{}, errors.New("service begin-upgrade --user|--machine <install folder> --related <product codes>, " +
		"service end-upgrade --user|--machine, service start --related <product codes> | --machine, or service upgrade-check")
}

func serviceCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, serviceUsage)
		return err
	}
	request, err := serviceArguments(args)
	if err != nil {
		io.WriteString(diagnostics, serviceUsage)
		return &exitError{code: 2, err: err}
	}
	switch request.command {
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
	case request.command == "begin-upgrade" || request.command == "end-upgrade":
		return request.command + " --" + request.scope, request.scope
	case request.command == "start" && request.scope == "machine":
		return "start --machine", "machine"
	case request.command == "start":
		return "start --related", "user"
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
