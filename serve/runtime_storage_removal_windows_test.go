package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-download/go/archive"
	"github.com/openabstractions/abstraction-facade/go/grants"
	facadehost "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	storage "github.com/openabstractions/abstraction-storage/go"
	api "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

type inlineRemovalSource struct{ fixtureInventory }

func TestConfiguredStorageHoldsKeepsDefaultAndExplicitBlock(t *testing.T) {
	root := t.TempDir()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rightspolicy.LoadDecisionPolicyWithDefinitions(filepath.Join(root, "rights.json"), rwire.ResourceActions, definitions)
	if err != nil {
		t.Fatal(err)
	}
	rights := &runtimeRights{owner: account.Uid, policy: policy}
	store, err := storage.NewContentStore(runtimeContentStore, filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	const digest = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	ref, err := store.Place(digest, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(ref), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ref); err != nil {
		t.Fatal(err)
	}
	book, err := asks.LoadApplicationBook(filepath.Join(root, "asks.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the Holds receiver is exercised. It does not consume lease or provider operations.
	runtime := &runtimeInference{content: &runtimeContent{store: store, rights: rights},
		resources: &runtimeResources{book: &resourceservice.Book{}}, providers: &runtimeProviders{}}
	var options facadehost.Options
	runtime.configureStorageRemoval(&options, &runtimeAsks{book: book})
	if options.StorageRemoval == nil {
		t.Fatal("Windows composition did not expose Holds")
	}
	for _, action := range options.RightsActions {
		if err := policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := listen.Endpoint(fmt.Sprintf("holds-catalogue-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	service, err := storageservice.Listen(endpoint, store, options.StorageRemoval.Holds)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.EnableRemoval(*options.StorageRemoval); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx) }()
	defer func() {
		cancel()
		_ = service.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := api.NewHoldsClient(listen.FrameClient{Endpoint: endpoint}.WithContext(ctx))
	hold, err := client.Hold("default-self-hold", digest, "fixture", api.LifetimeUntilReleased, 0)
	if err != nil || hold.Outcome != api.HoldOutcomeHeld {
		t.Fatalf("default own hold: %+v, %v", hold, err)
	}
	subject := rwire.Subject{Account: account.Uid, Program: program}
	if err := policy.Set(subject, contentHoldsAction, storageservice.HoldsSelf, false); err != nil {
		t.Fatal(err)
	}
	hold, err = client.Hold("blocked-self-hold", digest, "fixture", api.LifetimeUntilReleased, 0)
	if err != nil || hold.Outcome != api.HoldOutcomeForbidden {
		t.Fatalf("explicitly blocked own hold: %+v, %v", hold, err)
	}
}

func (f inlineRemovalSource) Verify(string) (api.SourcePage, error) {
	page := unavailableSourcePage()
	page.Outcome, page.Complete = api.SourcePageOutcomePage, true
	page.Stores = []api.Store{{Name: "ollama", Program: "ollama", Locator: "ollama", Present: true, Placement: api.PlacementLocal, Errors: []api.StoreError{}}}
	return page, nil
}

func TestStorageInlineRemovalUsesOneAnswerForTheHeldFile(t *testing.T) {
	ctx := t.Context()
	folder := t.TempDir()
	store, err := storage.NewContentStore(runtimeContentStore, filepath.Join(folder, "store"))
	if err != nil {
		t.Fatal(err)
	}
	const digest = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	keep := func() string {
		t.Helper()
		ref, err := store.Place(digest, 3)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.Path(ref), []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := store.Commit(ref); err != nil {
			t.Fatal(err)
		}
		ref, ok := store.Find(digest)
		if !ok {
			t.Fatal("committed file not found")
		}
		return store.Path(ref)
	}
	path := keep()
	bookPath := filepath.Join(folder, "asks.json")
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(folder, "rights.json")
	policy, err := rightspolicy.LoadDecisionPolicy(policyPath, []string{contentWriteAction, contentRemoveAction})
	if err != nil {
		t.Fatal(err)
	}
	panel, err := openPanelImage(ctx, fileid.Self, program, fileid.RefuseLinks)
	if err != nil {
		t.Fatal(err)
	}
	rights := &runtimeRights{owner: account.Uid, operators: []string{program}, panels: panelRoster{images: []fileid.Handle{panel}, imagePaths: []string{program}}, policy: policy,
		path: policyPath, marker: filepath.Join(folder, "defaults-applied")}
	t.Cleanup(rights.close)
	if err := rights.installStorageRemovalRules(facadehost.Options{StorageRemoval: &storageservice.RemovalOptions{}}); err != nil {
		t.Fatal(err)
	}
	content := &runtimeContent{store: store, rights: rights, inputs: filepath.Join(folder, "inputs"),
		checkArchiveRestoreSpace: func(string, int64) error { return nil }}
	subject := rwire.Subject{Account: account.Uid, Program: identity.NormalizeSubjectProgram(program)}
	archive.UseState(filepath.Join(folder, "archive-state"))
	t.Cleanup(func() { archive.UseState("") })
	archiveRoot := filepath.Join(folder, "archive")
	entry := filepath.Join(archiveRoot, "sha256", strings.TrimPrefix(digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := archive.SetFolder(ctx, archiveRoot); err != nil {
		t.Fatal(err)
	}
	admit := func(_ context.Context, peer *identity.Peer, _ string) error {
		s, err := rightsclient.SubjectFromPeer(peer)
		if err != nil || s.Account != rights.owner || !rights.isPanelPeer(ctx, peer) {
			return errors.New("only the fixture Panel")
		}
		return nil
	}
	var lastAnswer asks.InlineStorageRemoval
	var failSettlement bool
	var enableRestoreWorker bool
	var interruptAfterPlacement bool
	placed := make(chan struct{})
	run := func(f func(api.ContentRemover, api.InventorySource, listen.FrameClient)) {
		t.Helper()
		book, err := asks.LoadApplicationBook(bookPath)
		if err != nil {
			t.Fatal(err)
		}
		questions := &runtimeAsks{book: book}
		content.recoverStorageRemovals(ctx, questions)
		endpoint := listen.Endpoint(fmt.Sprintf("inline-storage-%d-%d", os.Getpid(), endpointSeq.Add(1)))
		host, err := storageservice.Listen(endpoint, store, admit)
		if err != nil {
			t.Fatal(err)
		}
		defer host.Close()
		write := func(ctx context.Context, peer *identity.Peer, digest string) error {
			s, err := rightsclient.SubjectFromPeer(peer)
			if err != nil {
				return err
			}
			call, _ := storageservice.WriteCallFrom(ctx)
			return content.authorizeWrite(ctx, s, digest, call)
		}
		if err := host.EnableWriter(write, 1, casapi.BoundedFileStore{MaxBytes: storageservice.MaxRecordFileBytes},
			filepath.Join(folder, "writers.json")); err != nil {
			t.Fatal(err)
		}
		opts := storageservice.RemovalOptions{
			Gate: rights.storageRemovalGate, Holds: admit, Grace: 100 * time.Millisecond,
			Sources: func(context.Context) []storageservice.DesignatedSource {
				return []storageservice.DesignatedSource{{Program: "inventoryd", Stores: []string{"ollama"}, Reader: inlineRemovalSource{}}}
			},
			InlineAnswer: func(ctx context.Context, peer *identity.Peer, store, digest, observation, occurrence string, size int64) error {
				err := questions.answerStorageRemoval(ctx, rights, peer, store, digest, observation, occurrence, size)
				if err == nil {
					s, _ := rightsclient.SubjectFromPeer(peer)
					lastAnswer = asks.InlineStorageRemoval{Account: s.Account, Program: s.Program, Store: store,
						Digest: digest, Observation: observation, Occurrence: occurrence, Size: size}
				}
				return err
			},
			CompleteInline: func(ctx context.Context, occurrence string) error {
				if failSettlement {
					return errors.New("simulated interruption before Activity settlement")
				}
				return book.CompleteInlineStorageRemoval(ctx, occurrence)
			},
			ArchiveRestore: func(ctx context.Context, peer *identity.Peer, actor storageservice.ArchiveActor, selection string) storageservice.ArchiveRestoreReply {
				return content.restoreRemovedArchive(ctx, questions, peer, actor, selection)
			},
			WithoutLease: func(_ context.Context, _ string, effect func() error) error { return effect() },
			Records:      casapi.BoundedFileStore{MaxBytes: storageservice.MaxRecordFileBytes}, RecordPath: filepath.Join(folder, "holds.json"),
		}
		if enableRestoreWorker {
			opts.ArchiveRestoreWork = func(ctx context.Context, place storageservice.ArchivePlacement, release storageservice.ArchiveRelease) {
				if interruptAfterPlacement {
					active, cancel := context.WithCancel(ctx)
					defer cancel()
					content.archiveRestoreWorker(active, questions, func(ctx context.Context, actor storageservice.ArchiveActor, id, digest string, size int64, body io.Reader, check func(context.Context, storageservice.WriteCall) error) error {
						err := place(ctx, actor, id, digest, size, body, check)
						if err == nil {
							cancel()
							close(placed)
						}
						return err
					}, release)
					return
				}
				content.archiveRestoreWorker(ctx, questions, place, release)
			}
		}
		err = host.EnableRemoval(opts)
		if err != nil {
			t.Fatal(err)
		}
		active, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- host.Serve(active) }()
		defer func() {
			cancel()
			_ = host.Close()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		transport := listen.FrameClient{Endpoint: endpoint}
		bound := transport.WithContext(ctx)
		f(api.NewContentRemoverClient(bound), api.NewInventorySourceClient(bound), transport)
		rows, _, err := book.ApplicationHistory()
		if err != nil || len(rows) != 0 {
			t.Fatalf("inline answer leaked to generic Asks: %v %v", rows, err)
		}
	}
	verify := func(inventory api.InventorySource) api.SourcePage {
		t.Helper()
		_, err := inventory.Verify(digest)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(110 * time.Millisecond)
		page, err := inventory.Verify(digest)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	target := func(inventory api.InventorySource) string {
		t.Helper()
		page := verify(inventory)
		if !page.Complete || len(page.Objects) != 1 {
			t.Fatalf("no verified removal ticket: %+v", page)
		}
		return page.Objects[0].Object.Locator
	}
	expect := func(remover api.ContentRemover, value string, want api.RemoveOutcome) {
		t.Helper()
		got, err := remover.Remove(value)
		if err != nil || got.Outcome != want {
			t.Fatalf("removal: %+v %v, want %v", got, err, want)
		}
	}
	var earlier string
	run(func(remove api.ContentRemover, inventory api.InventorySource, _ listen.FrameClient) {
		expect(remove, digest, api.RemoveOutcomeForbidden)
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
		page := verify(inventory)
		if len(page.Stores) != 1 || page.Stores[0].Rule != storageservice.RemovalNeedsAdministrator || len(page.Objects) != 0 {
			t.Fatalf("read-only file is not actionable: %+v", page)
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		earlier = target(inventory)
		if err := os.Rename(path, filepath.Join(folder, "original")); err != nil {
			t.Fatal(err)
		}
		path = keep()
		expect(remove, earlier, api.RemoveOutcomeForbidden)
		earlier = target(inventory)
	})
	failSettlement = true
	run(func(remove api.ContentRemover, inventory api.InventorySource, _ listen.FrameClient) {
		expect(remove, earlier, api.RemoveOutcomeForbidden)
		ticket := target(inventory)
		bits := strings.Split(ticket, ".")
		expect(remove, strings.Replace(ticket, bits[2], strings.Repeat("a", 32), 1), api.RemoveOutcomeForbidden)
		expect(remove, ticket, api.RemoveOutcomeUnavailable)
		expect(remove, ticket, api.RemoveOutcomeNotFound)
		book, err := asks.LoadApplicationBook(bookPath)
		if err != nil {
			t.Fatal(err)
		}
		pending := lastAnswer
		pending.Occurrence = strings.Repeat("b", 32)
		if err := book.RecordInlineStorageRemoval(ctx, pending); err != nil {
			t.Fatal(err)
		}
		if rows, err := book.InlineStorageRemoved(ctx); err != nil || len(rows) != 0 {
			t.Fatalf("unsettled answer appeared in Activity: %+v %v", rows, err)
		}
	})
	failSettlement = false
	const ordinaryDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var acceptedSelection string
	run(func(_ api.ContentRemover, _ api.InventorySource, transport listen.FrameClient) {
		activity := func(selection string, outcome string) storageservice.ArchiveRestoreReply {
			t.Helper()
			reply, err := storageservice.ArchiveRestore(ctx, transport, selection)
			if err != nil || reply.Outcome != outcome {
				t.Fatalf("archive %q: %+v %v", outcome, reply, err)
			}
			return reply
		}
		writer := api.NewContentWriterClient(transport.WithContext(ctx))
		forbidden, err := writer.Begin("normal-writer-request", digest, 3)
		if err != nil || forbidden.Outcome != api.BeginOutcomeForbidden {
			t.Fatalf("standard installation unexpectedly granted generic write: %+v %v", forbidden, err)
		}
		if err := policy.Set(subject, contentWriteAction, ordinaryDigest, true); err != nil {
			t.Fatal(err)
		}
		tooLarge, err := writer.Begin("normal-writer-request", ordinaryDigest, 3)
		if err != nil || tooLarge.Outcome != api.BeginOutcomeTooLarge {
			t.Fatalf("ordinary writer cap changed: %+v %v", tooLarge, err)
		}
		proven := activity("", "page")
		if len(proven.Entries) != 1 || !proven.Entries[0].Available {
			t.Fatalf("archive not offered: %+v", proven)
		}
		selected := proven.Entries[0].Occurrence
		if err := os.Rename(entry, filepath.Join(archiveRoot, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		activity(selected, "unavailable")
		selection := activity("", "page").Entries[0].Occurrence
		if selection == "" || selection == selected {
			t.Fatal("replacement reused the original archive proof")
		}
		const concurrentRestores = 8
		attempts := make(chan string, concurrentRestores)
		startRestores := make(chan struct{})
		for range concurrentRestores {
			go func() {
				<-startRestores
				reply, err := storageservice.ArchiveRestore(ctx, transport, selection)
				if err != nil {
					attempts <- err.Error()
					return
				}
				attempts <- reply.Outcome
			}()
		}
		close(startRestores)
		for range concurrentRestores {
			if outcome := <-attempts; outcome != "queued" {
				t.Errorf("concurrent restore: %s", outcome)
			}
		}
		acceptedSelection = selection
		occurrence, _, _ := strings.Cut(selection, ".")
		activity(occurrence+"."+strings.Repeat("0", 64), "refused")
		queued := activity("", "page")
		if queued.Entries[0].Status != "restoring" || queued.Entries[0].Available {
			t.Fatalf("durable submission was not visible: %+v", queued)
		}
	})
	enableRestoreWorker = true
	if err := policy.Set(subject, contentRemoveAction, ResourceStore(runtimeContentStore), false); err != nil {
		t.Fatal(err)
	}
	run(func(_ api.ContentRemover, _ api.InventorySource, transport listen.FrameClient) {
		for until := time.Now().Add(6 * time.Second); time.Now().Before(until); {
			page, err := storageservice.ArchiveRestore(ctx, transport, "")
			if err == nil && len(page.Entries) == 1 && page.Entries[0].Status == "unavailable" {
				if page.Entries[0].Available {
					t.Fatal("a refused occurrence gained another restore action")
				}
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		reply, err := storageservice.ArchiveRestore(ctx, transport, acceptedSelection)
		if err != nil || reply.Outcome != "unavailable" {
			t.Fatalf("revoked placement was not durably refused: %+v %v", reply, err)
		}
		if _, present := store.Find(digest); present {
			t.Fatal("revoked placement published content")
		}
	})
	enableRestoreWorker = false
	if err := policy.Set(subject, contentRemoveAction, ResourceStore(runtimeContentStore), true); err != nil {
		t.Fatal(err)
	}
	_ = keep()
	run(func(remover api.ContentRemover, inventory api.InventorySource, transport listen.FrameClient) {
		expect(remover, target(inventory), api.RemoveOutcomeRemoved)
		page, err := storageservice.ArchiveRestore(ctx, transport, "")
		if err != nil || len(page.Entries) != 1 || !page.Entries[0].Available || page.Entries[0].Occurrence == acceptedSelection {
			t.Fatalf("a fresh exact removal did not offer its own restore: %+v %v", page, err)
		}
		acceptedSelection = page.Entries[0].Occurrence
		reply, err := storageservice.ArchiveRestore(ctx, transport, acceptedSelection)
		if err != nil || reply.Outcome != "queued" {
			t.Fatalf("the fresh Restore click was refused: %+v %v", reply, err)
		}
	})
	enableRestoreWorker = true
	interruptAfterPlacement = true
	run(func(_ api.ContentRemover, _ api.InventorySource, transport listen.FrameClient) {
		select {
		case <-placed:
		case <-time.After(6 * time.Second):
			t.Fatal("managed restore never reached native placement")
		}
		page, err := storageservice.ArchiveRestore(ctx, transport, "")
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Status != "restoring" {
			t.Fatalf("A committed but unsettled job lost its pending status: %+v %v", page, err)
		}
	})
	interruptAfterPlacement = false
	if err := policy.Set(subject, contentWriteAction, digest, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	run(func(_ api.ContentRemover, _ api.InventorySource, transport listen.FrameClient) {
		var page storageservice.ArchiveRestoreReply
		for until := time.Now().Add(6 * time.Second); time.Now().Before(until); {
			page, err = storageservice.ArchiveRestore(ctx, transport, "")
			if err == nil && len(page.Entries) == 1 && page.Entries[0].Status == "restored" {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Status != "restored" {
			t.Fatalf("exact native receipt did not finish the already published job after restart: %+v %v", page, err)
		}
		replay, err := storageservice.ArchiveRestore(ctx, transport, acceptedSelection)
		if err != nil || replay.Outcome != "restored" {
			t.Fatalf("exact replay did not read the durable receipt: %+v %v", replay, err)
		}
		occurrence, _, _ := strings.Cut(acceptedSelection, ".")
		forged, err := storageservice.ArchiveRestore(ctx, transport, occurrence+"."+strings.Repeat("0", 64))
		if err != nil || forged.Outcome != "refused" {
			t.Fatalf("rebound replay received receipt: %+v %v", forged, err)
		}
		ref, ok := store.Find(digest)
		if !ok {
			t.Fatal("archive did not publish to Storage")
		}
		raw, err := os.ReadFile(store.Path(ref))
		if err != nil || string(raw) != "abc" {
			t.Fatalf("native commit bytes: %q %v", raw, err)
		}
	})
	book, err := asks.LoadApplicationBook(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := book.CheckInlineStorageRemoval(ctx, lastAnswer); err != nil {
		t.Fatal(err)
	}
	if err := book.RecordInlineStorageRemoval(ctx, lastAnswer); !errors.Is(err, asks.ErrInlineStorageRemovalUsed) {
		t.Fatalf("answered occurrence replay after restart: %v", err)
	}
	lastAnswer.Observation = strings.Repeat("a", 64)
	if err := book.CheckInlineStorageRemoval(ctx, lastAnswer); err == nil {
		t.Fatal("retained answer authorized a different file")
	}
}
