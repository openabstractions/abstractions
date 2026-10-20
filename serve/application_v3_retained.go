package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

type retainedDraftMember struct{ oldPath, path, old, current string }

func retainedManifestMembers(prefix string) []retainedDraftMember {
	return []retainedDraftMember{
		{prefix, prefix, "profiles", "choosers"},
		{prefix + "/needs/models/[]", prefix + "/needs/models/[]", "profile", "modality"},
		{prefix + "/profiles/entries/[]", prefix + "/choosers/entries/[]", "profile", "modality"},
		{prefix + "/host", prefix + "/host", "readiness_timeout_ms", "readiness_deadline_ms"},
	}
}

func retainedProgramMembers() []retainedDraftMember {
	rules := []retainedDraftMember{
		{"/registration", "/registration", "generation", "epoch"},
		{"/registration/spec", "/registration/spec", "hosts", "servers"},
		{"/legacy", "/legacy", "Generation", "Epoch"},
		{"/legacy/Spec", "/legacy/Spec", "Hosts", "Servers"},
	}
	return append(rules, retainedManifestMembers("/registration/spec/applications/[]/manifest")...)
}

// Rewrite only private retained schema member tokens, preserving opaque JSON,
// values, spacing and canonical field order. Current generated codecs validate
// the unchanged values and bounds. Public incoming codecs have no aliases.
func retainedDraftMembers(raw []byte, rules []retainedDraftMember, prior bool, selected map[int]bool) ([]byte, map[int]bool, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	type replacement struct {
		start, end int
		value      string
	}
	var edits []replacement
	used := map[int]bool{}
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if depth > 1000 {
			return errors.New("depth")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				before := int(d.InputOffset())
				token, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errors.New("duplicate member")
				}
				seen[key] = true
				end := int(d.InputOffset())
				start := before + bytes.IndexByte(raw[before:end], '"')
				next := key
				for i, rule := range rules {
					if selected != nil && !selected[i] {
						continue
					}
					if prior && path == rule.oldPath && key == rule.old {
						next = rule.current
						used[i] = true
					}
					if !prior && path == rule.path && key == rule.current {
						next = rule.old
						used[i] = true
					}
				}
				if next != key {
					edits = append(edits, replacement{start, end, `"` + next + `"`})
				}
				if err := walk(path+"/"+key, depth+1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			for _, rule := range rules {
				if (path == rule.path || path == rule.oldPath) && seen[rule.old] && seen[rule.current] {
					return errors.New("renamed member collision")
				}
			}
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(path+"/[]", depth+1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return nil, nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, nil, errors.New("trailing retained input")
	}
	if len(edits) == 0 {
		return raw, used, nil
	}
	var out []byte
	last := 0
	for _, edit := range edits {
		out = append(out, raw[last:edit.start]...)
		out = append(out, edit.value...)
		last = edit.end
	}
	out = append(out, raw[last:]...)
	return out, used, nil
}

func applicationV3RenameMembers(raw []byte, prior bool) ([]byte, bool) {
	out, used, err := retainedDraftMembers(raw, retainedManifestMembers(""), prior, nil)
	if err != nil {
		return raw, false
	}
	return out, len(used) != 0
}

// Retained display reads preserve original signed bytes and historical digest.
// Native source admission continues to use the current closed codec.
func applicationV3RetainedManifest(raw []byte) ([]byte, bool) {
	return applicationV3RenameMembers(raw, true)
}

func applicationV3PriorCanonical(canonical []byte, original ...[]byte) []byte {
	rules := retainedManifestMembers("")
	var selected map[int]bool
	if len(original) != 0 {
		_, used, err := retainedDraftMembers(original[0], rules, true, nil)
		if err != nil {
			return nil
		}
		selected = used
	}
	prior, _, err := retainedDraftMembers(canonical, rules, false, selected)
	if err != nil {
		return nil
	}
	return prior
}

func registrationRetainedProgramPayload(raw []byte) ([]byte, error) {
	out, _, err := retainedDraftMembers(raw, retainedProgramMembers(), true, nil)
	return out, err
}
