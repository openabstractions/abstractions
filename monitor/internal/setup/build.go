package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

// PanelBuild names the running Panel build: the size and modification time
// of its executable. An OpenAbstractions update replaces the executable and
// with it the name. Setup's journals record the build that approved or
// continued their work, so a later build reads that work as interrupted by
// the update (backlog/hp4-hp5-adopter-updates.md, "Setup installs on the
// person's press"). It is empty when the executable cannot be read. Tests
// replace it.
var PanelBuild = sync.OnceValue(executableBuild)

func executableBuild() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(self)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())))
	return hex.EncodeToString(sum[:8])
}

// InterruptedByUpdate is the install record's interruption by an
// OpenAbstractions update.
const InterruptedByUpdate = "update"
