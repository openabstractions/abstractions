//go:build windows || darwin

package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"

	"github.com/openabstractions/abstraction-identity/fileid"
)

type agentReplacementPin struct {
	ID    fileid.FileID
	Hash  [32]byte
	Bytes int64
}
type agentImagePin = agentReplacementPin

func pinAgentImage(ctx context.Context, image *AgentImage) (agentImagePin, error) {
	var pin agentImagePin
	if err := image.Check(ctx); err != nil {
		return pin, err
	}
	admitted, err := fileid.Open(ctx, fileid.Self, image.Program(), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return pin, err
	}
	defer admitted.Close()
	pin.ID = admitted.FileID()
	if pin.ID.Key() != image.FileKey() || pin.ID.Links() != 1 || pin.ID.IsDir() {
		return pin, errors.New("integration image changed")
	}
	stat, err := image.file.Stat()
	if err != nil {
		return pin, err
	}
	if stat.Size() < 1 || stat.Size() > 256<<20 {
		return pin, errors.New("integration image size is invalid")
	}
	pin.Bytes = stat.Size()
	h := sha256.New()
	n, err := io.Copy(h, &agentImageReader{ctx: ctx, reader: io.NewSectionReader(image.file, 0, pin.Bytes+1)})
	if err != nil || n != pin.Bytes {
		return pin, errors.Join(err, errors.New("integration image changed during read"))
	}
	copy(pin.Hash[:], h.Sum(nil))
	return pin, image.Check(ctx)
}
