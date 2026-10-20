package product

// DesktopInstallerDiagnostics describes the diagnostic records owned by a
// shipped desktop installer. Only these records can reach the Panel.
type DesktopInstallerDiagnostics struct {
	Title                string
	Prefix               string
	ProcessAction        string
	Actions              []string
	FailureStages        []string
	ChildStages          []string
	SettingsAction       string
	SettingsConflictExit string
}

// DesktopInstallerLog selects the grammar for an admitted Catalog app.
func DesktopInstallerLog(app string) (DesktopInstallerDiagnostics, bool) {
	if app != "opencode" {
		return DesktopInstallerDiagnostics{}, false
	}
	// The adopter's integration.wxs and installer_host.c own these names.
	const processAction = "CheckOpenCodeClosed"
	const settingsAction = "InstallOpenCodeIntegration"
	return DesktopInstallerDiagnostics{
		Title:         AgentConnectionTitle(app),
		Prefix:        "OA OpenCode",
		ProcessAction: processAction,
		Actions: []string{
			processAction, settingsAction, "RemoveOpenCodeIntegration", "RollbackOpenCodeInstall",
			"RollbackOpenCodeRemove", "CommitOpenCodeInstall", "CommitOpenCodeRemove",
		},
		FailureStages: []string{
			"guard_refused", "guard_exception", "trusted_payload", "desktop_fix", "prepare_integration",
			"prepared_image_receipt", "configuration_request", "configure", "configuration_result",
		},
		ChildStages:          []string{"prepare_integration", "configure"},
		SettingsAction:       settingsAction,
		SettingsConflictExit: "80",
	}, true
}
