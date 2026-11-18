//go:build !windows

package setup

// ReadOfficialInstall has no installer records to read off Windows.
func ReadOfficialInstall(AppID) OfficialInstallEvidence { return OfficialInstallEvidence{} }
