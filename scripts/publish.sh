#!/bin/sh
# Push the generated trees in .split/<repo> to github.com/openabstractions.
# scripts/split.sh builds them and never pushes; this is the step that does.
#
#   scripts/publish.sh                      what would change, per repository
#   scripts/publish.sh --adopt <repo>...    record the mirror's HEAD as the baseline
#   scripts/publish.sh --approve <repo>...  bind an approval to this diff and these revisions
#   scripts/publish.sh --push <repo>...     publish those repositories
#   scripts/publish.sh --push --all         publish every repository that differs
#   scripts/publish.sh --verbose            with the changed paths under each one
#
# One line per repository and the verdict last, so nothing has to be grepped.
# The changed paths are behind --verbose because the count is the answer and
# twenty of them per repository is what taught people to filter this.
#
# Publishing is the owner's decision. Without --push nothing is committed and no
# remote is written to.
#
# The deny scan runs over the candidate tree - the clone after the generated
# files are copied over it and the dropped paths removed - because a file this
# tree does not generate is still a file this tree publishes.
#
# Three trees, not two. The mirror's HEAD and the generated candidate are not
# enough to tell "this file changed here" from "this file changed in the
# mirror": the copy below reverts a merged contribution and reports it as our
# own edit. So the sha of the last tree we pushed is kept beside the approval
# tokens and the two are compared. There is nothing to compare against on the
# first run, and assuming the mirror is ours is exactly how a file was lost, so
# the first run refuses until --adopt records a baseline the operator looked at.
#
# A refusal exits non-zero, names the file and names the rule:
#
#   2 usage       3 fetch      5 destination     7 denied string
#   4 a file the manifest declares that would not be published    6 approval
#   8 the mirror moved under us: no baseline, or an edit this publish would erase
set -u
cd "$(dirname "$0")/.."
ROOT=$PWD

OUT="${ABSTRACTION_SPLIT:-$ROOT/.split}"
ORG="${ABSTRACTION_ORG:-openabstractions}"
REMOTE="${ABSTRACTION_REMOTE:-git@github.com:$ORG}"
MAN="$ROOT/scripts/split.manifest"
WORK="$OUT/.publish"
TOKENS="$WORK/approved"
EXPORTED="$WORK/exported"
PUSH=0
ALL=0
APPROVE=0
ADOPT=0
VERBOSE=0
WANT=""
RC=0
NREFUSED=0

for a in "$@"; do
    case "$a" in
    --push)    PUSH=1 ;;
    --approve) APPROVE=1 ;;
    --adopt)   ADOPT=1 ;;
    --all)     ALL=1 ;;
    --verbose) VERBOSE=1 ;;
    -*)        echo "unknown flag: $a" >&2; exit 2 ;;
    *)         WANT="$WANT $a" ;;
    esac
done
[ "$PUSH$APPROVE" = "11" ] && { echo "--approve and --push are separate acts" >&2; exit 2; }
[ "$ADOPT" = 1 ] && [ "$PUSH$APPROVE" != "00" ] && { echo "--adopt is its own act" >&2; exit 2; }

[ -d "$OUT" ] || { echo "no $OUT - run scripts/split.sh first" >&2; exit 1; }
[ -f "$MAN" ] || { echo "no $MAN" >&2; exit 1; }

TMP=$(mktemp -d) || exit 1
trap 'rm -rf "$TMP"' EXIT

refuse() {
    code=$1
    shift
    printf '  \033[31mREFUSED\033[0m  %s\n' "$*" >&2
    NREFUSED=$((NREFUSED + 1))
    [ "$RC" = 0 ] && RC=$code
    return 0
}

# The same reading of the manifest as scripts/split.sh: a regex is the rest of
# the line, because a deny containing a space is not one awk field.
awk '$1 ~ /^>/ || NF==0 { next }
     $1=="repo" { r=$2; next }
     $1=="deny" || $1=="allow" { p=$0; sub(/^[ \t]*[a-z]+[ \t]+/, "", p)
         printf "-\t%s\t%s\n", $1, p; next }
     { printf "%s\t%s\t%s\n", (r==""?"-":r), $1, $2 }' "$MAN" > "$TMP/rules"

awk -F'\t' '$2=="deny"  { if (d) print d "\t" (a?a:"^$"); d=$3; a="" }
            $2=="allow" { a=$3 }
            END         { if (d) print d "\t" (a?a:"^$") }' "$TMP/rules" > "$TMP/deny"

ALLDENY=$(cut -f1 "$TMP/deny" | paste -sd'|' -)
[ -n "$ALLDENY" ] || { echo "scripts/split.manifest carries no deny rule" >&2; exit 1; }

matches() {
    mp=$1
    while read -r mg; do case "$mp" in $mg) return 0 ;; esac; done < "$2"
    return 1
}

digest() {
    (
        cd "$1" || exit 1
        git -c core.quotepath=false status --porcelain -uall --no-renames > "$TMP/state"
        cut -c4- "$TMP/state" | while IFS= read -r p; do
            [ -f "$p" ] && printf '%s\n' "$p"
        done > "$TMP/present"
        cat "$TMP/state"
        [ -s "$TMP/present" ] && tr '\n' '\0' < "$TMP/present" | xargs -0 sha256sum
    ) | sha256sum | cut -d' ' -f1
}

repos=$(find "$OUT" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -exec basename {} \; | sort)
[ -n "$WANT" ] && repos=$WANT
[ "$PUSH$ALL" = "10" ] && [ -z "$WANT" ] && { echo "--push needs repository names, or --all" >&2; exit 2; }
[ "$APPROVE" = 1 ] && [ -z "$WANT" ] && { echo "--approve needs repository names" >&2; exit 2; }
[ "$ADOPT" = 1 ] && [ -z "$WANT" ] && { echo "--adopt needs repository names" >&2; exit 2; }

mkdir -p "$WORK" "$TOKENS" "$EXPORTED"
SOURCE=$(git -C "$ROOT" rev-parse HEAD)
printf '\033[1msource\033[0m %s\n\033[1mdestination\033[0m %s\n\n' "$SOURCE" "$REMOTE"
changed=0
published=0
uptodate=0

for r in $repos; do
    gen="$OUT/$r"
    d="$WORK/$r"
    url="$REMOTE/$r.git"

    [ -d "$gen" ] || { refuse 4 "$r  no generated tree in ${OUT#$ROOT/} - run scripts/split.sh"; continue; }

    if [ -d "$d/.git" ]; then
        git -C "$d" fetch -q origin 2>/dev/null || { refuse 3 "$r  cannot fetch $url"; continue; }
    else
        rm -rf "$d"
        # The candidate tree is compared byte for byte with the generated tree,
        # so its working files must be the published bytes. A Windows checkout
        # with core.autocrlf on rewrites every text file and makes all of them
        # differ from a generated tree that never left LF.
        git clone -q -c core.autocrlf=false -c core.eol=lf "$url" "$d" 2>/dev/null ||
            { refuse 3 "$r  cannot clone $url"; continue; }
    fi
    if [ "$(git -C "$d" config --get core.autocrlf || echo)" != "false" ]; then
        git -C "$d" config core.autocrlf false
        git -C "$d" config core.eol lf
        rm -f "$d/.git/index"
    fi

    origin=$(git -C "$d" config --get remote.origin.url 2>/dev/null || echo '')
    [ "$origin" = "$url" ] || { refuse 5 "$r  destination is $origin, expected $url"; continue; }

    dest=$(git -C "$d" ls-remote origin HEAD 2>/dev/null | cut -f1)
    [ -n "$dest" ] || { refuse 3 "$r  cannot read HEAD from $url"; continue; }
    git -C "$d" reset -q --hard "$dest" 2>/dev/null || { refuse 3 "$r  $dest is not in the clone of $url"; continue; }
    git -C "$d" clean -qfdx

    if [ "$ADOPT" = 1 ]; then
        was=$(cat "$EXPORTED/$r" 2>/dev/null || echo)
        printf '%s\n' "$dest" > "$EXPORTED/$r"
        if [ -n "$was" ]; then
            printf '  \033[32madopted\033[0m  %-22s %.12s -> %.12s\n' "$r" "$was" "$dest"
        else
            printf '  \033[32madopted\033[0m  %-22s %.12s  first baseline\n' "$r" "$dest"
        fi
        continue
    fi

    : > "$TMP/drop"
    : > "$TMP/notgen"
    : > "$TMP/declared"
    awk -F'\t' -v r="$r" -v drop="$TMP/drop" -v notgen="$TMP/notgen" -v decl="$TMP/declared" '
        $1!=r { next }
        $2=="drop" { print $3 > drop }
        $2=="own" || $2=="drop" { print $3 > notgen }
        $2=="file" || $2=="door" || $2=="contract" { print $3 > decl }' "$TMP/rules"

    : > "$TMP/missing"
    sort -u "$TMP/declared" |
    while read -r dst; do
        [ -n "$dst" ] || continue
        matches "$dst" "$TMP/notgen" && continue
        [ -f "$gen/$dst" ] || echo "$dst" >> "$TMP/missing"
    done
    if [ -s "$TMP/missing" ]; then
        while read -r dst; do
            refuse 4 "$r  $dst  declared in scripts/split.manifest, generated by nothing"
        done < "$TMP/missing"
        continue
    fi

    (cd "$gen" && find . -type f) | sed 's|^\./||' | sort > "$TMP/genlist"

    exported=$(cat "$EXPORTED/$r" 2>/dev/null || echo)
    if [ -z "$exported" ]; then
        refuse 8 "$r  no baseline - a mirror-side edit cannot be told from a fresh publish; read $url, then run scripts/publish.sh --adopt $r"
        continue
    fi
    if [ "$exported" != "$dest" ]; then
        if git -C "$d" cat-file -e "$exported^{commit}" 2>/dev/null; then
            git -C "$d" -c core.quotepath=false diff --name-only "$exported" "$dest" > "$TMP/mirror"
            : > "$TMP/clash"
            while read -r p; do
                [ -n "$p" ] || continue
                grep -qxF -- "$p" "$TMP/genlist" && printf '%s\n' "$p" >> "$TMP/clash"
            done < "$TMP/mirror"
            if [ -s "$TMP/clash" ]; then
                while read -r p; do
                    who=$(git -C "$d" log -1 --format='%h by %an' "$exported..$dest" -- "$p")
                    refuse 8 "$r  $p  changed in the mirror ($who) since we exported $(printf '%.12s' "$exported") - port it upstream and --adopt, do not let this publish erase it"
                done < "$TMP/clash"
                continue
            fi
        else
            refuse 8 "$r  baseline $(printf '%.12s' "$exported") is not a commit in $url - read the mirror, then run scripts/publish.sh --adopt $r"
            continue
        fi
    fi
    cp -R "$gen/." "$d/"

    # A drop names paths and is never a synchronisation: a published file is
    # removed only when a drop glob in the manifest matches it, so a file
    # authored in the org repo and absent from the generated tree stays.
    : > "$TMP/delete"
    if [ -s "$TMP/drop" ]; then
        git -C "$d" ls-files | while read -r p; do
            matches "$p" "$TMP/drop" && echo "$p" >> "$TMP/delete"
        done
    fi
    if [ -s "$TMP/delete" ]; then
        printf '  \033[33mdelete\033[0m   %-22s %s published file(s) named by a drop rule in scripts/split.manifest\n' \
               "$r" "$(wc -l < "$TMP/delete")"
        sed 's/^/          /' "$TMP/delete"
        while read -r p; do git -C "$d" rm -q -f -- "$p"; done < "$TMP/delete"
        grep -vxF -f "$TMP/delete" "$TMP/genlist" > "$TMP/keep"
        mv "$TMP/keep" "$TMP/genlist"
    fi

    # git add skips an ignored path rather than failing, so a generated file the
    # destination's own .gitignore covers would be reported as differing, be
    # counted in the approved diff, and never reach the repository.
    if git -C "$d" check-ignore --stdin < "$TMP/genlist" > "$TMP/ignored" 2>/dev/null; then
        while read -r p; do
            refuse 4 "$r  $p  ignored by the destination's .gitignore and would not be published"
        done < "$TMP/ignored"
        continue
    fi

    # One pass over the tree for every deny at once, because a process per rule
    # per repository is most of a publish on Windows. Which rule caught what is
    # worked out afterwards, over the handful of lines that matched anything.
    leaked=0
    (cd "$d" && grep -rInE "$ALLDENY" . --exclude-dir=.git 2>/dev/null) > "$TMP/hits"
    if [ -s "$TMP/hits" ]; then
        while IFS='	' read -r pat exempt; do
            [ -z "$pat" ] && continue
            # File and line, never the line's text: a refusal that printed what
            # it caught would copy the credential into the terminal and into
            # every log that scrolls past it. The allow is tested against the
            # matched text alone - tested against the whole grep line it would
            # exempt every hit in any file whose PATH contained the word, which
            # is the per-file waiver scripts/split.manifest says does not exist.
            grep -E "$pat" "$TMP/hits" |
            awk -F: -v ex="$exempt" '{ t=$0; sub(/^[^:]*:[0-9]+:/, "", t)
                if (t !~ ex) print $1 ":" $2 }' |
            sed 's|^\./||' | sort -u > "$TMP/caught"
            [ -s "$TMP/caught" ] || continue
            head -5 "$TMP/caught" > "$TMP/first"
            while read -r h; do
                f=${h%%:*}
                if [ -f "$gen/$f" ]; then whose=generated; else whose="public-authored, retained"; fi
                refuse 7 "$r  $h  matches deny /$pat/  ($whose)"
            done < "$TMP/first"
            n=$(wc -l < "$TMP/caught")
            [ "$n" -gt 5 ] && refuse 7 "$r  and $((n - 5)) more line(s) matching deny /$pat/"
            leaked=1
        done < "$TMP/deny"
    fi
    [ "$leaked" = 0 ] || continue

    state=$(git -C "$d" status --porcelain -uall)
    if [ -z "$state" ]; then
        printf '  \033[32mok\033[0m       %-22s up to date\n' "$r"
        uptodate=$((uptodate + 1))
        continue
    fi

    changed=$((changed + 1))
    n=$(printf '%s\n' "$state" | wc -l)
    printf '  \033[1mdiffer\033[0m   %-22s %s file(s)\n' "$r" "$n"
    if [ "$VERBOSE" = 1 ]; then
        printf '%s\n' "$state" | sed 's/^/          /'
    fi
    diff=$(digest "$d")

    if [ "$APPROVE" = 1 ]; then
        printf 'source\t%s\ndest\t%s\ndiff\t%s\n' "$SOURCE" "$dest" "$diff" > "$TOKENS/$r"
        printf '          \033[32mapproved\033[0m  source %.12s  destination %.12s  diff %.12s\n' \
               "$SOURCE" "$dest" "$diff"
        continue
    fi
    [ "$PUSH" = 1 ] || continue

    tok="$TOKENS/$r"
    [ -f "$tok" ] || { refuse 6 "$r  no approval - run scripts/publish.sh --approve $r"; continue; }
    was_source=$(awk -F'\t' '$1=="source" { print $2 }' "$tok")
    was_dest=$(awk -F'\t' '$1=="dest" { print $2 }' "$tok")
    was_diff=$(awk -F'\t' '$1=="diff" { print $2 }' "$tok")
    stale=""
    [ "$was_source" = "$SOURCE" ] || stale="$stale source revision ($was_source -> $SOURCE);"
    [ "$was_dest" = "$dest" ]     || stale="$stale destination moved ($was_dest -> $dest);"
    [ "$was_diff" = "$diff" ]     || stale="$stale diff ($was_diff -> $diff);"
    [ -z "$stale" ] || { refuse 6 "$r  the approval no longer describes this publication:$stale re-approve it"; continue; }

    git -C "$d" add --pathspec-from-file="$TMP/genlist"
    git -C "$d" commit -q -m "generated from the private tree

scripts/split.sh builds every file here out of one source; scripts/split.manifest
says where each one comes from. Do not edit a generated file in place - the next
publish overwrites it. CONTRIBUTING.md says where a fix goes instead, and this
subject line is how you tell a generated file from one authored here."
    if git -C "$d" push -q origin HEAD; then
        git -C "$d" rev-parse HEAD > "$EXPORTED/$r"
        printf '          \033[32mpushed\033[0m\n'
        rm -f "$tok"
        published=$((published + 1))
    else
        refuse 1 "$r  push to $url failed"
    fi
done

printf '\n  %s up to date, %s differ, %s published, %s refused' \
       "$uptodate" "$changed" "$published" "$NREFUSED"
[ "$PUSH" = 1 ] || printf '; nothing was pushed'
printf '\n'
[ "$changed" = 0 ] || [ "$VERBOSE" = 1 ] || printf '  --verbose lists the changed paths\n'
if [ "$NREFUSED" = 0 ]; then
    printf '  \033[32mOK\033[0m\n'
else
    printf '  \033[31mREFUSED\033[0m  %s refusal(s), exit %s — the lines above say which\n' "$NREFUSED" "$RC"
fi
exit $RC
