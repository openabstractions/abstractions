//go:build windows || darwin

package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// AgentParty selects an integration image. It conveys no enrollment or rights.
type AgentParty string

const (
	AgentCodex    AgentParty = "codex"
	AgentClaude   AgentParty = "claude"
	AgentOpenCode AgentParty = "opencode"
)

// AgentImage retains a newly created integration image for this Setup lifetime.
// Close releases the hold; it leaves the image for the enrolled application.
// Existing files require their owning registration/update flow and are refused
// here, even if their bytes happen to match the installed gateway.
type AgentImage struct {
	*nativecli.Image
	file     *os.File
	previous AgentEnrollmentCapture
}

func newAgentImage(file *os.File, directory *programfiles.Hold, program, key string) *AgentImage {
	return &AgentImage{Image: nativecli.AdoptImage(file, directory, program, key), file: file}
}

func (a *AgentImage) PreviousEnrollment() AgentEnrollmentCapture { return a.previous }

// AgentGatewayProgram selects and holds the installed gateway for read-only
// inventory. It creates no integration image and starts no process.
func AgentGatewayProgram(ctx context.Context) (*AgentImage, error) {
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("agent integration requires the installed runtime")
	}
	selected, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return nil, err
	}
	path := agentGatewayPath(selected.Server.Program)
	directory, err := programfiles.HoldDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	input, key, err := openAgentImage(ctx, path)
	if err != nil {
		return nil, errors.Join(err, directory.Release())
	}
	result := newAgentImage(input, directory, path, key)
	if err := result.Check(ctx); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}

func invalidAgentImageParty(party AgentParty) bool {
	if party != AgentCodex && party != AgentClaude && party != AgentOpenCode {
		return true
	}
	return false
}

// PrepareAgentImage copies only the selected installed runtime's gateway. It
// performs no registration, vendor command, runtime activation or enrollment.
func PrepareAgentImage(ctx context.Context, party AgentParty) (*AgentImage, error) {
	if invalidAgentImageParty(party) {
		return nil, errors.New("unknown agent integration")
	}
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("agent integration requires the installed runtime")
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	root := filepath.Join(home.Config, "agent-integrations")
	if err := privatedir.Ensure(root); err != nil {
		return nil, err
	}
	return copyAgentImage(ctx, source.Program(), root, party)
}

// copyAgentImage is the TEMP-fixture seam. Production supplies the installed
// component and the private Setup directory above; callers cannot select them.
func copyAgentImage(ctx context.Context, source, root string, party AgentParty) (*AgentImage, error) {
	return copyAgentImageNamed(ctx, source, root, party, agentImageName(party))
}
func copyAgentImageNamed(ctx context.Context, source, root string, party AgentParty, name string) (*AgentImage, error) {
	if party != AgentCodex && party != AgentClaude && party != AgentOpenCode {
		return nil, errors.New("unknown agent integration")
	}
	directory, err := programfiles.HoldDirectory(root)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*AgentImage, error) { return nil, errors.Join(err, directory.Release()) }
	input, inputKey, err := openAgentImage(ctx, source)
	if err != nil {
		return fail(err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 256<<20 {
		return fail(errors.New("installed integration component is not a bounded regular image"))
	}
	admitted, err := fileid.Open(ctx, fileid.Self, source, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fail(err)
	}
	sourceID := admitted.FileID()
	admitted.Close()
	if inputKey != sourceID.Key() {
		return fail(errors.New("installed integration component changed"))
	}
	if sourceID.IsDir() || sourceID.Links() != 1 {
		return fail(errors.New("installed integration component has multiple names"))
	}
	parent, err := fileid.Open(ctx, fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fail(err)
	}
	defer parent.Close()
	output, err := fileid.CreateExclusive(parent, name)
	if err != nil {
		return fail(fmt.Errorf("agent integration target exists or cannot be created; update/restart review required: %w", err))
	}
	path, key := output.FileID().Path(), output.FileID().Key()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output.File(), hash), &agentImageReader{ctx: ctx, reader: input})
	modeErr := agentImageMode(output.File())
	syncErr := output.File().Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, modeErr, syncErr, closeErr); err != nil {
		return fail(err)
	}
	held, actualKey, err := openAgentImage(ctx, path)
	if err != nil {
		return fail(err)
	}
	bad := func(err error) (*AgentImage, error) { return fail(errors.Join(err, held.Close())) }
	if actualKey != key || key == sourceID.Key() {
		return bad(errors.New("agent integration image identity changed"))
	}
	actual := sha256.New()
	if _, err := io.Copy(actual, &agentImageReader{ctx: ctx, reader: held}); err != nil {
		return bad(err)
	}
	if string(actual.Sum(nil)) != string(hash.Sum(nil)) {
		return bad(errors.New("agent integration image bytes changed"))
	}
	result := newAgentImage(held, directory, path, key)
	if err := result.Check(ctx); err != nil {
		return bad(err)
	}
	return result, nil
}

type agentImageReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *agentImageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
