package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

const claudeWatchRule = "mcp__OpenAbstractions__oa_messages_watch"
const claudeAsksWatchRule = "mcp__OpenAbstractions__oa_asks_watch"

// Claude evaluates deny, ask, then allow. Its documented MCP forms include a
// bare server name, an exact tool name, and tool-name globs. This check only
// decides whether adding our exact allow would conflict; host rules stay intact.
// https://code.claude.com/docs/en/permissions#tool-name-wildcards
func claudeWatchRestricted(rule string) bool {
	return claudeWatchRestrictedFor(rule, claudeWatchRule)
}
func claudeWatchRestrictedFor(rule, target string) bool {
	tool := strings.TrimSpace(strings.SplitN(rule, "(", 2)[0])
	if tool == target || tool == "mcp__OpenAbstractions" {
		return true
	}
	if !strings.ContainsAny(tool, "*?[]\\") {
		return false
	}
	// Only '*' has documented examples for MCP matching. Preserve ambiguous
	// MCP/global glob forms for host review rather than guessing their grammar.
	if strings.ContainsAny(tool, "?[]\\") {
		prefix := strings.FieldsFunc(tool, func(r rune) bool { return strings.ContainsRune("*?[]\\", r) })
		return strings.HasPrefix(tool, "mcp__") || strings.HasPrefix(tool, "*") || len(prefix) == 0 || strings.HasPrefix(target, prefix[0])
	}
	matched, err := path.Match(tool, target)
	return err != nil || matched
}

// This private receipt contains hashes and the extent of our insertion, never
// a copy of the person's settings. Pending receipts make interrupted writes
// recoverable without claiming ownership of foreign settings.
type claudeWatchReceipt = cacheproducer.WatchReceipt

func claudeWatchReceiptFile(state, rule string) string {
	return cacheproducer.WatchReceiptFile(state, rule)
}
func readClaudeWatchReceipt(ctx context.Context, p string) ([]byte, claudeWatchReceipt, error) {
	return cacheproducer.ReadWatchReceipt(ctx, p)
}

// Authenticate an insertion by both endpoint hashes and the existing insertion
// owner. This also accepts Messages receipts written before scopes existed.
func unwindClaudeWatch(raw []byte, r claudeWatchReceipt, rule string) ([]byte, error) {
	if !r.Added || pluginHash(raw) != r.After || r.Offset < 0 || r.Length < 1 || r.Offset+r.Length > len(raw) {
		return nil, errors.New("settings changed; owned undo preserved")
	}
	before := append(append([]byte{}, raw[:r.Offset]...), raw[r.Offset+r.Length:]...)
	if pluginHash(before) != r.Before {
		// A previously absent settings file starts from the canonical empty object.
		if pluginHash(nil) != r.Before || !bytes.Equal(before, []byte("{}\n")) {
			return nil, errors.New("unknown owned insertion; preserved")
		}
	}
	offset, insertion, exists, err := claudeWatchInsertionFor(before, rule)
	if err != nil || exists || offset != r.Offset || !bytes.Equal(insertion, raw[r.Offset:r.Offset+r.Length]) {
		return nil, errors.New("unknown owned insertion; preserved")
	}
	return before, nil
}

// strictClaudeObject uses the existing JSON decoder and duplicate-key owner.
// Offsets come from that decoder; unsupported JSONC is preserved unchanged.
func strictClaudeObject(raw []byte) (map[string]json.RawMessage, map[string][2]int, int, error) {
	fields, err := claudeConfigObject(raw)
	if err != nil {
		return nil, nil, 0, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	_, _ = d.Token()
	spans := map[string][2]int{}
	for d.More() {
		key, _ := d.Token()
		start := int(d.InputOffset())
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, nil, 0, err
		}
		end := int(d.InputOffset())
		start = end - len(value)
		spans[key.(string)] = [2]int{start, end}
	}
	end := int(d.InputOffset())
	// Decoder leaves whitespace before the closing brace. Insert there.
	return fields, spans, end, nil
}

func claudeWatchInsertion(raw []byte) (int, []byte, bool, error) {
	return claudeWatchInsertionFor(raw, claudeWatchRule)
}
func claudeWatchInsertionFor(raw []byte, target string) (int, []byte, bool, error) {
	fields, spans, end, err := strictClaudeObject(raw)
	if err != nil {
		return 0, nil, false, err
	}
	quoted, _ := json.Marshal(target)
	p, ok := fields["permissions"]
	if !ok {
		prefix := ""
		if len(fields) > 0 {
			prefix = ","
		}
		return end, []byte(prefix + `"permissions":{"allow":[` + string(quoted) + `]}`), false, nil
	}
	permissions, ps, pend, err := strictClaudeObject(p)
	if err != nil {
		return 0, nil, false, err
	}
	for _, name := range []string{"deny", "ask"} {
		if v, exists := permissions[name]; exists {
			var rules []string
			if json.Unmarshal(v, &rules) != nil {
				return 0, nil, false, errors.New("unsupported permission rules")
			}
			for _, rule := range rules {
				if claudeWatchRestrictedFor(rule, target) {
					return 0, nil, false, errors.New("matching ask or deny rules require Claude review")
				}
			}
		}
	}
	allow, ok := permissions["allow"]
	if !ok {
		prefix := ""
		if len(permissions) > 0 {
			prefix = ","
		}
		return spans["permissions"][0] + pend, []byte(prefix + `"allow":[` + string(quoted) + `]`), false, nil
	}
	var rules []string
	if json.Unmarshal(allow, &rules) != nil || len(bytes.TrimSpace(allow)) == 0 || bytes.TrimSpace(allow)[0] != '[' {
		return 0, nil, false, errors.New("unsupported allow rules")
	}
	for _, rule := range rules {
		if rule == target {
			return 0, nil, true, nil
		}
	}
	a := ps["allow"]
	close := a[1] - 1
	prefix := ""
	if len(rules) > 0 {
		prefix = ","
	}
	return spans["permissions"][0] + close, []byte(prefix + string(quoted)), false, nil
}

func claudeWatchCommand(args []string, output, diagnostics io.Writer) int {
	if len(args) < 3 || args[1] != "claude" {
		return 2
	}
	action := args[2]
	f := flag.NewFlagSet("watch-permission claude", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	state := f.String("state-dir", "", "absolute private gateway state directory")
	cli := f.String("claude", "", "absolute selected Claude CLI")
	scope := f.String("scope", "messages", "separately reviewed background scope: messages or asks")
	if err := f.Parse(args[3:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || !filepath.IsAbs(*state) || !filepath.IsAbs(*cli) || (*scope != "messages" && *scope != "asks") || (action != "status" && action != "enable" && action != "disable") {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rule := claudeWatchRule
	if *scope == "asks" {
		rule = claudeAsksWatchRule
	}
	status, preserved, err := applyClaudeWatchFor(ctx, action, *state, *cli, "", rule)
	if err != nil {
		checkLine(diagnostics, "Claude background permission: "+err.Error())
		return 1
	}
	if _, err = output.Write(pluginJSON(map[string]any{"outcome": "page", "state": status, "preserved": preserved})); err != nil {
		return 1
	}
	return 0
}

func applyClaudeWatch(ctx context.Context, action, state, cli, root string) (string, bool, error) {
	return applyClaudeWatchFor(ctx, action, state, cli, root, claudeWatchRule)
}
func applyClaudeWatchFor(ctx context.Context, action, state, cli, root, rule string) (string, bool, error) {
	if rule != claudeWatchRule && rule != claudeAsksWatchRule {
		return "unknown", false, errors.New("unsupported background scope")
	}
	if action == "status" {
		return applyClaudeWatchLockedFor(ctx, action, state, cli, root, rule)
	}
	if err := privatedir.Ensure(state); err != nil {
		return "unknown", false, errors.New("private state unavailable")
	}
	var status string
	var preserved bool
	// The canonical CAS owner serializes the complete two-file transaction.
	// Individual settings and receipt replacements retain their own comparisons.
	err := cas.ChangeLimitContext(ctx, filepath.Join(state, "claude-watch-operation"), 16, func(_ []byte) ([]byte, error) {
		var err error
		status, preserved, err = applyClaudeWatchLockedFor(ctx, action, state, cli, root, rule)
		if err != nil {
			return nil, err
		}
		return []byte("1\n"), nil
	})
	return status, preserved, err
}

func applyClaudeWatchLocked(ctx context.Context, action, state, cli, root string) (string, bool, error) {
	return applyClaudeWatchLockedFor(ctx, action, state, cli, root, claudeWatchRule)
}
func applyClaudeWatchLockedFor(ctx context.Context, action, state, cli, root, rule string) (string, bool, error) {
	fail := func(err error) (string, bool, error) { return "unknown", false, err }
	if err := privatedir.Ensure(state); err != nil {
		return fail(errors.New("private state unavailable"))
	}
	receiptPath := claudeWatchReceiptFile(state, rule)
	base, receipt, err := readClaudeWatchReceipt(ctx, receiptPath)
	if err != nil {
		return fail(err)
	}
	// Status deliberately reads only OA's private receipt. It cannot inspect a
	// host profile before the native person decision owned by Panel.
	if action == "status" {
		if base == nil {
			return "unreviewed", false, nil
		}
		if receipt.Status == "pending" || receipt.Status == "undo-pending" {
			return "unknown", false, nil
		}
		return receipt.Status, !receipt.Added, nil
	}
	if root == "" {
		root, err = setupio.ClaudeConfigDir()
		if err != nil {
			return fail(err)
		}
	}
	directory, rootID, err := setupIdentity(ctx, root, true)
	if err != nil {
		return fail(errors.New("Claude settings directory unavailable"))
	}
	defer directory.Close()
	selected, _, err := setupIdentity(ctx, cli, false)
	if err != nil {
		return fail(errors.New("selected CLI unavailable"))
	}
	defer selected.Close()
	// Saved CLI is a locator. Authority comes from the caller-selected, held
	// executable for this operation. A Claude Code update moves the path to a
	// new version folder of the same installation, which keeps ownership.
	cliID := selected.FileID().Path()
	sameCLI := func(saved string) bool { return nativecli.SameClaudeCLIInstall(saved, cliID) }
	stateHandle, stateID, err := setupIdentity(ctx, state, true)
	if err != nil {
		return fail(err)
	}
	defer stateHandle.Close()
	handles := []setupIdentityRef{directory, selected, stateHandle}
	if base != nil && (!sameCLI(receipt.CLI) || receipt.Root != rootID || receipt.State != stateID) {
		return fail(errors.New("ownership identity changed; preserved"))
	}
	path := filepath.Join(directory.FileID().Path(), "settings.json")
	settings, openErr := directory.Beneath("settings.json", fileid.Options{Links: fileid.RefuseLinks})
	if openErr == nil {
		info, statErr := settings.File().Stat()
		if statErr != nil || !info.Mode().IsRegular() || settings.FileID().Links() != 1 {
			settings.Close()
			return fail(errors.New("settings must be a regular unlinked file; preserved"))
		}
		settings.Close()
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return fail(errors.New("settings identity unavailable; preserved"))
	}
	raw, err := cas.ReadLimitContext(ctx, path, claudeProjectLimit)
	if err != nil {
		return fail(errors.New("settings unavailable; preserved"))
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeProjectLimit}
	markerStore := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	save := func(r claudeWatchReceipt) error {
		encoded := pluginJSON(r)
		err := markerStore.WriteContext(ctx, receiptPath, casapi.Value{Data: base}, encoded)
		if err == nil {
			base = encoded
			receipt = r
		}
		return err
	}
	otherRule := claudeWatchRule
	if rule == claudeWatchRule {
		otherRule = claudeAsksWatchRule
	}
	otherPath := claudeWatchReceiptFile(state, otherRule)
	otherBase, other, err := readClaudeWatchReceipt(ctx, otherPath)
	if err != nil {
		return fail(err)
	}
	// Complete a settings CAS whose receipt completion was interrupted. The
	// operation lock excludes another scope mutation during these comparisons.
	finish := func(p string, original []byte, r claudeWatchReceipt, siblingPath string, sibling []byte) error {
		if !setupIdentitiesCurrent(ctx, handles) {
			return errors.New("identity changed; preserved")
		}
		if !sameCLI(r.CLI) || r.Root != rootID || r.State != stateID {
			return errors.New("ownership identity changed; preserved")
		}
		if r.Rebased != nil {
			if !sameCLI(r.Rebased.CLI) || r.Rebased.Root != rootID || r.Rebased.State != stateID || r.Rebased.Status != "owned" || !r.Rebased.Added || r.Rebased.Rebased != nil || r.Rebased.SiblingBefore != "" || r.Rebased.After != r.Undo {
				return errors.New("unknown sibling rebase; preserved")
			}
			encoded := pluginJSON(*r.Rebased)
			if !bytes.Equal(sibling, encoded) {
				if pluginHash(sibling) != r.SiblingBefore {
					return errors.New("sibling ownership changed; preserved")
				}
				if err := markerStore.WriteContext(ctx, siblingPath, casapi.Value{Data: sibling}, encoded); err != nil {
					return err
				}
			}
		}
		r.Status, r.Rebased, r.SiblingBefore = "disabled", nil, ""
		return markerStore.WriteContext(ctx, p, casapi.Value{Data: original}, pluginJSON(r))
	}
	if other.Status == "undo-pending" && other.Rebased != nil {
		if pluginHash(raw) != other.Undo {
			return fail(errors.New("sibling undo pending; retry its disable"))
		}
		if err := finish(otherPath, otherBase, other, receiptPath, base); err != nil {
			return fail(err)
		}
		base, receipt, err = readClaudeWatchReceipt(ctx, receiptPath)
		if err != nil {
			return fail(err)
		}
		otherBase, other, err = readClaudeWatchReceipt(ctx, otherPath)
		if err != nil {
			return fail(err)
		}
	}
	if action == "disable" {
		if base == nil {
			return "disabled", false, save(claudeWatchReceipt{Version: 1, CLI: cliID, Root: rootID, State: stateID, Status: "disabled"})
		}
		if receipt.Status == "disabled" {
			return "disabled", !receipt.Added, nil
		}
		if receipt.Added {
			if receipt.Status == "undo-pending" && receipt.Undo != "" && pluginHash(raw) == receipt.Undo {
				return "disabled", false, finish(receiptPath, base, receipt, otherPath, otherBase)
			}
			if pluginHash(raw) == receipt.Before && receipt.Status == "pending" {
				receipt.Status = "disabled"
				return "disabled", false, save(receipt)
			}
			next, undoErr := unwindClaudeWatch(raw, receipt, rule)
			if undoErr != nil {
				// The only accepted intervening edit is the other scope's exact,
				// authenticated insertion. Unwind both, then rebuild its rule.
				if !sameCLI(other.CLI) || other.Root != rootID || other.State != stateID || (other.Status != "owned" && other.Status != "pending") {
					return fail(undoErr)
				}
				middle, err := unwindClaudeWatch(raw, other, otherRule)
				if err != nil {
					return fail(undoErr)
				}
				initial, err := unwindClaudeWatch(middle, receipt, rule)
				if err != nil {
					return fail(err)
				}
				offset, insertion, exists, err := claudeWatchInsertionFor(initial, otherRule)
				if err != nil || exists {
					return fail(errors.New("sibling insertion unavailable; preserved"))
				}
				next = append(append(append([]byte{}, initial[:offset]...), insertion...), initial[offset:]...)
				rebased := other
				rebased.Before, rebased.After = receipt.Before, pluginHash(next)
				rebased.Offset, rebased.Length, rebased.Status = offset, len(insertion), "owned"
				receipt.Rebased, receipt.SiblingBefore = &rebased, pluginHash(otherBase)
			}
			receipt.Status = "undo-pending"
			receipt.Undo = pluginHash(next)
			if err := save(receipt); err != nil {
				return fail(errors.New("undo intent unavailable; preserved"))
			}
			if !setupIdentitiesCurrent(ctx, handles) {
				return fail(errors.New("identity changed; preserved"))
			}
			if err := store.WriteContext(ctx, path, casapi.Value{Data: raw}, next); err != nil {
				return fail(errors.New("settings changed during undo; preserved"))
			}
			if receipt.Rebased != nil {
				return "disabled", false, finish(receiptPath, base, receipt, otherPath, otherBase)
			}
		}
		receipt.Status = "disabled"
		return "disabled", !receipt.Added, save(receipt)
	}
	if base != nil && receipt.Added && receipt.Status != "disabled" {
		if receipt.Rebased != nil {
			return fail(errors.New("owned undo pending; retry disable"))
		}
		if pluginHash(raw) == receipt.After {
			receipt.Status = "owned"
			return "owned", false, save(receipt)
		}
		if sameCLI(other.CLI) && other.Root == rootID && other.State == stateID && (other.Status == "owned" || other.Status == "pending") {
			if middle, err := unwindClaudeWatch(raw, other, otherRule); err == nil {
				if _, err := unwindClaudeWatch(middle, receipt, rule); err == nil {
					receipt.Status = "owned"
					return "owned", false, save(receipt)
				}
			}
		}
		if receipt.Status != "pending" || pluginHash(raw) != receipt.Before {
			return fail(errors.New("settings changed; ownership preserved"))
		}
	}
	working := raw
	if working == nil {
		working = []byte("{}\n")
	}
	offset, insertion, preexisting, err := claudeWatchInsertionFor(working, rule)
	if err != nil {
		return fail(errors.New("settings unsupported or host restrictions require review; preserved"))
	}
	receipt = claudeWatchReceipt{Version: 1, CLI: cliID, Root: rootID, State: stateID, Before: pluginHash(raw), Offset: offset, Length: len(insertion), Added: !preexisting, Status: "preexisting"}
	if preexisting {
		return "preexisting", true, save(receipt)
	}
	next := append(append(append([]byte{}, working[:offset]...), insertion...), working[offset:]...)
	receipt.After = pluginHash(next)
	receipt.Status = "pending"
	if err := save(receipt); err != nil {
		return fail(errors.New("ownership intent unavailable; preserved"))
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return fail(errors.New("identity changed; preserved"))
	}
	if err := store.WriteContext(ctx, path, casapi.Value{Data: raw}, next); err != nil {
		return fail(errors.New("settings changed during enable; preserved"))
	}
	receipt.Status = "owned"
	if err := save(receipt); err != nil {
		return fail(errors.New("permission saved; ownership completion pending"))
	}
	return "owned", false, nil
}
