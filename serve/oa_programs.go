package main

import "runtime"

// OA's own program images by file stem. Each site that names OA's programs
// reads one of these two sets.
//
// oaRuntimeProgramStems are the runtime's images: the console image and the
// windowless twin the supervisor host runs. Model hosting admits these alone,
// since only the runtime serves models.
//
// oaProgramStems adds the person's operator programs installed beside them:
// the Panel, and the Asks presenter that draws native questions and
// notifications for it. Operator credentials, factory reset's running-process
// check and the upgrade stop set read this set.
var oaRuntimeProgramStems = []string{"openabstractions", "openabstractionsw"}
var oaProgramStems = append(oaRuntimeProgramStems[:len(oaRuntimeProgramStems):len(oaRuntimeProgramStems)], oaPanelProgramStem, oaPresenterProgramStem)

const oaPanelProgramStem = "Abstraction Panel"

// oaPresenterProgramStem is the Asks presenter's image.
const oaPresenterProgramStem = "OpenAbstractions Presenter"

// oaProgramFiles is stems as this platform's program file names.
func oaProgramFiles(stems []string) []string {
	files := make([]string, len(stems))
	for i, stem := range stems {
		files[i] = stem
		if runtime.GOOS == "windows" {
			files[i] += ".exe"
		}
	}
	return files
}
