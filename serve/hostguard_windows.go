package main

// hostUpgradeGuard refuses a host launch with exit status 3 while an installer
// replaces this installation.
func hostUpgradeGuard() error { return refuseDuringUpgrade() }
