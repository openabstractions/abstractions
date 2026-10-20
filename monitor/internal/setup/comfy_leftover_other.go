//go:build !windows

package setup

import "io/fs"

// reparsePoint is a Windows fact; Lstat names other links by their type.
func reparsePoint(fs.FileInfo) bool { return false }
