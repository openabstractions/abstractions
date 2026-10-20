package main

// The Swift CFBundleExecutable owns identity and runtime pipes. Its private Go
// child supplies the existing resource server and frame codec, with no Announce.
import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"unicode/utf8"
)

const nativeOwnedChunk = 32 << 10

type nativePanelOwnedChild struct {
	bridge  *nativeAsksBridge
	reader  *io.PipeReader
	writer  *io.PipeWriter
	frames  *panelOwnedFrames
	started bool
	ended   bool
	endOnce sync.Once
	model   *panelChildModel
}

func newNativePanelOwnedChild(bridge *nativeAsksBridge) *nativePanelOwnedChild {
	reader, writer := io.Pipe()
	child := &nativePanelOwnedChild{bridge: bridge, reader: reader, writer: writer}
	child.frames = newPanelOwnedFrames(reader, child, child.end)
	return child
}

func (child *nativePanelOwnedChild) end() {
	child.endOnce.Do(func() {
		_ = child.writer.CloseWithError(io.EOF)
		_ = child.reader.Close()
		_ = child.bridge.write(nativeAsksFrame{Kind: "owned-end"})
	})
}

func (child *nativePanelOwnedChild) accept(reply nativeAsksReply) error {
	if reply.Kind == "owned-core-activation" || reply.Kind == "owned-core-output" || reply.Kind == "owned-core-completed" {
		if child.model == nil || reply.Option != "" || reply.Unlocked != nil || reply.Page != "" || reply.Method != "" || reply.URL != "" || reply.Headers != nil {
			return errors.New("invalid native core control")
		}
		switch reply.Kind {
		case "owned-core-completed":
			if reply.Body != "" || reply.Args != "" || len(reply.Error) > 64 {
				return errors.New("invalid completion acknowledgement")
			}
			return child.model.acknowledge(reply.ID, reply.Error)
		case "owned-core-output":
			if reply.ID != "" || reply.Args != "" || reply.Error != "" {
				return errors.New("invalid native core output")
			}
		case "owned-core-activation":
			if reply.Error != "" {
				return errors.New("invalid native activation")
			}
		}
		body, err := base64.StdEncoding.Strict().DecodeString(reply.Body)
		if err != nil || len(body) > panelOwnedFrameLimit {
			return errors.New("invalid native core bytes")
		}
		if reply.Kind == "owned-core-output" {
			return child.frames.write(body)
		}
		return child.model.activation(reply.ID, reply.Args, body)
	}
	if reply.Option != "" || reply.Error != "" || reply.Unlocked != nil || reply.Page != "" ||
		reply.Args != "" || reply.Method != "" || reply.URL != "" || reply.Headers != nil {
		return errors.New("invalid owned launcher control")
	}
	switch reply.Kind {
	case "owned-bootstrap":
		if child.started || child.ended || reply.Body != "" || !utf8.ValidString(reply.ID) || len(reply.ID) < 1 || len(reply.ID) > 128 {
			child.ended = true
			_ = child.frames.close()
			return nil
		}
		child.started = true
		run, err := newPanelRun("credentials")
		if err != nil {
			return err
		}
		manifest, err := panelOwnedResources.ReadFile("panel-application.json")
		if err != nil {
			return err
		}
		child.model, err = newPanelChildModel(child, manifest)
		if err != nil {
			return err
		}
		if err = child.frames.attachModel(child.model); err != nil {
			return err
		}
		if err = child.bridge.write(nativeAsksFrame{Kind: "owned-core-start", ID: reply.ID, Body: base64.StdEncoding.EncodeToString(manifest)}); err != nil {
			return err
		}
		sdk, err := fs.Sub(panelOwnedResources, "static/native-sdk")
		if err != nil {
			return err
		}
		run.owned = &panelOwnedWindow{frames: child.frames, instance: reply.ID, manifest: string(manifest),
			sdk: http.StripPrefix("/native-host/sdk/", http.FileServerFS(sdk))}
		// Swift intercepts the guarded visibility route on the actual UI thread.
		resources, start := run.resources(panelResourceOriginMacOS)
		child.bridge.servePanel(newNativePanelServer(child.bridge.ctx, resources, child.bridge.write))
		go child.frames.read()
		child.bridge.outMu.Lock()
		err = writePanelURL(child.bridge.out, start, false)
		child.bridge.outMu.Unlock()
		return err
	case "owned-input":
		if reply.ID != "" || len(reply.Body) > base64.StdEncoding.EncodedLen(nativeOwnedChunk) {
			child.ended = true
			_ = child.frames.close()
			return errors.New("invalid owned input chunk")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(reply.Body)
		if err != nil || len(data) == 0 || len(data) > nativeOwnedChunk {
			child.ended = true
			_ = child.frames.close()
			return nil
		}
		if !child.started || child.ended {
			child.ended = true
			_ = child.frames.close()
			return nil
		}
		if _, err := child.writer.Write(data); err != nil {
			child.ended = true
			_ = child.frames.close()
		}
		return nil
	case "owned-end":
		if reply.ID != "" || reply.Body != "" {
			return errors.New("invalid owned retirement")
		}
		child.ended = true
		_ = child.frames.close()
		return nil
	}
	return errors.New("unknown owned launcher control")
}

func (child *nativePanelOwnedChild) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		if err := child.frames.ctx.Err(); err != nil {
			return written, err
		}
		n := min(len(data), nativeOwnedChunk)
		if err := child.bridge.write(nativeAsksFrame{Kind: "owned-output", Body: base64.StdEncoding.EncodeToString(data[:n])}); err != nil {
			return written, err
		}
		written += n
		data = data[n:]
	}
	return written, nil
}

func runNativeOwnedChild(in io.Reader, out io.Writer) error {
	bridge := newNativeAsksBridge(context.Background(), out)
	child := newNativePanelOwnedChild(bridge)
	bridge.owned = child
	defer child.frames.close()
	err := bridge.read(in, func(string) error { return errors.New("owned host retains its one document") })
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
