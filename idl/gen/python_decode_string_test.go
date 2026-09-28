package main

import (
	"os"
	"os/exec"
	"testing"
)

// Exercise the generated reader, including the unescaped fast path and its
// bytewise fallback. Refusal offsets and the position after a string matter to
// field parsing, so these are runtime checks rather than source assertions.
func TestPythonStringDecodeBoundary(t *testing.T) {
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.py", genPy(enumDefinition(t)))
	writeNamespaceFile(t, dir, "check.py", `import rec

for raw, expected in (
    (b'"plain"', 'plain'),
    (b'""', ''),
    ('"é"'.encode('utf-8'), 'é'),
    (b'"a\\"b\\\\c"', 'a"b\\c'),
    (b'"\\uD83D\\uDE00"', '😀'),
):
    reader = rec._Reader(raw)
    assert reader.string() == expected, raw
    assert reader.pos == len(raw), raw

reader = rec._Reader(b'"alpha",tail')
assert reader.string() == 'alpha'
assert reader.pos == len(b'"alpha"')
assert reader.at() == ord(',')

for raw, word, offset in (
    (b'"a\x01b"', 'bad_string', 2),
    (b'"\xff"', 'bad_string', 3),
    (b'"unterminated', 'malformed', len(b'"unterminated')),
    (b'"\\uD800"', 'bad_string', 7),
):
    try:
        rec._Reader(raw).string()
    except rec.Refusal as error:
        assert (error.word, error.offset) == (word, offset), (raw, error.word, error.offset)
    else:
        raise AssertionError(f'accepted {raw!r}')
`)
	cmd := exec.Command(servicePython(t), "-B", "check.py")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Python string boundary: %v\n%s", err, out)
	}
}
