package main

import (
	"regexp"
	"strings"
	"testing"
)

// The Device page's Storage section shows on arrival one row per storage
// class with its keep rule, in the collector's order with data last
// (STO-T3). Arrival reads sizes from Inventory and Logging. Pagination
// starts hidden and follows the authorized committed-content page.
func TestStorageSectionStatesEachClassOnArrival(t *testing.T) {
	start := strings.Index(cardPage, `<section id="storage-section"`)
	if start < 0 {
		t.Fatal("the Device page has no Storage section")
	}
	section := cardPage[start : start+strings.Index(cardPage[start:], `</section>`)]
	if !strings.Contains(section, "<h2>Storage</h2>") || !strings.Contains(section, "Space OpenAbstractions uses on this device.") {
		t.Fatalf("section heading or caption: %s", section)
	}
	classes := regexp.MustCompile(`data-storage="([a-z]+)"`).FindAllStringSubmatch(section, -1)
	var order []string
	for _, m := range classes {
		order = append(order, m[1])
	}
	if strings.Join(order, ",") != "tmp,cache,logs,data" {
		t.Fatalf("class rows %v", order)
	}
	for _, row := range storageClassRows {
		if !strings.Contains(section, `<span class="oa-row-label">`+row.Label+`</span><span class="oa-row-caption" id="storage-keep-`+row.Class+`">`+row.Keep+`</span>`) {
			t.Fatalf("row %+v not shown", row)
		}
	}
	if !strings.Contains(section, "Reading storage sizes…") {
		t.Fatal("the section must show that sizes are being read")
	}
	for _, endpoint := range []string{"/card/storage", "/card/storage/logs"} {
		if !strings.Contains(cardPage, "call('"+endpoint+"'") {
			t.Fatalf("the section is missing its owned read at %s", endpoint)
		}
	}
	ready := regexp.MustCompile(`var cardReady\s*=\s*Promise\.all\(\[[^\]]*\]\)`).FindString(cardPage)
	if !strings.Contains(ready, "storage()") || !strings.Contains(ready, "storageLogs()") {
		t.Fatal("the initial page read must request inventory and log sizes")
	}
	for _, control := range []string{"<input", "<select", "Clean now"} {
		if strings.Contains(section, control) {
			t.Fatalf("the Storage section offers %s with no operation behind it", control)
		}
	}
	if strings.Count(section, "<button") != 2 ||
		!strings.Contains(section, `id="storage-committed" hidden`) ||
		!strings.Contains(section, `id="storage-committed-prev" hidden`) ||
		!strings.Contains(section, `id="storage-committed-next" hidden`) ||
		!strings.Contains(cardPage, "storageRemovalPage(v.committed,v.committed_next)") {
		t.Fatal("committed-content pagination must start hidden and follow the returned page")
	}
	if !strings.Contains(section, `id="storage-items" hidden`) || !strings.Contains(cardPage, "el('storage-items').hidden=false") {
		t.Fatal("inventory details must appear after the authorized read")
	}
}
