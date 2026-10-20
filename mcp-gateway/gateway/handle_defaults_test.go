package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// R35: a client or SDK that fills schema defaults sends them beside a
// handle. Through the picture tool's server, a handle with size auto, empty
// or null fields and wait_ms continues; a field naming a new picture is
// refused by name. Neither generate schema carries a default to fill.
func TestHandleWithFilledDefaultsContinues(t *testing.T) {
	handle := strings.Repeat("a", 32)
	b := newPictureBackend(t)
	_, cs := connected(t, b, "defaults-client", "2026-07-28")
	_, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"handle": handle, "size": "auto", "model": "", "mask": nil, "wait_ms": 10})
	if out.Outcome == "invalid" {
		t.Fatalf("a handle with filled defaults: %+v", out)
	}
	_, out = call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"handle": handle, "size": "512x512"})
	if out.Outcome != "invalid" || out.Field != "size" {
		t.Fatalf("a size beside the handle: %+v", out)
	}

	for _, c := range []struct {
		in    VideoInput
		field string
	}{
		{VideoInput{Handle: handle, Preset: videoQuick, Size: "auto", WaitMS: 10}, ""},
		{VideoInput{Handle: handle, Preset: videoQuality}, "preset"},
		{VideoInput{Handle: handle, Model: "ltx-2.5"}, "model"},
		{VideoInput{Handle: handle, DurationMS: 5000}, "duration_ms"},
	} {
		in, refused := videoRequest(c.in)
		if c.field == "" && (refused != nil || in != (VideoInput{Handle: handle, WaitMS: 10})) || c.field != "" && (refused == nil || refused.Field != c.field) {
			t.Fatalf("%+v: %+v %+v", c.in, in, refused)
		}
	}

	for name, schema := range map[string]any{"image": imageSchema(), "video": videoSchema()} {
		raw, _ := json.Marshal(schema)
		if strings.Contains(string(raw), `"default"`) {
			t.Fatalf("the %s schema carries a default: %s", name, raw)
		}
	}
}
