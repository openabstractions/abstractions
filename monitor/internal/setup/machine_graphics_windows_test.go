//go:build windows

package setup

import (
	"strings"
	"testing"
)

// The display class is readable without elevation and every adapter it
// names is a PCI or other bus hardware id; reading it opens no device.
func TestDisplayAdaptersReadTheDisplayClass(t *testing.T) {
	hardware, err := displayAdapters()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hardware {
		if !strings.Contains(id, `\`) {
			t.Fatalf("adapter id %q is not a hardware id", id)
		}
	}
	t.Logf("adapters %v, graphics families %v", hardware, graphicsOf(hardware))
}
