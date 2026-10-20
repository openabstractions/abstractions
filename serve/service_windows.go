package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"golang.org/x/sys/windows"
)

// `openabstractions service …` are the commands Burn and Windows Installer run
// around an upgrade. Burn's early package and the MSI Binary table carry the
// same incoming openabstractions.exe.

const serviceUsage = `Usage: openabstractions service <command>

  begin-upgrade --user|--machine <install folder> --related <product codes>
        hold the upgrade exclusion for <install folder> and the folders the
        related products recorded, then close and await their identified OA
        processes; activation from those folders exits 3 until it is released
        (installer, early in the script)
  begin-upgrade --machine --installed --chain <32 hex characters>
        resolve installed core products and hold their folders for this Burn
        chain before Windows Installer begins; no product writes no record
  begin-removal --user|--machine <install folder>
        hold the same exclusion and close identified OA processes for the
        removing package folder without querying its product registration
  end-upgrade --user|--machine
        release it (installer commit)
  start --related <product codes>
        release the user exclusion and run each related per-user product's
        own activation from the folder it recorded (installer rollback)
  start --user <install folder> <version>
        release the user exclusion and run the removing package's own
        activation without querying its registration (removal rollback)
  start --machine
        release the machine exclusion and start every stopped per-session
        instance of the host service (installer rollback)
  start-presenter --user|--machine
        dispatch the optional installed native presenter through this account's
        unelevated desktop shell after committed installation; unavailable or
        foreign desktops refuse delivery and retain an installer diagnostic
  upgrade-check
        exit 3 while an upgrade of this program's installation is in progress
  inspect-upgrade --user|--machine <install folder>
        read-only census and stop-right admission in this caller's context;
        close every handle, write no exclusion and stop no process. This
        does not prove SYSTEM-context admission or exclusion write access.
  remove-sandboxes
        take back every application sandbox a session of this account left
        in its runtime state (apps/*/sandbox.json): the file grants, the
        ancestors' attribute entries and the profile, each list compared
        with its recorded snapshot. A sandbox a session still runs in is
        left to that session. An elevated token is refused with one line;
        run it as the account itself (installer removal)
  remove-login --user <prior ToolsFolder>
        remove this account's two exact-owned login Run values after committed
        uninstall; foreign values and other accounts are retained

A failure of an installer command is also appended to installer-actions.txt
beside its scope's exclusion record, because Windows Installer discards the
output of custom actions.
`

type serviceRequest struct {
	command   string
	scope     string
	folder    string
	related   string
	version   string
	chain     string
	installed bool
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
	if len(args) == 5 && args[0] == "begin-upgrade" && args[1] == "--machine" && args[2] == "--installed" && args[3] == "--chain" && validUpgradeChain(args[4]) {
		return serviceRequest{command: "begin-upgrade", scope: "machine", installed: true, chain: args[4]}, nil
	}
	if len(args) == 7 && args[0] == "begin-upgrade" && args[5] == "--chain" && (args[6] == "" || validUpgradeChain(args[6])) {
		request, err := serviceArguments(args[:5])
		request.chain = args[6]
		return request, err
	}
	if len(args) == 3 && args[0] == "inspect-upgrade" && scopeFlag(args[1]) != "" && args[2] != "" {
		return serviceRequest{command: "inspect-upgrade", scope: scopeFlag(args[1]), folder: args[2]}, nil
	}
	if len(args) == 4 && args[0] == "start" && args[1] == "--user" && args[2] != "" && args[3] != "" {
		return serviceRequest{command: "start", scope: "user", folder: args[2], version: args[3]}, nil
	}
	if len(args) == 3 && args[0] == "remove-login" && args[1] == "--user" && args[2] != "" {
		return serviceRequest{command: "remove-login", scope: "user", folder: args[2]}, nil
	}
	if len(args) == 2 && args[0] == "start-presenter" && scopeFlag(args[1]) != "" {
		return serviceRequest{command: "start-presenter", scope: scopeFlag(args[1])}, nil
	}
	if len(args) == 3 && args[0] == "begin-removal" && scopeFlag(args[1]) != "" && args[2] != "" {
		return serviceRequest{command: "begin-removal", scope: scopeFlag(args[1]), folder: args[2]}, nil
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
		"service begin-removal --user|--machine <install folder>, service end-upgrade --user|--machine, service start --related <product codes> | --user <install folder> <version> | --machine, service upgrade-check, " +
		"service remove-login --user <prior ToolsFolder>, or service remove-sandboxes")
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
	if request.command == "inspect-upgrade" {
		return serviceInspectUpgrade(request, output)
	}
	releaseSettings, err := runtimeSettingsGuard(context.Background(), runtimeFlags{})
	if err != nil {
		return err
	}
	defer releaseSettings()
	switch request.command {
	case "remove-login":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = removeInstalledRuntimeLogon(ctx, request.folder)
	case "start-presenter":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err = activateInstalledPresenter(ctx, output)
	case "begin-upgrade", "begin-removal":
		err = serviceBeginExclusion(request, output)
	case "end-upgrade":
		err = serviceEndUpgrade(request.scope, output)
	case "start":
		if request.scope == "machine" {
			err = serviceStartMachine(output)
		} else {
			err = serviceStartUser(request, output)
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
	case request.command == "remove-login":
		return "remove-login --user", "user"
	case request.command == "start-presenter":
		return "start-presenter --" + request.scope, request.scope
	case request.command == "begin-upgrade" || request.command == "begin-removal" || request.command == "end-upgrade":
		return request.command + " --" + request.scope, request.scope
	case request.command == "start" && request.scope == "machine":
		return "start --machine", "machine"
	case request.command == "start" && request.folder != "":
		return "start --user", "user"
	case request.command == "start":
		return "start --related", "user"
	case request.command == "remove-sandboxes":
		return "remove-sandboxes", "user"
	}
	return "", ""
}

func serviceBeginExclusion(request serviceRequest, output io.Writer) error {
	scope := request.scope
	info := msiProductInfo
	if scope == "machine" {
		info = msiMachineProductInfo
	}
	folders, predecessors, notes, err := serviceExclusionFolders(request, info)
	if err != nil {
		return fmt.Errorf("upgrade exclusion stage=folders scope=%s folder=%q: %w", scope, request.folder, err)
	}
	for _, note := range notes {
		if err := printLine(output, note); err != nil {
			return err
		}
	}
	if len(folders) == 0 {
		if request.installed {
			recordEarlyUpgradeStop(installerActionsPath, "no-related-product", earlyUpgradeStop{guard: "unchecked"}, time.Now())
		}
		return printLine(output, "no installed core product; no upgrade exclusion written")
	}
	holder, err := installerIdentity()
	if err != nil {
		return fmt.Errorf("cannot identify the installer that holds the upgrade exclusion: %w", err)
	}
	var tools *os.File
	if !request.installed && request.command == "begin-upgrade" {
		var release func()
		tools, release = holdSameUpgradeTools(request.folder, predecessors, folders)
		if release != nil {
			defer release()
		}
	}
	env := systemExclusionEnv()
	report := earlyUpgradeStop{folders: len(folders)}
	var admission, held func(upgradeCensusObservation)
	if request.installed {
		admission, held = report.admission.observe, report.held.observe
	}
	if err := holdAndStopUpgradeAdmission(scope, folders, holder, env, func(ctx context.Context, folders []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := readOnlyUpgradeCensus(ctx, folders, systemUpgradeCensusOps(), admission)
		return errors.Join(err, ctx.Err())
	}, func(ctx context.Context, folders []string) error {
		return stopUpgradeProcesses(ctx, folders, func(folders []string) ([]upgradeStopProcess, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			report.scans++
			return censusUpgradeProcessesContext(ctx, folders, systemUpgradeCensusOps(), held)
		})
	}, request.chain); err != nil {
		return err
	}
	if request.installed {
		report.guard, report.holderPID = earlyUpgradeGuard(env, scope, folders)
		recordEarlyUpgradeStop(installerActionsPath, "held-census-empty", report, time.Now())
	} else if request.command == "begin-upgrade" {
		reportUpgradeAside(output, scope, "service begin-upgrade --"+scope, moveUpgradeImagesAsideFor(env, scope, folders, tools, holder))
	}
	return printLine(output, fmt.Sprintf("%s upgrade exclusion held by %s (pid %d) for %s", scope, holder.Image, holder.PID, strings.Join(folders, "; ")))
}

// A real removal receives its own folder from MSI before files are removed.
// Its registration can be unavailable inside the removal transaction. Upgrades
// still require every related product registration to agree with discovery.
func serviceExclusionFolders(request serviceRequest, info productInfoFunc) ([]string, []relatedProduct, []string, error) {
	if request.installed {
		folders, notes, err := installedCoreFolders(info, installedCoreProducts, func() (string, error) {
			folder, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
			if err != nil {
				return "", err
			}
			return filepath.Join(folder, "OpenAbstractions"), nil
		})
		return folders, nil, notes, err
	}
	if request.command == "begin-removal" {
		folders, notes, err := upgradeFolders(request.folder, nil)
		return folders, nil, notes, err
	}
	products, err := relatedProducts(request.related, info)
	if err != nil {
		return nil, nil, nil, err
	}
	folders, notes, err := upgradeFolders(request.folder, products)
	return folders, products, notes, err
}

func serviceEndUpgrade(scope string, output io.Writer) error {
	notes, _, err := settleInstallerUpgrade(scope, false)
	reportUpgradeAside(output, scope, "service end-upgrade --"+scope, notes)
	if err != nil {
		return err
	}
	return printLine(output, fmt.Sprintf("%s upgrade exclusion released", scope))
}

func settleInstallerUpgrade(scope string, rollback bool) (notes, releaseNotes []string, err error) {
	holder, err := installerIdentity()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot identify the installer settling the upgrade exclusion: %w", err)
	}
	return settleUpgradeAsideOperation(systemExclusionEnv(), scope, rollback, holder, true)
}
