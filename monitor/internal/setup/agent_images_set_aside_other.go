//go:build !windows

package setup

import "context"

// RemoveSetAsideAgentImages has nothing to sweep here: only the Windows helper
// replacement moves a running image aside.
func RemoveSetAsideAgentImages(context.Context) []string { return nil }
