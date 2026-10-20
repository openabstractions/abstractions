package nativecli

import (
	"context"
	"errors"
	"os"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// Keep the admitted source object stable while its expected bytes and roster
// are read. A preexisting writer or replacement conflicts with this hold.
func OpenSource(ctx context.Context, name string) (*os.File, error) {
	admitted, err := fileid.Open(ctx, fileid.Self, name, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer admitted.Close()
	path := admitted.FileID().Path()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	key, err := fileid.FileKeyOf(f)
	if err != nil || key != admitted.FileID().Key() {
		return nil, errors.Join(err, f.Close(), errors.New("archive source changed during admission"))
	}
	return f, nil
}
