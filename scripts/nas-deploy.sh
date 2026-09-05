#!/usr/bin/env bash
# Put the current jobd on the NAS and restart it. No DSM UI, no root, no Docker.
#
# WHY THIS DOES NOT USE DOCKER
#
# It cannot, without a decision that is yours rather than mine. On this DS218+
# `docker` lives at /usr/local/bin/docker but /var/run/docker.sock is root:root
# 0660 and sudo wants a password, so `admin` over SSH cannot drive it. Granting a
# user access to that socket is equivalent to granting root, which is a security
# decision and not one to make in passing.
#
# So this deploys the binary directly and supervises it with jobd's own start /
# stop / status. That is not a workaround: `jobd run` in a container and `jobd
# run` on the host are the same program over the same store — the container was
# only ever buying restart-on-boot. See "surviving a reboot" at the bottom for
# the one-time setup that gets that back, which you do once and never again.
#
#   usage: nas-deploy.sh [user@host] [remote-dir]
#     env: NAS_STORE   the store on the NAS (default /volume1/docker/abstraction/store)
#          NAS_KEY     ssh key (default ~/.ssh/id_ed25519_nas)

set -u

REPO="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${1:?usage: nas-deploy.sh <user>@<nas-host> [remote-dir]}"
REMOTE="${2:-/volume1/docker/abstraction}"
NAS_STORE="${NAS_STORE:-/volume1/docker/abstraction/store}"
NAS_KEY="${NAS_KEY:-$HOME/.ssh/id_ed25519_nas}"

step() { printf '\n--- %s ---\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*"; exit 1; }

# Synology's sshd is old enough to warn about post-quantum key exchange on every
# connection. That is a real notice and not one this script can act on, so it is
# filtered from the transcript rather than left to bury the output.
quiet() { grep -v "post-quantum\|store now, decrypt\|openssh.com/pq\|^\*\*" || true; }
nas() { ssh -i "$NAS_KEY" -o BatchMode=yes "$TARGET" "$@" 2>&1 | quiet; }

command -v ssh >/dev/null || fail "no ssh"
[ -f "$NAS_KEY" ] || fail "no key at $NAS_KEY"

step "build"
# The same static Linux binary the image is made from, so what runs here and
# what would run in a container are byte-identical.
( cd "$REPO/download/go" \
  && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
     go build -trimpath -ldflags="-s -w" -o "$REPO/deploy/nas/build/jobd" ./cmd/jobd ) \
  || fail "cross-build"
LOCAL_SUM=$(sha256sum "$REPO/deploy/nas/build/jobd" | cut -d' ' -f1)
echo "  jobd $(stat -c %s "$REPO/deploy/nas/build/jobd" 2>/dev/null || stat -f %z "$REPO/deploy/nas/build/jobd") bytes  sha256:${LOCAL_SUM:0:16}…"

step "is something already supervising that store?"
# A container announces itself with its own hostname, so a heartbeat from "jobd"
# rather than from the NAS means Docker is running one and this script must not
# start a second. Two supervisors over one store is the exact damage the lease
# exists to prevent, and it would be self-inflicted.
BEFORE=$(nas "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' status 2>/dev/null | head -3")
echo "$BEFORE" | sed 's/^/  /'
if echo "$BEFORE" | grep -q "supervisor: jobd@jobd"; then
    fail "a CONTAINER is supervising this store. Stop it in DSM first, or deploy the image instead:
    Docker → Container → jobd → Stop
    then re-run this script, or import $REMOTE/jobd-image.tar and let Docker own it."
fi

step "copy"
# -O matters: DSM's SSH has no SFTP subsystem and modern scp speaks SFTP by
# default, which fails with "subsystem request failed on channel 0".
#
# Copied to a temporary name and moved into place, because the file being
# replaced may be the one currently executing — Linux tolerates that far better
# than overwriting it in place.
scp -O -i "$NAS_KEY" -o BatchMode=yes "$REPO/deploy/nas/build/jobd" "$TARGET:$REMOTE/jobd.new" 2>&1 | quiet
REMOTE_SUM=$(nas "sha256sum '$REMOTE/jobd.new' | cut -d' ' -f1" | tr -d '\r')
[ "$REMOTE_SUM" = "$LOCAL_SUM" ] || fail "the copy does not match what was built: $REMOTE_SUM"
echo "  sha256 matches what was built"

step "stop, replace, start"
nas "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' stop" | sed 's/^/  /'
nas "chmod +x '$REMOTE/jobd.new' && mv -f '$REMOTE/jobd.new' '$REMOTE/jobd'" | sed 's/^/  /'
nas "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' start" | sed 's/^/  /'

step "verify"
# Ask the NAS what it thinks, rather than trusting that the start command
# returned. A supervisor that failed to come up looks exactly like one that
# came up, until somebody reads the store.
AFTER=$(nas "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' status")
echo "$AFTER" | sed 's/^/  /'
echo "$AFTER" | grep -q "supervisor: jobd@" \
    || fail "it is not running. Its log is at $NAS_STORE/jobd.log"

step "done"
cat <<EOF
The NAS is running the build from this working tree, over $NAS_STORE.

  log      $NAS_STORE/jobd.log
  stop     ssh $TARGET "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' stop"
  status   ssh $TARGET "ABSTRACTION_STORE='$NAS_STORE' '$REMOTE/jobd' status"

Surviving a reboot — one-time, and the only thing Docker was giving you:

  DSM → Control Panel → Task Scheduler → Create → Triggered Task → User-defined
    Task: jobd
    User: admin
    Event: Boot-up
    Command:  ABSTRACTION_STORE=$NAS_STORE $REMOTE/jobd start

  Run it once from that screen to check it, then leave it. After that this
  script is the whole deployment, every time.
EOF
