//go:build !windows

package main

import "context"

func panelSettingsGuard(context.Context) (func(), error) { return func() {}, nil }
