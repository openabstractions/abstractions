package main

import "log"

// setupPackageCardFacts is the Catalog wire shape. Its OA build fields refer
// to a retained package checked by the adapter's own Setup journal.
type setupPackageCardFacts struct {
	State           string `json:"state"`
	Kind            string `json:"kind,omitempty"`
	Folder          string `json:"folder,omitempty"`
	Connectable     bool   `json:"connectable"`
	Open            bool   `json:"open"`
	Update          bool   `json:"update,omitempty"`
	Changed         bool   `json:"changed,omitempty"`
	UpdateBlocked   string `json:"update_blocked,omitempty"`
	Restart         bool   `json:"restart,omitempty"`
	OABuild         bool   `json:"oa_build"`
	OABuildRemoving bool   `json:"oa_build_removing,omitempty"`
	DownloadPage    string `json:"download_page"`
}

// A discovered host carries only the observations needed for presentation.
// Registry and update probes are deferred until the host state needs them.
type packageCardHost struct {
	kind, folder        string
	canOpen, integrated bool
	kept                func() (kept, known bool)
	changed             func() bool
	outdated            func() bool
	updateBlocked       func() string
	restartPending      func() bool
}

func presentPackageCard(target packageConnectTarget, downloadPage string, pinned, pinReadable bool, host packageCardHost, present bool, pending func() (string, bool), retained func() (bool, bool)) setupPackageCardFacts {
	facts := setupPackageCardFacts{State: "get", Connectable: pinned, DownloadPage: downloadPage}
	if present {
		facts.State, facts.Kind, facts.Folder, facts.Open = "connect", host.kind, host.folder, host.canOpen
		if host.integrated {
			renderIntegratedPackage(target.title, &facts, pinReadable, host)
		}
	} else if kind, ok := pending(); ok {
		facts.State, facts.Kind, facts.Open = "finish", kind, true
	}
	facts.OABuild, facts.OABuildRemoving = retained()
	return facts
}

func renderIntegratedPackage(title string, facts *setupPackageCardFacts, pinReadable bool, host packageCardHost) {
	kept, known := host.kept()
	if known && !kept {
		// An already installed integration can reconnect without a new
		// package. A mismatched release pin still blocks Connect.
		facts.Connectable = pinReadable
		return
	}
	if !known {
		log.Printf("setup: %s's Apps entry could not be read; the row reads its integration folder", title)
	}
	facts.State = "connected"
	switch {
	case host.changed():
		facts.Changed = true
	case host.outdated():
		facts.Update, facts.UpdateBlocked = true, host.updateBlocked()
	default:
		facts.Restart = host.restartPending()
	}
}
