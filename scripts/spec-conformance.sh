#!/usr/bin/env bash
# Do the implementations read a download SPEC the same way?
#
# scripts/conformance.sh already asks this of the job RECORD, and answers it by
# passing one record around and requiring byte-identical re-encoding. The spec
# inside that record had no such test, and could not have one of the same shape:
# the job layer refuses to look inside a spec, which is precisely what lets
# download grow mirrors and chunk manifests without a schema change in three
# languages.
#
# The opacity is right. The hole it leaves is real, and it cost something. One
# implementation wrote a digest bare; another built "sha256:" + hex and compared
# strings. The error said:
#
#     got sha256:1fc70f…  want 1fc70f…
#
# — the same digest, twice — and because a mismatch deletes the partial, a
# correct 1.5 GB download was thrown away and fetched again.
#
# So the record's conformance is by identical BYTES and the spec's is by
# identical MEANING. Each implementation prints what it understood, and they must
# all print the same thing.
#
# What "the same thing" is, exactly — every line, its key, its order, its
# spelling, the channel a refusal goes to — is written down in
# download/README.md § The spec, exactly. This script is that section executed.
# A reader written against that section and against nothing else must pass here;
# if it cannot, the section is wrong or silent, and that is a defect in the
# section rather than in the reader.
#
#   usage: spec-conformance.sh
#     env: SPECREAD_CPP    path to the C++ reader (default $ROOT/.build/Release/specread.exe)
#          SPECREAD_EXTRA  one 'name=command' per line, for a reader from anywhere:
#                            SPECREAD_EXTRA='node=node /path/to/specread.js'
#
# WHAT AN ABSENCE MEANS HERE. Some of the checks below run once per
# implementation, so the total falls by that many every time one is missing — and
# it did, from 61 to 57, because the C++ default named a machine layout that no
# longer exists while a parenthetical line was the whole of the notice. A count
# that can drop without failing teaches everybody to read the word PASS and not
# the number beside it. So the roster is fixed at three, an absent one is named
# loudly at the end and states its own cost in checks, and the total says which
# roster produced it.

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
solo=0
ok()  { echo "  PASS  $1"; pass=$((pass+1)); }
bad() { echo "  FAIL  $1"; fail=$((fail+1)); }

# per is ok/bad for a check that runs once PER IMPLEMENTATION. Counting them is
# what lets an absence below state its own cost in checks rather than leaving a
# reader to notice that a number moved.
per()    { ok "$1";  solo=$((solo+1)); }
perbad() { bad "$1"; solo=$((solo+1)); }

ROSTER="go python cpp"
ABSENT=""
absent() { ABSENT="$ABSENT
    $1"; }

echo "download spec conformance"

# --- the implementations, one line each ------------------------------------
#
# Every implementation is reached through a shim, exactly as scripts/
# conformance.sh reaches the job implementations, so this harness never has to
# know whether one is a binary, an interpreter plus a script, or something from
# outside this repository. Adding a language is adding a line. It also sidesteps
# the fact that this repository's path contains a space, which quietly breaks any
# scheme that keeps a command in a string and expands it unquoted.
mkdir -p "$WORK/bin"
IMPLS=()
impl() { # impl <name> <command...>
    local name="$1"; shift
    { printf '#!/usr/bin/env bash\nexec'
      for a in "$@"; do printf ' %q' "$a"; done
      printf ' "$@"\n'
    } > "$WORK/bin/$name"
    chmod +x "$WORK/bin/$name"
    IMPLS+=("$name")
}
read_spec() { local name="$1"; shift; "$WORK/bin/$name" "$@"; }

# What a reader said when it failed. A refusal printed on stdout is exactly the
# mistake this harness now names, so quoting stderr alone would report it as
# silence and send the author looking in the wrong place.
said() { # said <stderr file> [stdout file]
    local s
    s="$(head -1 "$1" 2>/dev/null)"
    [ -n "$s" ] || s="$(head -1 "${2:-/dev/null}" 2>/dev/null)"
    echo "${s:-said nothing}"
}

(cd "$ROOT/download/go" && go build -o "$WORK/specread-go.exe" ./cmd/specread) \
  || { echo "cannot build the Go reader"; exit 1; }
impl go "$WORK/specread-go.exe"

PY=""
for cand in "py -3" python3 python; do
    if $cand -c "pass" >/dev/null 2>&1; then PY="$cand"; break; fi
done
if [ -n "$PY" ]; then
    export PYTHONPATH="$ROOT/job/python"
    # $PY may itself be two words ("py -3"), which is why it goes through a shim
    # rather than into a variable that something later expands.
    impl python $PY "$ROOT/download/python/specread.py"
else
    absent "python: no working interpreter on this machine"
fi

WANTED="${SPECREAD_CPP:-$ROOT/.build/Release/specread.exe}"
CPP="$WANTED"
[ -x "$CPP" ] || CPP="$ROOT/.build/specread"
if [ -x "$CPP" ]; then
    impl cpp "$CPP"
else
    absent "cpp: no reader at $WANTED — set SPECREAD_CPP, or cmake --build to make one"
fi

while IFS= read -r line; do
    case "$line" in ""|\#*) continue ;; esac
    eval "cmd=( ${line#*=} )"
    impl "${line%%=*}" "${cmd[@]}"
done <<< "${SPECREAD_EXTRA:-}"

echo "  implementations: ${IMPLS[*]}"
[ "${#IMPLS[@]}" -ge 2 ] || { echo "need at least two implementations to prove anything"; exit 1; }

# --- the shape of the output, before anything is compared ------------------
#
# Two implementations that disagree about every line produce a diff nobody can
# read, and the first thing a fourth reader gets wrong is the shape: a key it
# renamed, a line it put somewhere else, a CRLF its language writes by default.
# Naming those separately is the difference between "your output is wrong" and a
# hundred-line diff that says the same thing without saying which part.
FIXED="digest size final partial final_refusal partial_refusal"
SHAPE="$FIXED source0 source0.attrs source0.headers"
for name in "${IMPLS[@]}"; do
    out="$WORK/shape.$name"
    read_spec "$name" "$ROOT/download/testdata/specs/canonical.json" >"$out" 2>"$WORK/shape.$name.err"
    status=$?
    if [ "$status" != 0 ]; then
        perbad "$name: exits $status on a well-formed spec — a refusal is a value on stdout, not an exit code ($(said "$WORK/shape.$name.err" "$out"))"
        continue
    fi
    if LC_ALL=C grep -qU $'\r' "$out"; then
        perbad "$name: writes CR — this output is LF only, including after the last line"
        continue
    fi
    if [ "$(tail -c1 "$out" | wc -l)" != 1 ]; then
        perbad "$name: does not end its output with a newline"
        continue
    fi
    if grep -qvE '^[^=]+=' "$out"; then
        perbad "$name: prints a line that is not key=value"
        continue
    fi
    head="$(head -6 "$out" | cut -d= -f1 | tr '\n' ' ')"
    if [ "${head% }" != "$FIXED" ]; then
        perbad "$name: the six fixed lines are wrong, or in the wrong order"
        echo "        want: $FIXED"
        echo "        got:  ${head% }"
        continue
    fi
    # Then three lines per source, indexed from zero. Derived rather than
    # spelled out, so a fixture gaining a source does not silently become the
    # definition of the shape.
    if ! tail -n +7 "$out" | cut -d= -f1 | awk '
        { g=int((NR-1)/3); r=(NR-1)%3
          want = (r==0 ? "source" g : (r==1 ? "source" g ".attrs" : "source" g ".headers"))
          if ($0 != want) { printf "        line %d: want %s, got %s\n", NR+6, want, $0; bad=1 } }
        END { if (NR%3) { printf "        a source is three lines; %d are left over\n", NR%3; bad=1 }
              exit bad ? 1 : 0 }'; then
        perbad "$name: the source lines are wrong, or in the wrong order"
        continue
    fi
    per "$name: prints the documented lines, in order, LF"
done

# --- every implementation must agree about every fixture -------------------
#
# stdout only, and the exit status separately. Merging stderr in made the
# harness judge a reader that refuses on one channel differently from a reader
# that refuses on the other, and neither channel was written down anywhere.
for spec in "$ROOT"/download/testdata/specs/*.json; do
    name="$(basename "$spec" .json)"
    first=""
    agreed=1
    for i in "${IMPLS[@]}"; do
        out="$WORK/out.$i"
        read_spec "$i" "$spec" >"$out" 2>"$WORK/err.$i"
        status=$?
        if [ "$status" != 0 ]; then
            bad "$name: $i exits $status ($(said "$WORK/err.$i" "$out"))"
            agreed=0
            continue
        fi
        if [ ! -s "$out" ]; then
            bad "$name: $i produced nothing"
            agreed=0
            continue
        fi
        if LC_ALL=C grep -qU $'\r' "$out"; then
            bad "$name: $i writes CR — this output is LF only"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then
            first="$out"
            firstimpl="$i"
            continue
        fi
        if ! cmp -s "$first" "$out"; then
            bad "$name: $i and $firstimpl disagree"
            diff "$first" "$out" | sed 's/^/        /'
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "$name: all implementations read it the same way"
done

# --- and the specific disagreement that cost a download --------------------
#
# canonical.json and bare-digest.json name the same artifact and differ only in
# whether the digest carries its label. Every implementation must resolve them
# to the same thing — that is the whole bug, pinned.
for i in "${IMPLS[@]}"; do
    a="$(read_spec "$i" "$ROOT/download/testdata/specs/canonical.json" | grep '^digest=')"
    b="$(read_spec "$i" "$ROOT/download/testdata/specs/bare-digest.json" | grep '^digest=')"
    if [ "$a" = "$b" ] && [ "$a" != "digest=" ]; then
        per "$i: a labelled and an unlabelled digest name the same artifact"
    else
        perbad "$i: reads them as different artifacts ($a vs $b)"
    fi
done

# --- an unreadable digest must not read as "matches anything" --------------
cat > "$WORK/rubbish.json" <<'EOF'
{"artifact":{"digest":"not-a-digest"},"sources":[{"scheme":"https","locator":"https://example.invalid/x"}],"sink":{"final":"x"}}
EOF
for i in "${IMPLS[@]}"; do
    if [ "$(read_spec "$i" "$WORK/rubbish.json" | grep '^digest=')" = "digest=" ]; then
        per "$i: refuses to interpret a malformed digest"
    else
        perbad "$i: invented a meaning for a malformed digest"
    fi
done

# --- a contract refusal is a value on stdout, not a channel or a status ----
#
# escaping-sink.json is a record every implementation must refuse to write, for
# the same stated reason, in the same words. Refusing it by exiting non-zero or
# by printing to stderr is a different contract: the caller learns that the whole
# spec was unreadable rather than that one path in it is out of bounds, and the
# other five lines it could have acted on are gone.
#
# The wording is pinned by its fixed part and the path by the comparison above,
# rather than both being spelled out here: this fixture is where a new escape is
# added, and a check that has to be edited alongside it is a check that will be
# edited to agree with whatever the implementations now do.
ESCAPE='download: sink path escapes the store root: '
for i in "${IMPLS[@]}"; do
    out="$WORK/escape.$i"
    read_spec "$i" "$ROOT/download/testdata/specs/escaping-sink.json" >"$out" 2>"$WORK/escape.$i.err"
    status=$?
    f="$(grep '^final_refusal=' "$out" 2>/dev/null)"
    p="$(grep '^partial_refusal=' "$out" 2>/dev/null)"
    stated=1
    case "$f" in "final_refusal=$ESCAPE"?*) ;; *) stated=0 ;; esac
    case "$p" in "partial_refusal=$ESCAPE"?*) ;; *) stated=0 ;; esac
    if [ "$status" = 0 ] && [ "$stated" = 1 ]; then
        per "$i: refuses an escaping sink as a value, in the words the contract fixes"
    else
        perbad "$i: does not state the escaping-sink refusal as the contract fixes it (exit $status)"
        echo "        want: final_refusal=${ESCAPE}<the path as the spec spells it>, and the same for partial_refusal, at exit 0"
        echo "        got:  ${f:-<no final_refusal line>} / ${p:-<no partial_refusal line>} $(said "$WORK/escape.$i.err" "$out")"
    fi
done

# --- the partial name, which nobody reads and everybody writes -------------
#
# Every other check here asks whether the implementations READ a spec the same
# way. This one asks whether they WRITE the same one, and it is the only field a
# caller is allowed to leave out — so the layer invents it, and a successor in
# another language finds a predecessor's bytes only if it invents the same name.
#
# It went uncovered for exactly the reason that makes it dangerous: it is not in
# any fixture, because it does not exist until somebody submits. Meanwhile the
# ComfyUI node was inventing `<final>.part` by hand, so the convention had two
# authors and one of them was an application.
#
# Git Bash rewrites an argument that looks like a POSIX path into a Windows one
# before the program sees it, so "/mnt/models/x.gguf" would arrive as
# "C:/Program Files/Git/mnt/...". Every implementation would receive the mangled
# string and agree about it, which is the worst kind of green: the test passes
# without ever examining the path it names. Excluded here and nowhere else --
# excluded by prefix rather than wholesale: turning conversion off for every
# argument sends unconverted /tmp paths to a Go compiler and a Python.
partial_of() { ( export MSYS2_ARG_CONV_EXCL='/mnt'; read_spec "$@" ); }

for final in "models/x.gguf" "a/b/../c/x.gguf" 'D:\models\x.gguf' "/mnt/models/x.gguf" '\\nas\share\x.gguf' ""; do
    first=""
    agreed=1
    for i in "${IMPLS[@]}"; do
        out="$(partial_of "$i" --partial "$final" "1757000000000-deadbeef" 2>"$WORK/perr")"
        status=$?
        if [ "$status" != 0 ]; then
            bad "partial for '${final:-<empty>}': $i exits $status (${out:-$(said "$WORK/perr")})"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then first="$out"; firstimpl="$i"; continue; fi
        if [ "$out" != "$first" ]; then
            bad "partial for '$final': $i says '$out', $firstimpl says '$first'"
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "partial for '${final:-<empty>}': all agree on '$first'"
done

# And the two cases are different, or the rule has quietly collapsed into one.
# A relative final belongs to the store's work directory on whichever machine
# adopts the job; an absolute one names a volume that may not be the store's, so
# the partial sits beside the artifact and delivery stays a rename. Comparing
# implementations against each other cannot catch them agreeing on the wrong
# thing, so the behaviour itself is pinned here.
rel="$(partial_of "$firstimpl" --partial "models/x.gguf" "abc")"
abs="$(partial_of "$firstimpl" --partial "/mnt/models/x.gguf" "abc")"
if [ "$rel" = "partial=work/abc" ] && [ "$abs" = "partial=/mnt/models/x.gguf.part" ]; then
    ok "a relative final partials into the store, an absolute one beside the artifact"
else
    bad "the partial convention changed: relative='$rel' absolute='$abs'"
fi

# --- the store's own layout, which a sink may not name ---------------------
#
# Containment stopped a sink climbing OUT of the store root. It never stopped
# one aiming at what is IN it: a final of `jobs/<id>.json` overwrites a job
# record and a final of `work/<other>` overwrites another job's partial. Both
# are contained and both were accepted by every implementation.
#
# Not expressible as a fixture, because the answer depends on WHICH job is
# asking -- work/<id> is reserved against everyone except the job it belongs to
# -- so it is driven as a table, like the partial above. Case is folded on every
# platform here and only on Windows in the containment check, deliberately: a
# record refused by one host and accepted by another means the refusal depends
# on who looked.
ME=1757000000000-deadbeef
OTHER=1757000000001-cafebabe
reserved_of() { ( export MSYS2_ARG_CONV_EXCL='/mnt;/models'; read_spec "$@" ); }

for p in "jobs/$OTHER.json" "jobs/$ME.json" "jobs/$ME.epoch.7" "jobs" "work" \
         "work/$OTHER" "work/$OTHER/part" "services.json" "supervisor.json" \
         "supervisor.json.tmp" "supervisor.sock" "Jobs/$OTHER.json" \
         "jobs\\$OTHER.json" "JOBS/$OTHER.json" "models/../jobs/$OTHER.json" \
         "Supervisor.json" "WORK/$OTHER" \
         "work/$ME" "work/$ME/part" "models/x.gguf" "jobsy/x.json" \
         "a/jobs/x.json" "services.json.bak" 'D:\models\x.gguf' \
         "/mnt/models/x.gguf" ""; do
    first=""
    agreed=1
    for i in "${IMPLS[@]}"; do
        out="$(reserved_of "$i" --reserved "$ME" "$p" 2>"$WORK/rerr")"
        status=$?
        if [ "$status" != 0 ]; then
            bad "reserved '${p:-<empty>}': $i exits $status (${out:-$(said "$WORK/rerr")})"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then first="$out"; firstimpl="$i"; continue; fi
        if [ "$out" != "$first" ]; then
            bad "reserved '$p': $i says '$out', $firstimpl says '$first'"
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "reserved '${p:-<empty>}': all agree on '$first'"
done

# Agreeing on the wrong thing is not caught by comparing implementations, so
# the two answers that carry the whole rule are pinned to their text.
mine="$(reserved_of "$firstimpl" --reserved "$ME" "work/$ME")"
theirs="$(reserved_of "$firstimpl" --reserved "$ME" "work/$OTHER")"
if [ "$mine" = "reserved=" ] && \
   [ "$theirs" = "reserved=download: sink path is reserved by the store: work/$OTHER" ]; then
    ok "a job owns work/<its own id> and nothing else in the store"
else
    bad "the reserved rule changed: own='$mine' other='$theirs'"
fi

# --- an absolute sink written in the other platform's convention -----------
#
# The contract said an absolute path is left alone, so a Windows path handed to
# Linux "fails with no such file rather than quietly creating a directory".
# True of reading and false of a sink: O_CREAT on `D:\models\x.gguf` under Linux
# makes a file of that literal name in the working directory with a `.part`
# beside it. This is the one answer that depends on the host, so what is checked
# is that the three implementations give the SAME answer on this one.
for p in 'D:\models\x.gguf' '\\nas\share\x.gguf' "/mnt/models/x.gguf" \
         "models/x.gguf" ""; do
    first=""
    agreed=1
    for i in "${IMPLS[@]}"; do
        out="$(reserved_of "$i" --foreign "$p" 2>"$WORK/ferr")"
        status=$?
        if [ "$status" != 0 ]; then
            bad "foreign '${p:-<empty>}': $i exits $status (${out:-$(said "$WORK/ferr")})"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then first="$out"; firstimpl="$i"; continue; fi
        if [ "$out" != "$first" ]; then
            bad "foreign '$p': $i says '$out', $firstimpl says '$first'"
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "foreign '${p:-<empty>}': all agree on '$first'"
done

# And one of the two IS refused here, whichever host this is, or the check is
# passing because nothing is ever refused.
if [ "$(reserved_of "$firstimpl" --foreign 'D:\models\x.gguf')" = "foreign=" ] && \
   [ "$(reserved_of "$firstimpl" --foreign '/mnt/models/x.gguf')" = "foreign=" ]; then
    bad "no absolute path is foreign on this host, so the check proves nothing"
else
    ok "an absolute path in the other platform's convention is refused"
fi

# --- the spelling a path gets when it is written into a record -------------
#
# Every check above reads a path back. None of them could see what the layer
# WROTE, and that is where the defect was: two finished jobs in the monitor, one
# destination spelled `C:/Users/...` because it came back through the NAS and one
# spelled `C:\Users\...` because it was fetched here, and the second changing
# convention halfway along — backslashes as far as `snapshots\<sha>`, then a
# forward slash before the file name, because an adopter joined a native
# directory to a file name with a hardcoded `/`. Three implementations and two
# cross-language harnesses agreed about that path. A person looking at a window
# for a few minutes did not.
#
# So: one separator per record, `/`, whatever wrote it. A drive letter and a UNC
# root still say Windows after the rewrite, which is what --foreign is asked
# below, and a POSIX-rooted path is untouched because a backslash is a legal
# character in a file's name there.
portable_of() { ( export MSYS2_ARG_CONV_EXCL='/mnt;/models;//nas'; read_spec "$@" ); }

for p in 'C:\Users\r\snapshots\41ba88db/x.gguf' 'D:\models\x.gguf' \
         '\\nas\share\x.gguf' 'models\x.gguf' "models/x.gguf" \
         "/mnt/models/x.gguf" ""; do
    first=""
    agreed=1
    for i in "${IMPLS[@]}"; do
        out="$(portable_of "$i" --portable "$p" 2>"$WORK/perr")"
        status=$?
        if [ "$status" != 0 ]; then
            bad "portable '${p:-<empty>}': $i exits $status (${out:-$(said "$WORK/perr")})"
            agreed=0
            continue
        fi
        if [ -z "$first" ]; then first="$out"; firstimpl="$i"; continue; fi
        if [ "$out" != "$first" ]; then
            bad "portable '$p': $i says '$out', $firstimpl says '$first'"
            agreed=0
        fi
    done
    [ "$agreed" = "1" ] && ok "portable '${p:-<empty>}': all agree on '$first'"
done

# Agreeing is not the rule; three implementations can agree on a malformed path,
# and did. These are the answers themselves.
want_mixed='portable=C:/Users/r/snapshots/41ba88db/x.gguf'
got_mixed="$(portable_of "$firstimpl" --portable 'C:\Users\r\snapshots\41ba88db/x.gguf')"
if [ "$got_mixed" = "$want_mixed" ]; then
    ok "a path that changed convention halfway along comes out with one separator"
else
    bad "the mixed-separator path is still mixed: '$got_mixed' (want '$want_mixed')"
fi

want_unc='portable=//nas/share/x.gguf'
got_unc="$(portable_of "$firstimpl" --portable '\\nas\share\x.gguf')"
if [ "$got_unc" = "$want_unc" ]; then
    ok "a UNC root is respelled with the same separator as everything else"
else
    bad "the UNC spelling changed: '$got_unc' (want '$want_unc')"
fi

posix="$(portable_of "$firstimpl" --portable "/mnt/models/x.gguf")"
if [ "$posix" = "portable=/mnt/models/x.gguf" ]; then
    ok "a POSIX-rooted path is left exactly as it was"
else
    bad "a POSIX-rooted path was rewritten: '$posix'"
fi

# Stable, or the deduplication that makes any of this worth having does not
# work: the same file named twice must produce the same bytes in the record. Run
# over the whole list above, so a spelling this rule invents can never be one
# the rule would then change again.
for p in 'C:\Users\r\snapshots\41ba88db/x.gguf' 'D:\models\x.gguf' \
         '\\nas\share\x.gguf' 'models\x.gguf' "/mnt/models/x.gguf"; do
    for i in "${IMPLS[@]}"; do
        once="$(portable_of "$i" --portable "$p")"
        twice="$(portable_of "$i" --portable "${once#portable=}")"
        if [ "$once" != "$twice" ]; then
            bad "$i: portable is not stable — '$p' gives '$once' then '$twice'"
        fi
    done
done
ok "every spelling this rule produces survives a second pass unchanged"

# And the rewrite did not cost the path its platform: a UNC sink is still an
# absolute Windows path afterwards, so the one host that can write it still may
# and every other host still refuses it. Asked of both spellings on every
# implementation, because this is the classifier the rewrite could have broken.
for i in "${IMPLS[@]}"; do
    raw="$(reserved_of "$i" --foreign '\\nas\share\x.gguf')"
    respelled="$(reserved_of "$i" --foreign '//nas/share/x.gguf')"
    drive="$(reserved_of "$i" --foreign 'D:\models\x.gguf')"
    if [ "$raw" = "$respelled" ] && [ "$raw" = "$drive" ]; then
        per "$i: a respelled UNC root is still an absolute Windows path"
    else
        perbad "$i: the UNC rewrite changed which host may write the path (raw='$raw' respelled='$respelled' drive='$drive')"
    fi
done

# --- and the contract this script enforces is written down -----------------
#
# A stranger's reader failed every check here for things stated in no document:
# the line set, their order, the refusal's channel and wording. Silence is not
# visible from inside — three implementations transcribed from one author agree
# about the unwritten parts by descent, and the harness reads that as conformance.
# So every key this script demands must appear in the page a fourth implementer
# is handed, or the instrument is measuring agreement rather than a contract.
DOC="$ROOT/download/README.md"
undocumented=""
for k in $SHAPE; do
    grep -qF "$k=" "$DOC" || undocumented="$undocumented $k="
done
grep -qF 'download: sink path escapes the store root' "$DOC" || undocumented="$undocumented <the-refusal-wording>"
grep -qF 'download: sink path is reserved by the store' "$DOC" || undocumented="$undocumented <the-reserved-wording>"
grep -qF "download: sink path names another platform's filesystem" "$DOC" || undocumented="$undocumented <the-foreign-wording>"
for k in reserved foreign portable; do
    grep -qF -- "--$k" "$DOC" || undocumented="$undocumented <--$k>"
done
grep -qF 'SPECREAD_EXTRA' "$DOC" || undocumented="$undocumented <how-to-register-an-implementation>"
if [ -z "$undocumented" ]; then
    ok "every line this script compares is specified in download/README.md"
else
    bad "compared here and written down nowhere:$undocumented"
fi

echo
if [ -n "$ABSENT" ]; then
    each=$((solo / ${#IMPLS[@]}))
    missing=0
    for r in $ROSTER; do
        case " ${IMPLS[*]} " in *" $r "*) ;; *) missing=$((missing+1)) ;; esac
    done
    echo "========================================"
    echo "  ABSENT — $missing of the $(echo $ROSTER | wc -w) implementations did not run$ABSENT"
    echo
    echo "  $each of the checks above are per implementation, so this total is"
    echo "  $((each * missing)) smaller than a full roster would produce. Nothing here failed;"
    echo "  a smaller number is not a better one."
    echo "========================================"
fi
echo "----------------------------------------"
echo "  $pass passed, $fail failed — implementations: ${IMPLS[*]}"
[ "$fail" -eq 0 ]
