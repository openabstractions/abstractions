#!/usr/bin/env sh
# Measures a built .spk and nothing else. Every other instrument we own reads
# the source tree, and the source tree is the half that already works: the
# description defect shipped inside a package whose sources were green.
#
#   usage: scripts/check-spk.sh <package.spk>
#
# Reads no repository, no network and no NAS, and runs nothing it unpacks.
set -u

[ $# -eq 1 ] || { echo "usage: $0 <package.spk>" >&2; exit 2; }
[ -f "$1" ] || { echo "check-spk: no such file: $1" >&2; exit 2; }
SPK="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"

FAIL=0
bad() { echo "FAIL  $*"; FAIL=1; }
ok()  { echo "ok    $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
tar -C "$WORK" -xf "$SPK" || { echo "check-spk: not a tar archive" >&2; exit 2; }

for f in INFO package.tgz; do
    [ -f "$WORK/$f" ] || { echo "check-spk: package has no $f" >&2; exit 2; }
done

info() { sed -n "s/^$1=\"\(.*\)\"\$/\1/p" "$WORK/INFO"; }
ARCH="$(info arch)"

WANT="$(info checksum)"
GOT="$(md5sum "$WORK/package.tgz" | cut -d' ' -f1)"
[ -n "$WANT" ] && [ "$WANT" = "$GOT" ] &&
    ok "checksum matches package.tgz md5 ($GOT)" ||
    bad "checksum is '$WANT', package.tgz md5 is '$GOT'"

mkdir "$WORK/payload"
tar -C "$WORK/payload" -xzf "$WORK/package.tgz" || bad "package.tgz does not unpack"

SSS="$WORK/scripts/start-stop-status"
[ -f "$SSS" ] || { echo "check-spk: package has no scripts/start-stop-status" >&2; exit 2; }

case "$ARCH" in
    x86_64|amd64) MACHINE=3e ;;
    arm64|aarch64) MACHINE=b7 ;;
    *) MACHINE="" ;;
esac

BINS="$(sed -n 's|.*\$TARGET/bin/\([A-Za-z0-9_.-]*\).*|\1|p' "$SSS" | sort -u)"
[ -n "$BINS" ] || bad "start-stop-status names no binary under \$TARGET/bin"
for b in $BINS; do
    p="$WORK/payload/bin/$b"
    [ -f "$p" ] || { bad "$b is named by start-stop-status and is not in package.tgz"; continue; }

    # ELF ident is magic, class, data; e_machine sits at offset 18. od rather
    # than readelf or file, because build.sh already requires no more than this.
    set -- $(od -An -N20 -tx1 "$p")
    id="$1$2$3$4"; class="$5"; mach="${19}"
    [ "$id" = "7f454c46" ] || { bad "$b is not ELF"; continue; }
    [ "$class" = "02" ] || bad "$b is not ELF64"
    if [ -n "$MACHINE" ]; then
        [ "$mach" = "$MACHINE" ] && ok "$b is ELF64 e_machine=0x$mach, matching arch=$ARCH" ||
            bad "$b has e_machine=0x$mach, arch=$ARCH wants 0x$MACHINE"
    else
        bad "INFO arch='$ARCH' is not one this check knows how to verify"
    fi
    grep -q 'ld-linux' "$p" &&
        bad "$b names a dynamic loader; it is not static" ||
        ok "$b is statically linked"
done

for s in "$WORK"/scripts/*; do
    n="scripts/$(basename "$s")"
    sh -n "$s" 2>/dev/null && ok "$n parses" || bad "$n is not valid sh"
    tr -d '\r' <"$s" | cmp -s - "$s" && ok "$n is LF" || bad "$n contains CR"
done
tar -tvf "$SPK" 2>/dev/null | awk '$NF ~ /^scripts\// && $1 !~ /^d/ && $1 !~ /^.rwxr-xr-x/ {print $NF}' |
    while read -r n; do echo "FAIL  $n is not 0755 in the archive"; done | grep . && FAIL=1

CFG="$WORK/payload/ui/config"
# postinst renders config.in into config; a package shipping the template is
# the normal case, so render it the same way rather than declaring it absent.
[ -f "$CFG" ] || { [ -f "$CFG.in" ] &&
    sed -e 's/@@PORT@@/8734/' -e 's/@@KEY@@/k/' "$CFG.in" >"$CFG"; }
if [ -f "$CFG" ]; then
    awk '
    function reduce(s,   p) {
        gsub(/\\./, "e", s)
        gsub(/"[^"]*"/, "S", s)
        gsub(/-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?/, "V", s)
        gsub(/true|false|null/, "V", s)
        gsub(/[ \t\r\n]/, "", s)
        do {
            p = s
            gsub(/\{\}|\[\]/, "V", s)
            gsub(/\[[VS](,[VS])*\]/, "V", s)
            gsub(/\{S:[VS](,S:[VS])*\}/, "V", s)
        } while (s != p)
        return s
    }
    { doc = doc $0 "\n" }
    END {
        r = reduce(doc)
        if (r != "V" && r != "S") { print "FAIL  ui/config is not valid JSON (reduced to " r ")"; exit 1 }
        print "ok    ui/config is valid JSON"
        if (match(doc, /"type"[ \t]*:[ \t]*"[^"]*"/) == 0) { print "FAIL  ui/config declares no type"; exit 1 }
        t = substr(doc, RSTART, RLENGTH); sub(/.*:[ \t]*"/, "", t); sub(/"$/, "", t)
        # DSM 6.2.4 desktop.js opens a window for "url" and calls launchApp for
        # "legacy", which needs a JavaScript class this package does not ship.
        if (t != "url") { print "FAIL  ui/config type is \"" t "\"; DSM opens a window only for \"url\""; exit 1 }
        print "ok    ui/config type is \"url\""
    }' "$CFG" || FAIL=1
else
    bad "package contains neither ui/config nor ui/config.in"
fi

DISPLAY="$(info displayname)"
DESC="$(info description)"
[ -n "$DISPLAY" ] && ok "displayname is '$DISPLAY'" || bad "displayname is empty"
if [ -z "$DESC" ]; then
    bad "description is empty"
else
    # Package Center shows this to somebody deciding whether to install. The
    # brief asked only that it not *open* with a caveat; the copy that shipped
    # on 2026-09-07 put "plain HTTP on port 8734 ... a random key as its only
    # credential" in sentence two, so a first-sentence rule would have passed
    # it. A caveat belongs in the README at any position, so scan all of it.
    # A trailing project URL is not a protocol reference and is stripped first.
    BODY="$(printf '%s' "$DESC" | sed 's|[ ]*https\{0,1\}://[^ ]*[ ]*$||' | tr 'A-Z' 'a-z')"
    N=0
    printf '%s\n' "$BODY" | tr '.!?' '\n\n\n' | while IFS= read -r s; do
        [ -n "$(printf '%s' "$s" | tr -d ' ')" ] || continue
        N=$((N + 1))
        W=""
        printf '%s' "$s" | grep -Eq '(^|[^a-z])(http|https|tcp|udp|tls|ssl|ssh|smb|nfs|ftp|ipv4|ipv6)([^a-z]|$)' && W="a protocol"
        [ -z "$W" ] && printf '%s' "$s" | grep -Eq '(port[ ]+[0-9]|:[0-9]{2,5}([^0-9]|$))' && W="a port"
        [ -z "$W" ] && printf '%s' "$s" | grep -Eq '(^|[^a-z])(warning|caution|unsigned|insecure|unencrypted|plaintext|plain|password|credential|secret|vulnerable|attacker|exposed|anyone|anybody|open to)([^a-z]|$)' && W="a security caveat"
        [ -n "$W" ] && printf 'FAIL  description sentence %s is %s, which belongs in the README:%s\n' "$N" "$W" "$s"
    done >"$WORK/desc.out"
    grep -q . "$WORK/desc.out" && { cat "$WORK/desc.out"; FAIL=1; } ||
        ok "description is $(printf '%s' "$DESC" | wc -c) chars and names no protocol, port or caveat"
fi

[ "$FAIL" = 0 ] && echo "check-spk: $(basename "$SPK") passes" ||
    echo "check-spk: $(basename "$SPK") FAILED"
exit "$FAIL"
