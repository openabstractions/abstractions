package main

import (
	"io"
	"runtime"
	"strings"
	"testing"

	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

// --get downloads the pinned sd-server archive and every file of the set by
// its SHA-256 from its publisher's https source; only Lightricks' gated files
// carry the named credential, and the flags are refused outside --get.
func TestGetVideoDownloadsEveryPinnedFileOfTheSet(t *testing.T) {
	pin := modelhost.SdCppMaster843WindowsVulkan
	files := videoSetFiles(modelhost.LTX25, pin)
	if len(files) != 1+len(modelhost.LTX25.Files) || files[0].sha256 != pin.ArchiveSHA256 || files[0].size != pin.ArchiveSize {
		t.Fatalf("files %+v", files)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.url, "https://") || len(f.sha256) != 64 || f.size <= 0 || f.name == "" {
			t.Fatalf("%+v is not a pinned https download", f)
		}
	}
	if credentialFor(modelhost.LTX25.Files[0].Source, "hf") != "" || credentialFor(modelhost.LTX25.Files[2].Source, "hf") != "hf" {
		t.Fatal("the credential goes to the wrong publisher")
	}
	for _, args := range [][]string{{"--get"}, {"--get", "--video-model", "ltx-2.5", "--video-engine", `C:\sd\sd-server.exe`},
		{"--video-model", "ltx-2.5", "--credential", "hf"}, {"--get", "--video-model", "ltx-2.5", "--credential", "bad name"}} {
		err := modelsHostCommand("models host", args, io.Discard, io.Discard)
		if runtime.GOOS == "windows" || nativeProviderLimit == "" {
			assertExit(t, err, exitUsage, strings.Join(args, " "))
		}
	}
}
