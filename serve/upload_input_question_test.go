package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// TestUploadHeldForItsInputQuestionThenSucceeds holds RIGHTS-FLOW ruling 2
// and INF-W10 for the window's own upload store: a fresh program's image
// edit and transcription, refused at the store for want of input:image or
// input:audio, are held on the paired input question the store raised; the
// person's Allow lets the identical call store its upload, and the call
// completes with every later question answered within the same hold.
func TestUploadHeldForItsInputQuestionThenSucceeds(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	if err := r.install(slices.Concat(firstUseActions, []string{contentReadAction, contentWriteAction}), nil); err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse, r.pendingQuestions, r.pairedInput, r.awaitQuestions = a.admitFirstUse, a.pendingQuestions, a.admitPairedInput, a.awaitQuestions
	content, err := composeContent(runtimeFlags{endpoint: "upload-question-" + filepath.Base(state)}, state, r)
	if err != nil {
		t.Fatal(err)
	}
	panel := rwire.Subject{Account: r.owner, Program: r.operators[len(r.operators)-1]}

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.Copy(io.Discard, io.LimitReader(req.Body, 40<<20))
		switch {
		case strings.HasSuffix(req.URL.Path, "/models"):
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-image-1"},{"id":"whisper-1"}]}`)
		case strings.HasSuffix(req.URL.Path, "/images/edits"):
			fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{}}`, base64.StdEncoding.EncodeToString(picture.Bytes()))
		case strings.HasSuffix(req.URL.Path, "/audio/transcriptions"):
			fmt.Fprint(w, `{"language":"en","duration":1.2,"text":"hello upload","segments":[{"id":0,"start":0,"end":1.2,"text":"hello upload"}]}`)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	routes := router.New(router.NewHosted("openai", upstream.URL, router.WireOpenAICompatible, "openai"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	// The credential's use is permitted here; complete, the input and the
	// output profiles are asked through the runtime's decision point.
	provider, err := inference.New(inference.Config{Router: routes, SurveyAge: time.Hour, Decide: r.decideInference, AskInput: r.askInput,
		ResolveContent: content.read, PrepareContentWrite: content.prepareWrite,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return map[string]string{"Authorization": "Bearer fixture"}, "applied"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })

	// The person answers Allow to each question a call is held on.
	var mu sync.Mutex
	var current inference.Subject
	var held [][]string
	var decline bool
	window, err := gateway.Listen("127.0.0.1:0", gateway.Config{Chat: provider, Account: r.owner, StoreContent: content.write, ReadContent: content.read,
		Keys: func(context.Context, inference.Subject, string) (gateway.Grant, string) {
			return gateway.Grant{Credential: "openai"}, gateway.KeyVerified
		},
		Bind: func(net.Conn, time.Time) (gateway.Bound, error) {
			mu.Lock()
			defer mu.Unlock()
			return foundingBound{current}, nil
		},
		QuestionWait: 10 * time.Second,
		Await: func(ctx context.Context, s inference.Subject, ids []string) bool {
			mu.Lock()
			held = append(held, slices.Clone(ids))
			unanswered := decline
			mu.Unlock()
			if unanswered {
				return false
			}
			// Every question of the round is answered before the hold
			// observes them, so each retry meets only questions not yet
			// asked.
			for _, id := range ids {
				if _, _, err := book.AnswerApplication(id, "allow"); err != nil {
					t.Error(err)
					return false
				}
				snapshot, err := r.policy.OperatorSnapshot()
				if err != nil {
					t.Error(err)
					return false
				}
				if edit, err := a.applyOperatorAllow(context.Background(), panel, snapshot.Revision, id, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Errorf("the Allow of %s: %+v %v", id, edit, err)
				}
			}
			return r.awaitQuestions(ctx, rwire.Subject{Account: s.Account, Program: s.Program}, ids)
		},
		Pending: func(_ context.Context, s inference.Subject, ids []string) []string {
			pending, _ := r.pendingQuestions(rwire.Subject{Account: s.Account, Program: s.Program}, ids)
			return pending
		}})
	if err != nil {
		t.Fatal(err)
	}
	go window.Serve(context.Background())
	t.Cleanup(func() { window.Close() })

	audio := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	for _, row := range []struct {
		name, path, file, filename, input, want string
		data                                    []byte
		fields                                  map[string]string
	}{
		{"image edit", "/images/edits", "image", "frog.png", inference.InputImage, "b64_json", picture.Bytes(), map[string]string{"model": "gpt-image-1", "prompt": "a hat", "size": "1024x1024"}},
		{"transcription", "/audio/transcriptions", "file", "hello.wav", inference.InputAudio, "hello upload", audio, map[string]string{"model": "whisper-1"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "fresh-agent.exe")
			mu.Lock()
			current, held = inference.Subject{Account: r.owner, Program: program}, nil
			mu.Unlock()
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, _ := mw.CreateFormFile(row.file, row.filename)
			part.Write(row.data)
			for key, value := range row.fields {
				mw.WriteField(key, value)
			}
			mw.Close()
			req, _ := http.NewRequest(http.MethodPost, "http://"+window.Addr().String()+"/v1"+row.path, &body)
			req.Header.Set("Authorization", "Bearer placeholder")
			req.Header.Set("Content-Type", mw.FormDataContentType())
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), row.want) {
				t.Fatalf("%s: %d %s", row.path, resp.StatusCode, raw)
			}
			mu.Lock()
			rounds := slices.Clone(held)
			mu.Unlock()
			if len(rounds) == 0 || len(rounds[0]) != 1 {
				t.Fatalf("held on %v, want the input question first", rounds)
			}
			records, _, err := book.ApplicationHistory()
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range records {
				if q.ID != rounds[0][0] {
					continue
				}
				permission, ok := asks.PermissionForRecord(r.owner, q)
				if !ok || !samePrograms(permission.Targets[0].Program, program) || permission.Targets[0].Resource != inference.InputResource(row.input) {
					t.Fatalf("the first hold was on %+v, want the %s question about %s", permission, inference.InputResource(row.input), program)
				}
				return
			}
			t.Fatalf("question %s is not in the book", rounds[0][0])
		})
	}

	// Unanswered: the hold ends without an Allow, the call is refused and
	// the store keeps neither the upload's bytes nor an input record.
	t.Run("unanswered", func(t *testing.T) {
		var other bytes.Buffer
		if err := png.Encode(&other, image.NewRGBA(image.Rect(0, 0, 65, 65))); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		current, held, decline = inference.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "fresh-agent.exe")}, nil, true
		mu.Unlock()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, _ := mw.CreateFormFile("image", "other.png")
		part.Write(other.Bytes())
		mw.WriteField("model", "gpt-image-1")
		mw.WriteField("prompt", "a hat")
		mw.Close()
		req, _ := http.NewRequest(http.MethodPost, "http://"+window.Addr().String()+"/v1/images/edits", &body)
		req.Header.Set("Authorization", "Bearer placeholder")
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("unanswered upload succeeded: %s", raw)
		}
		mu.Lock()
		rounds := len(held)
		mu.Unlock()
		if rounds == 0 {
			t.Fatal("the unanswered upload was never held on its question")
		}
		digest := digestOf(other.Bytes())
		if _, ok := content.store.Find(digest); ok {
			t.Fatal("the refused upload's bytes stayed in the store")
		}
		if _, err := os.Stat(content.inputPath(digest)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the refused upload left an input record: %v", err)
		}
	})
}
