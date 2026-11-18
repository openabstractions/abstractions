package product

// AgentConnectionTitle is the Catalog display title for an assistant
// connection. Routing and update participation have their own owners.
func AgentConnectionTitle(party string) string {
	switch party {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "opencode":
		return "OpenCode"
	}
	return ""
}

// AgentConnectionUpdateOrder lists the assistant connections in batch order.
// The updater still checks the existing enrollment for each connection.
func AgentConnectionUpdateOrder() []string {
	return []string{"codex", "claude", "opencode"}
}

// InstalledAgentConnectionParty identifies the bundle-owned stock application
// whose connection combines host registration with the native helper's Keep.
func InstalledAgentConnectionParty() string { return "opencode" }

// OfficialProgram is a vendor image's path beneath the account's local
// application data and the Authenticode signer the catalogue requires.
// UninstallKey is the per-user uninstall registration the vendor's own
// installer writes when it reports success; the adopter's desktop_state.h
// OA_DESKTOP_ARP names the same key.
type OfficialProgram struct {
	LocalAppData string
	Signer       string
	UninstallKey string
}

// OfficialAgentProgram gives Setup and native admission the same vendor pin.
func OfficialAgentProgram(party string) (OfficialProgram, bool) {
	if party != InstalledAgentConnectionParty() {
		return OfficialProgram{}, false
	}
	return OfficialProgram{LocalAppData: "Programs/@opencode-aidesktop/OpenCode.exe", Signer: "Anomaly Innovations, Inc https://anoma.ly/",
		UninstallKey: "d074f30d-5f88-5885-b075-be1348cc7676"}, true
}

// InstalledAgentConnectionCLI names the fixed vendor and installer commands.
// Read is the private read-only command, outside the Panel flag set.
func InstalledAgentConnectionCLI() (read, connect, prepare string) {
	return "--read-opencode-connection", "connect-opencode", "prepare-opencode-integration"
}
