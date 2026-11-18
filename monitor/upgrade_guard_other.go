//go:build !windows

package main

// exitDuringUpgrade is the Windows Setup early stop's guard; other platforms'
// installers stop the Panel themselves.
func exitDuringUpgrade() {}
