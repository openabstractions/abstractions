package main

import "embed"

// The PE resource and sealed Mac bundle resource use this same manifest.
// Generated browser modules remain exact copies of the canonical SDK owners.
//
//go:embed panel-application.json static/native-sdk
var panelOwnedResources embed.FS
