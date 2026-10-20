//go:build !windows

package setup

import "io/fs"

func discoveryReparse(info fs.FileInfo) bool {
	return info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}
