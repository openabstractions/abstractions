//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// applicationHeldStamp names the held image by what its hold already read:
// file identity, size, creation and write times, and the declaration bytes.
// A rewrite changes the write time; an equal stamp only ever refuses.
func applicationHeldStamp(h *heldApplicationManifest) string {
	if h == nil || h.file == nil || h.key == "" {
		return ""
	}
	s := h.stamp
	declared := sha256.Sum256(h.Bytes)
	return fmt.Sprintf("%s|%d|%d|%d|%d|%d|%s", h.key, s.FileSizeHigh, s.FileSizeLow,
		uint64(s.CreationTime.HighDateTime)<<32|uint64(s.CreationTime.LowDateTime),
		uint64(s.LastWriteTime.HighDateTime)<<32|uint64(s.LastWriteTime.LowDateTime),
		s.VolumeSerialNumber, hex.EncodeToString(declared[:]))
}
