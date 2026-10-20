package main

import (
	"reflect"
	"testing"
)

func TestPackageCardPresentsAnotherDeclaredWorkspace(t *testing.T) {
	target := packageConnectTarget{registration: "other-editor", title: "Other Editor", program: "connector"}
	const page = "https://example.test/editor"
	for _, tc := range []struct {
		name                                                                                 string
		present, finishing, integrated, kept, known, changed, outdated, restart, pinReadable bool
		want                                                                                 setupPackageCardFacts
		probes                                                                               []string
	}{
		{name: "get", want: setupPackageCardFacts{State: "get"}, probes: []string{"pending"}},
		{name: "finish", finishing: true, want: setupPackageCardFacts{State: "finish", Kind: "desktop", Open: true}, probes: []string{"pending"}},
		{name: "connect", present: true, want: setupPackageCardFacts{State: "connect", Kind: "portable", Folder: "installed", Open: true}},
		{name: "waiting for Keep", present: true, integrated: true, known: true, pinReadable: true,
			want: setupPackageCardFacts{State: "connect", Kind: "portable", Folder: "installed", Open: true, Connectable: true}, probes: []string{"keep"}},
		{name: "invalid release pin", present: true, integrated: true, known: true,
			want: setupPackageCardFacts{State: "connect", Kind: "portable", Folder: "installed", Open: true}, probes: []string{"keep"}},
		{name: "Registry unavailable", present: true, integrated: true,
			want: setupPackageCardFacts{State: "connected", Kind: "portable", Folder: "installed", Open: true}, probes: []string{"keep", "changed", "outdated", "restart"}},
		{name: "changed", present: true, integrated: true, known: true, kept: true, changed: true,
			want: setupPackageCardFacts{State: "connected", Kind: "portable", Folder: "installed", Open: true, Changed: true}, probes: []string{"keep", "changed"}},
		{name: "update", present: true, integrated: true, known: true, kept: true, outdated: true,
			want: setupPackageCardFacts{State: "connected", Kind: "portable", Folder: "installed", Open: true, Update: true, UpdateBlocked: "Package unavailable"}, probes: []string{"keep", "changed", "outdated", "blocker"}},
		{name: "restart", present: true, integrated: true, known: true, kept: true, restart: true,
			want: setupPackageCardFacts{State: "connected", Kind: "portable", Folder: "installed", Open: true, Restart: true}, probes: []string{"keep", "changed", "outdated", "restart"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes []string
			record := func(name string) { probes = append(probes, name) }
			host := packageCardHost{kind: "portable", folder: "installed", canOpen: true, integrated: tc.integrated,
				kept:           func() (bool, bool) { record("keep"); return tc.kept, tc.known },
				changed:        func() bool { record("changed"); return tc.changed },
				outdated:       func() bool { record("outdated"); return tc.outdated },
				updateBlocked:  func() string { record("blocker"); return "Package unavailable" },
				restartPending: func() bool { record("restart"); return tc.restart },
			}
			got := presentPackageCard(target, page, false, tc.pinReadable, host, tc.present,
				func() (string, bool) { record("pending"); return "desktop", tc.finishing },
				func() (bool, bool) { record("retained"); return true, true },
			)
			tc.want.DownloadPage, tc.want.OABuild, tc.want.OABuildRemoving = page, true, true
			if got != tc.want || !reflect.DeepEqual(probes, append(tc.probes, "retained")) {
				t.Fatalf("card %+v, probes %v, want %+v, probes %v", got, probes, tc.want, append(tc.probes, "retained"))
			}
		})
	}
}
