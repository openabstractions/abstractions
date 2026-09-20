package main

// exitUpgradeInProgress is the status every activation path returns while an
// installer replaces the installation: `start`, `serve host`, `service
// upgrade-check`, and the SDK's on-demand activation reads it from `start`.
const exitUpgradeInProgress = 3
