//go:build !windows

package main

import "path/filepath"

// finalPath is an existing path with its symbolic links resolved.
var finalPath = filepath.EvalSymlinks
