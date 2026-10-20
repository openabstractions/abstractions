package main

// The Device Storage section reads reported content-store sizes through
// Inventory.List, classifying declared/observed holds under STO-T8. Rows
// retain their policy descriptions while a refused read reports unavailable.
// Working folders and staging are outside this inventory. The native store's
// operator-only removal candidates come from its own bound service.

// storageClassRow is one class: its person-facing label and keep rule.
type storageClassRow struct {
	Class string
	Label string
	Keep  string
}

// storageClassRows lists the classes in the collector's removal order
// (STO-T3), with data, which it never touches, last.
var storageClassRows = []storageClassRow{
	{Class: "tmp", Label: "Temporary files", Keep: "Removed when the work that used them ends."},
	{Class: "cache", Label: "Cache", Keep: "Reused by OpenAbstractions."},
	{Class: "logs", Label: "Logs", Keep: "Kept within the configured size and age limits."},
	{Class: "data", Label: "Data", Keep: "Kept until you remove it."},
}

// storageSection is the section's markup.
var storageSection = func() string {
	rows := ""
	for _, row := range storageClassRows {
		rows += `<div class="oa-row" data-storage="` + row.Class + `"><div class="oa-row-text"><span class="oa-row-label">` + row.Label +
			`</span><span class="oa-row-caption" id="storage-keep-` + row.Class + `">` + row.Keep + `</span></div><span id="storage-size-` + row.Class + `">…</span></div>`
	}
	return oaGroup("storage-section", "Storage", "Space OpenAbstractions uses on this device.",
		`<p class="oa-status" id="storage-status" role="status" aria-live="polite" data-state="loading">Reading storage sizes…</p><p id="storage-free" hidden></p>`, rows, `<div id="storage-items" hidden><h3>Largest items</h3><div id="storage-large"></div><h3>Duplicates</h3><div id="storage-matches"></div></div>`, `<div id="storage-committed" hidden><h3>Stored content</h3><div id="storage-committed-rows"></div><button type="button" id="storage-committed-prev" hidden>Previous</button><button type="button" id="storage-committed-next" hidden>Next</button></div><p id="storage-removal-status" role="status" aria-live="polite"></p>`)
}()
