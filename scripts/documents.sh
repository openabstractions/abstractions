#!/usr/bin/env bash
# Rules about what our own documents assert. Sourced by check.sh under
# section "documents"; it uses that script's pass/fail/note/baseline and $RULES.
#
# Every failure this file exists for was a fact written down once and never
# re-examined: a kill condition that fired and was never carried out, a ledger
# row telling cleanup to remove a scheduled task that is not there, a finding
# aid that stopped naming the files beside it. No rule was missing. Nothing
# re-read.
#
# So each check below re-derives the fact rather than trusting the sentence, and
# each prints what it examined, because a rule that passes on an empty input set
# is how this project has already fooled itself.

# ---- a pre-declared kill that fired was carried out ----------------------
# The highest-cost failure in the ledger: logging's condition fired 2026-08, the
# verdict sat in research/, the layer kept shipping as a zero-dependency root,
# and a duplicated timestamp is what that bought. There is no second register
# here on purpose — research/INDEX.md § "Kill conditions" is the register, and a
# copy under scripts/ would be the fourth ledger this project maintains by hand.
# This reads that table and turns two of its columns into a state the gate can
# name: fired, and not carried out.
IDX="$ROOT/research/INDEX.md"
awk -F'|' '
/^\| *condition *\|/ { t=1; next }
t && $0 !~ /^\|/ { t=0 }
t && $0 !~ /^\| *-/ {
    for (i=2; i<=6; i++) gsub(/^ +| +$/, "", $i)
    if ($2=="") next
    outcome = ($5 ~ /FIRED/) ? "fired" : ($5 ~ /did not fire/) ? "survived" \
            : ($5 ~ /never run|untested/) ? "untested" : "unreadable"
    carried = ($6 ~ /^\*\*No[.,]/) ? "no" : "yes"
    print outcome "\t" carried "\t" substr($2, 1, 100)
}' "$IDX" > "$RULES/kill-rows"
NKILL=$(wc -l < "$RULES/kill-rows")
NFIRED=$(grep -c '^fired	' "$RULES/kill-rows")
grep '^fired	no	' "$RULES/kill-rows" | cut -f3 > "$RULES/kill-uncarried"
grep -c '^unreadable' "$RULES/kill-rows" > "$RULES/kill-bad"
metric kills_fired "$NFIRED" documents.sh
metric kills_uncarried "$(wc -l < "$RULES/kill-uncarried")" documents.sh
# A file that states a condition BEFORE the data, rather than discussing one,
# must be in the table, or the register has stopped learning about the tree it
# sits in — which is how the logging one was lost. The index and this file are
# excluded because both hold the phrases as data: the register cannot be its own
# subject, and neither can the grep that finds it.
xargs -d '\n' grep -alaE 'Pre-declared kill condition:|WOULD KILL THIS|kill condition, written before' \
    < "$RULES/files" 2>/dev/null \
  | grep -vE '^(research/INDEX\.md|scripts/documents\.sh)$' | sort > "$RULES/kill-decl"
: > "$RULES/kill-unlisted"
while read -r f; do
    grep -qF "${f#research/}" "$IDX" || echo "$f" >> "$RULES/kill-unlisted"
done < "$RULES/kill-decl"
note "$NKILL kill conditions read from the table in research/INDEX.md, $NFIRED fired; $(wc -l < "$RULES/kill-decl") files in this tree state one before the data and every one must be in the table"
if [ "$NKILL" -lt 2 ]; then
    fail "kill conditions — the register in research/INDEX.md did not parse; every check here would pass on nothing"
elif [ "$(cat "$RULES/kill-bad")" != 0 ]; then
    fail "kill conditions ($(cat "$RULES/kill-bad") rows whose outcome column says neither fired, did not fire, nor never run)"
elif [ -s "$RULES/kill-unlisted" ]; then
    fail "kill conditions ($(wc -l < "$RULES/kill-unlisted") files declare one and the register does not list them)"
    sed 's/^/        /' "$RULES/kill-unlisted"
elif [ -s "$RULES/kill-uncarried" ]; then
    fail "kill conditions ($(wc -l < "$RULES/kill-uncarried") FIRED AND NOT CARRIED OUT — research/INDEX.md's own \"carried out\" column says No; each needs a dated VISION.md entry, which only the owner writes)"
    sed 's/^/        /' "$RULES/kill-uncarried"
else
    pass "kill conditions — every fired one has been carried out"
fi

# ---- the footprint ledger is checkable, and checks out -------------------
# Cleanup reads FOOTPRINT.md and nothing else, so a row it cannot act on is
# worse than no row: it reads as done work. Two questions, kept apart. First,
# can a machine even tell what this row points at — that answer does not depend
# on which machine runs the gate. Second, on THIS machine, does the thing the
# row claims is there exist, and the thing it claims is gone stay gone.
FP="$ROOT/FOOTPRINT.md"
awk -F'|' 'NF==8 && $2 ~ /^ *(20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]|earlier) *$/ {
    for (i=2; i<=7; i++) { gsub(/^ +| +$/, "", $i) }
    print $2 "\t" $3 "\t" $4 "\t" $5 "\t" $7
}' "$FP" > "$RULES/fp-rows"
NROW=$(wc -l < "$RULES/fp-rows")
[ "$NROW" -gt 0 ] || fail "footprint — no ledger row parsed; every check below would pass on nothing"

kind() {  # kind <location cell>
    case "$1" in
        *scratchpad*)                    echo scratchpad ;;
        *HK[CL][UM]\\*)                  echo registry ;;
        *"Task Scheduler Library"*)      echo task ;;
        *'`'[%~/]*|*'`'[A-Za-z]:\\*|*'`.'[a-z]*|*'`'[a-z]*/*) echo path ;;
        *127.0.0.1:*|*ports\ *)          echo port ;;
        *github.com/*)                   echo url ;;
        *insteadOf*)                     echo gitconfig ;;
        *)                               echo opaque ;;
    esac
}
# A row inside a session scratchpad is outside the ledger's own scope — it says
# so itself: what gets a line is what survives deleting the scratchpad. Those
# rows are courtesy, cleanup never needs them, and holding them to the same
# standard is how a rule earns the reputation of crying wolf.
: > "$RULES/fp-opaque"; : > "$RULES/fp-probe"; NSCRATCH=0
while IFS=$'\t' read -r date machine what loc removed; do
    k="$(kind "$loc")"
    [ "$k" = scratchpad ] && NSCRATCH=$((NSCRATCH+1))
    [ "$k" = opaque ] && printf '%s · %s\n' "$date" "$what" >> "$RULES/fp-opaque"
    printf '%s\t%s\t%s\t%s\t%s\n' "$k" "$machine" "$loc" "$removed" "$date · $what" >> "$RULES/fp-probe"
done < "$RULES/fp-rows"
sort -u -o "$RULES/fp-opaque" "$RULES/fp-opaque"
baseline footprint-opaque > "$RULES/fp-opaque-known"
comm -23 "$RULES/fp-opaque" "$RULES/fp-opaque-known" > "$RULES/fp-opaque-new"
comm -13 "$RULES/fp-opaque" "$RULES/fp-opaque-known" > "$RULES/fp-opaque-gone"
metric footprint_rows "$NROW" documents.sh; metric footprint_untestable "$(wc -l < "$RULES/fp-opaque")" documents.sh
note "$NROW ledger rows, $NSCRATCH inside a scratchpad and outside the ledger's own scope, $(wc -l < "$RULES/fp-opaque") point at something no machine can test"
if [ -s "$RULES/fp-opaque-new" ]; then
    fail "footprint rows ($(wc -l < "$RULES/fp-opaque-new") new rows cleanup cannot act on)"
    sed 's/^/        /' "$RULES/fp-opaque-new"
elif [ -s "$RULES/fp-opaque-gone" ]; then
    fail "footprint rows ($(wc -l < "$RULES/fp-opaque-gone") recorded as untestable and now testable — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/fp-opaque-gone"
else
    pass "footprint rows — every untestable row is the recorded set"
fi

# A location is where the thing lives, not the thing: `~\.ollama\models` holds a
# model this project pulled and removed, and the owner's four. Removing the
# thing leaves the directory, so those rows say nothing about existence and a
# footprint-container line is how one is declared rather than argued.
baseline footprint-container > "$RULES/fp-container"
here() { case "$1" in windows|repo) return 0 ;; *) return 1 ;; esac; }
expect() {  # expect <removed cell>
    case "$1" in
        yes*|gone*|\*\*yes*|\*\*gone*)     echo absent ;;
        no|no\ *|no,*|no-*|\*\*no*)        echo present ;;
        *)                                 echo unknown ;;
    esac
}
# Ports are deliberately not probed. `no — stops with the task` on a listener
# and `no — all four are the owner's own installs` on 11434, 13305, 1234 and
# 8188 describe what the ledger left behind, not what is bound this second, and
# a rule red because he closed LM Studio is a rule switched off. The scheduled
# task behind the listener is the durable half and that is what is asked.
: > "$RULES/fp-ask"; : > "$RULES/fp-want"; NROWID=0
while IFS=$'\t' read -r k machine loc removed id; do
    NROWID=$((NROWID+1))
    here "$machine" || continue
    want="$(expect "$removed")"
    [ "$want" = unknown ] && continue
    grep -qxF "$id" "$RULES/fp-container" && continue
    printf '%s\t%s\t%s\n' "$NROWID" "$k" "$loc" >> "$RULES/fp-ask"
    printf '%s\t%s\t%s\n' "$NROWID" "$want" "$id" >> "$RULES/fp-want"
done < "$RULES/fp-probe"

# Git Bash rewrites an argument that looks like a POSIX path before a native
# binary sees it, which is how `schtasks /query` arrived as
# `schtasks C:/Program Files/Git/query`, exited non-zero, and let this rule
# accuse a correct ledger row of describing a task that was running at the time.
# Converting the one path we pass ourselves means the answer no longer depends
# on whether the shell decided to convert it for us.
winpath() { case "$1" in /[A-Za-z]/*) printf '%s:%s\n' "$(tr 'a-z' 'A-Z' <<<"${1:1:1}")" "${1:2}" ;; *) printf '%s\n' "$1" ;; esac; }
PSPROBE="$ROOT/scripts/machine-probe.ps1"
if ! command -v powershell >/dev/null 2>&1; then
    printf '  \033[33mskip\033[0m  footprint claims — %s rows name Windows locations and there is no powershell on PATH\n' "$(wc -l < "$RULES/fp-ask")"
else
    powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$(winpath "$PSPROBE")" \
        < "$RULES/fp-ask" 2>/dev/null | tr -d '\r' > "$RULES/fp-answer"
    awk -F'\t' -v want="$RULES/fp-want" '
    BEGIN { while ((getline l < want) > 0) { split(l, a, "\t"); w[a[1]]=a[2]; id[a[1]]=a[3] } }
    $1 in w { print id[$1] "\t" $3 "\t" w[$1] "\t" $2 "\t" $4 }' "$RULES/fp-answer" > "$RULES/fp-tested"
    # One name, two instances: the task removed on 2026-09-05 and the one
    # installed on 2026-09-06 are spelt `\abstraction-resident-broker` alike, and
    # no query can tell them apart. The ledger is dated, so the newest row about a
    # target is the one describing the machine now; the older rows are history and
    # testing them by name convicts a record that says so itself.
    awk -F'\t' '{ d=$1; sub(/ .*/, "", d); if (d=="earlier") d="0000-00-00"
                  if (!($2 in best) || d >= best[$2]) { best[$2]=d; line[$2]=$0 } }
        END { for (t in line) print line[t] }' "$RULES/fp-tested" | sort > "$RULES/fp-current"
    NPROBED=$(wc -l < "$RULES/fp-current")
    NSUPER=$(( $(wc -l < "$RULES/fp-tested") - NPROBED ))
    awk -F'\t' '$3!=$4 { printf "%s — ledger says %s; %s says %s\n", $1, $3, $5, $4 }' \
        "$RULES/fp-current" | sort -u > "$RULES/fp-said"
    baseline footprint-contradiction > "$RULES/fp-said-known"
    comm -23 "$RULES/fp-said" "$RULES/fp-said-known" > "$RULES/fp-said-new"
    comm -13 "$RULES/fp-said" "$RULES/fp-said-known" > "$RULES/fp-said-gone"
    metric footprint_contradicted "$(wc -l < "$RULES/fp-said")" documents.sh
    note "$NPROBED locations asked of this machine by scripts/machine-probe.ps1, which runs Get-ScheduledTask and Test-Path and prints both beside every verdict; $NSUPER named again by a later row and tested there, $(wc -l < "$RULES/fp-container") declared a container. Ports, the mac and the nas are not asked"
    if [ "$NPROBED" = 0 ]; then
        fail "footprint claims — the probe answered nothing; every claim below would pass unexamined"
    elif [ -s "$RULES/fp-said-new" ]; then
        fail "footprint claims ($(wc -l < "$RULES/fp-said-new") the machine contradicts)"
        sed 's/^/        /' "$RULES/fp-said-new"
    elif [ -s "$RULES/fp-said-gone" ]; then
        fail "footprint claims ($(wc -l < "$RULES/fp-said-gone") recorded as wrong and now agreeing — drop them from check.baseline)"
        sed 's/^/        /' "$RULES/fp-said-gone"
    else
        pass "footprint claims — the machine agrees with every row it can test"
    fi
fi

# ---- every machine we name answers to its name --------------------------
# CLAUDE.md sent three sessions at the Mac under a misspelling of its name with
# a .l0cal suffix, and that name has never resolved anywhere. Spelt with a zero
# here on purpose: the leak rule reads this file too, and it is right to.
# Skipped wholesale when nothing resolves, because a gate red on a train is a
# gate switched off.
grep -ahoE '`[A-Za-z][A-Za-z0-9-]*(-[A-Za-z0-9-]+|\.local)`' "$FP" "$ROOT/CLAUDE.md" "$ROOT/MAP.md" 2>/dev/null \
  | tr -d '`' | grep -iE 'book|imac|mini|nas|station|machine|\.local' | sort -u > "$RULES/hosts"
: > "$RULES/hosts-dead"; NLIVE=0
while read -r h; do
    if ping -n 1 -w 2000 "$h" >/dev/null 2>&1; then NLIVE=$((NLIVE+1)); else echo "$h" >> "$RULES/hosts-dead"; fi
done < "$RULES/hosts"
if [ "$NLIVE" = 0 ]; then
    printf '  \033[33mskip\033[0m  machine names — none of the %s resolves, so this machine is off the network\n' "$(wc -l < "$RULES/hosts")"
else
    baseline host-unreachable > "$RULES/hosts-known"
    comm -23 "$RULES/hosts-dead" "$RULES/hosts-known" > "$RULES/hosts-new"
    note "$(wc -l < "$RULES/hosts") machine names in the documents, $NLIVE resolve"
    if [ -s "$RULES/hosts-new" ]; then
        fail "machine names ($(wc -l < "$RULES/hosts-new") named in a document and unreachable)"
        sed 's/^/        /' "$RULES/hosts-new"
    else
        pass "machine names — every one a document sends you to answers"
    fi
fi

# ---- the rules file can count its own documents -------------------------
# "Never create a new document" is the rule, and nothing could ever fail it.
# CLAUDE.md heads the list "The six documents", the table under it has eight
# rows, and the sentence after the table says read all five. Three numbers for
# one set, in one screen, unnoticed for a day — which is the whole thesis of this
# file in miniature. Both halves are re-derived: the count against the table, and
# the table against the tree.
CL="$ROOT/CLAUDE.md"
SAID="$(sed -n 's/^## The \([a-z]*\) documents.*/\1/p' "$CL" | head -1)"
SAIDN=$(awk -v w="$SAID" 'BEGIN{split("one two three four five six seven eight nine ten",n," ")
    for(i in n) if(n[i]==w) print i}')
sed -n '/^## The [a-z]* documents/,/^## /p' "$CL" \
  | sed -n 's/^| `\([^`]*\)` .*/\1/p' | sort -u > "$RULES/doc-rows"
grep '\.md$' "$RULES/doc-rows" | sort -u > "$RULES/doc-listed"
git ls-files --cached --others --exclude-standard | grep -E '^[A-Z]+\.md$' | sort > "$RULES/doc-actual"
comm -13 "$RULES/doc-listed" "$RULES/doc-actual" > "$RULES/doc-extra"
comm -23 "$RULES/doc-listed" "$RULES/doc-actual" > "$RULES/doc-missing"
baseline top-document > "$RULES/doc-known"
comm -23 "$RULES/doc-extra" "$RULES/doc-known" > "$RULES/doc-new"
# The heading counts the table, and the table counts `feedback/` — a directory
# of one file per agent, which is a document set and not a top-level .md. Only
# the .md rows can be looked for in the tree, so the two halves count different
# things and the first of them counted the second's set for a day.
note "CLAUDE.md's table under \"The $SAID documents\" has $(wc -l < "$RULES/doc-rows") rows, $(wc -l < "$RULES/doc-listed") of them a top-level .md; $(wc -l < "$RULES/doc-actual") such documents exist in the tree"
if [ -z "$SAIDN" ] || [ "$SAIDN" != "$(wc -l < "$RULES/doc-rows")" ]; then
    fail "documents — CLAUDE.md says \"$SAID\" and its own table has $(wc -l < "$RULES/doc-rows") rows"
elif [ -s "$RULES/doc-missing" ]; then
    fail "documents ($(wc -l < "$RULES/doc-missing") in the table and not in the tree)"
    sed 's/^/        /' "$RULES/doc-missing"
elif [ -s "$RULES/doc-new" ]; then
    fail "documents ($(wc -l < "$RULES/doc-new") top-level documents nothing declares)"
    sed 's/^/        /' "$RULES/doc-new"
else
    pass "documents — the table and the tree name the same set"
fi

# ---- the finding aid names the files it sits beside ---------------------
# An index is stale in one direction that matters: content arrives and the index
# does not learn it. Comparing dates would go green the moment somebody touched
# the file, so this compares the two sets instead. The needle is the path
# relative to research/, not the basename: `RESULTS.txt` appears in this index
# thirteen times, so a new results file anywhere under research/ scored as
# indexed by coincidence, and eight directories were green on somebody else's
# entry. Going exact turned 51 of those coincidences into honest reds at once.
# go.mod and go.sum are excluded: they are what a Go directory has, not
# something a reader could ever want to be sent to.
indexable() { git ls-files research/ | grep -vE '^research/INDEX\.md$|/go\.(mod|sum)$'; }
indexable | awk -v idx="$IDX" 'BEGIN { while ((getline l < idx) > 0) s = s l "\n" }
    { f=$0; sub("^research/","",f); if (!index(s, f)) print }' | sort > "$RULES/idx-unnamed"
baseline unindexed > "$RULES/idx-known"
comm -23 "$RULES/idx-unnamed" "$RULES/idx-known" > "$RULES/idx-new"
comm -13 "$RULES/idx-unnamed" "$RULES/idx-known" > "$RULES/idx-gone"
NRES=$(indexable | wc -l)
metric research_unindexed "$(wc -l < "$RULES/idx-unnamed")" documents.sh
note "$((NRES -$(wc -l < "$RULES/idx-unnamed"))) of $NRES files git tracks under research/ have their path, relative to research/, somewhere in research/INDEX.md"
if [ -s "$RULES/idx-new" ]; then
    fail "finding aid ($(wc -l < "$RULES/idx-new") files research/INDEX.md does not name)"
    sed 's/^/        /' "$RULES/idx-new"
elif [ -s "$RULES/idx-gone" ]; then
    fail "finding aid ($(wc -l < "$RULES/idx-gone") recorded as unnamed and now named — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/idx-gone"
else
    pass "finding aid — the unnamed set is exactly the recorded one"
fi

# ---- every feedback file demands one thing, from a closed list -----------
# A loop is a repeated conclusion and the words never repeat: 990 pairs of
# feedback files median 0.164 on content overlap, the hand-found loop clusters
# 0.18-0.21, and the most distant pair inside the worst loop 0.138 — under the
# corpus median, so a ranker would sort a real loop below a random pair. What is
# comparable is not the conclusion but the action it demands, and only because
# the list of actions is closed: scripts/demands.list, which the manager owns.
# Then a repeat is uniq -c and it fires on the third occurrence.
VOCAB="$ROOT/scripts/demands.list"
grep -aoE '^[a-z][a-z-]+$' "$VOCAB" 2>/dev/null | sort -u > "$RULES/dem-vocab"
NVOCAB=$(wc -l < "$RULES/dem-vocab")
git ls-files --cached --others --exclude-standard feedback/ \
  | grep -E '^feedback/20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]-.+\.md$' | sort > "$RULES/dem-files"
NFB=$(wc -l < "$RULES/dem-files")
# One grep over the whole set, not one per file: a loop spawning four processes
# per feedback file took minutes on a loaded machine, and a gate section slow
# enough to skip is the failure this file is about.
xargs -d '\n' grep -aHoE '^DEMANDS: [a-z][a-z-]+' < "$RULES/dem-files" 2>/dev/null \
  | sed 's/:DEMANDS: /\t/' | sort > "$RULES/dem-pairs"
cut -f1 "$RULES/dem-pairs" > "$RULES/dem-named"
sort -u "$RULES/dem-named" > "$RULES/dem-once"
uniq -d "$RULES/dem-named" > "$RULES/dem-many"
comm -23 "$RULES/dem-files" "$RULES/dem-once" > "$RULES/dem-none"
cut -f2 "$RULES/dem-pairs" | sort -u > "$RULES/dem-used"
comm -23 "$RULES/dem-used" "$RULES/dem-vocab" > "$RULES/dem-unknown"
baseline demands-exempt > "$RULES/dem-known"
comm -23 "$RULES/dem-none" "$RULES/dem-known" > "$RULES/dem-new"
comm -13 "$RULES/dem-none" "$RULES/dem-known" > "$RULES/dem-gone"
cut -f2 "$RULES/dem-pairs" | sort | uniq -c | awk '$1 >= 3 { print $1 "\t" $2 }' | sort -rn > "$RULES/dem-repeat"
metric demands_declared "$(wc -l < "$RULES/dem-once")" documents.sh
metric demands_repeated "$(wc -l < "$RULES/dem-repeat")" documents.sh
note "$NVOCAB terms in scripts/demands.list; $(wc -l < "$RULES/dem-once") of $NFB dated feedback files name one, $(wc -l < "$RULES/dem-known") predate the convention and are recorded"
if [ "$NVOCAB" -lt 2 ]; then
    fail "demands — scripts/demands.list yielded $NVOCAB terms; every check here would pass on nothing"
elif [ -s "$RULES/dem-unknown" ]; then
    fail "demands ($(wc -l < "$RULES/dem-unknown") terms no vocabulary line offers — add them to scripts/demands.list or use the closest)"
    sed 's/^/        /' "$RULES/dem-unknown"
elif [ -s "$RULES/dem-many" ]; then
    fail "demands ($(wc -l < "$RULES/dem-many") files name more than one; the count only works at one per file)"
    sed 's/^/        /' "$RULES/dem-many"
elif [ -s "$RULES/dem-new" ]; then
    fail "demands ($(wc -l < "$RULES/dem-new") feedback files end without a DEMANDS line)"
    sed 's/^/        /' "$RULES/dem-new"
elif [ -s "$RULES/dem-gone" ]; then
    fail "demands ($(wc -l < "$RULES/dem-gone") recorded as predating the convention and now carrying a line — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/dem-gone"
else
    pass "demands — every feedback file names one action, and the list offers it"
fi
# Reported, never failed. Three files asking for the same thing is the manager's
# judgement to make, and a gate that goes red on it blocks the tree for a
# decision no script is entitled to take.
if [ -s "$RULES/dem-repeat" ]; then
    note "$(wc -l < "$RULES/dem-repeat") actions asked for three or more times — read these before spawning anything near them:"
    while IFS=$'\t' read -r n t; do note "    $n × $t"; done < "$RULES/dem-repeat"
fi

# ---- the belief line, on the only artefact that has one -----------------
# CLAUDE.md asks two things: that every brief opens with the belief it will
# confirm or destroy, and that every feedback file opens with the verdict. The
# first half can never be checked — a brief is a prompt and never becomes a file
# any instrument can read — and the second half is one grep. This is the second
# half, and it checks the WORD, not the thought: an agent satisfies it by
# writing "belief" and the gate cannot tell a real falsification from the token.
# It is still worth more than the convention was, which 44 of 62 files ignored
# while it read as a control.
xargs -d '\n' awk '
FNR == 1 { if (f != "" && !seen) print f; f = FILENAME; seen = 0 }
FNR <= 12 && tolower($0) ~ /belief|verdict/ { seen = 1 }
END { if (f != "" && !seen) print f }' < "$RULES/dem-files" | sort > "$RULES/bel-none"
baseline belief-silent > "$RULES/bel-known"
comm -23 "$RULES/bel-none" "$RULES/bel-known" > "$RULES/bel-new"
comm -13 "$RULES/bel-none" "$RULES/bel-known" > "$RULES/bel-gone"
metric feedback_without_verdict "$(wc -l < "$RULES/bel-none")" documents.sh
note "$((NFB - $(wc -l < "$RULES/bel-none"))) of $NFB dated feedback files say belief or verdict in their first 12 lines; $(wc -l < "$RULES/bel-known") recorded as predating the check"
if [ -s "$RULES/bel-new" ]; then
    fail "belief line ($(wc -l < "$RULES/bel-new") feedback files open without a verdict on a stated belief)"
    sed 's/^/        /' "$RULES/bel-new"
elif [ -s "$RULES/bel-gone" ]; then
    fail "belief line ($(wc -l < "$RULES/bel-gone") recorded as silent and now opening with one — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/bel-gone"
else
    pass "belief line — every feedback file the record does not excuse opens with one"
fi

# ---- the map names what a reader would look up ---------------------------
# MAP.md is the inventory, and one day after it was written it said a window
# exists in no repository here while monitor/ was a tracked Go module, listed
# scripts/ without six of its files, and had a binaries table three main
# packages short. Prose numbers cannot be re-derived and are not tried; the
# three kinds of thing a reader looks up can be — a top-level directory, a file
# under scripts/, a main package — and each must appear in backticks somewhere
# in the map, while every row of its Layers and Scripts tables must exist.
# research/ is not asked: the map delegates it to research/INDEX.md, and the
# finding-aid rule above holds that.
MAPF="$ROOT/MAP.md"
{
    grep '/' "$RULES/files" | cut -d/ -f1 | grep -v '^\.' | sed 's|$|/|'
    sed -n 's|^scripts/||p' "$RULES/files" | grep -v /
    sed -nE 's|.*/cmd/([^/]+)/[^/]+\.go$|\1|p' "$RULES/files"
} | sort -u > "$RULES/map-things"
grep -oE '`[^`]+`' "$MAPF" | tr -d '`' | sed 's|/$||' | sort -u > "$RULES/map-names"
awk 'NR==FNR { n[$0]=1; next }
{ t=$0; sub("/$","",t); if (t in n) next
  for (k in n) if (k ~ ("(^|/)" t "$") || index(k, t "/")==1) next
  print }' "$RULES/map-names" "$RULES/map-things" | sort > "$RULES/map-unnamed"
sed -n '/^## Scripts/,/^## /{s/^| `\([^`]*\)` .*/scripts\/\1/p}' "$MAPF" \
  | while read -r f; do [ -e "$ROOT/$f" ] || echo "$f"; done > "$RULES/map-missing"
sed -n '/^## Layers/,/^## /{s/^| `\([^`]*\/\)`.*/\1/p}' "$MAPF" \
  | while read -r d; do [ -d "$ROOT/$d" ] || echo "$d"; done >> "$RULES/map-missing"
baseline map-unnamed > "$RULES/map-known"
comm -23 "$RULES/map-unnamed" "$RULES/map-known" > "$RULES/map-new"
comm -13 "$RULES/map-unnamed" "$RULES/map-known" > "$RULES/map-gone"
metric map_unnamed "$(wc -l < "$RULES/map-unnamed")" documents.sh
note "$(wc -l < "$RULES/map-things") things a reader looks up — top-level directories, files under scripts/, main packages — against $(wc -l < "$RULES/map-names") names MAP.md sets in backticks; $(wc -l < "$RULES/map-unnamed") unnamed, $(wc -l < "$RULES/map-known") recorded"
if [ "$(wc -l < "$RULES/map-things")" -lt 20 ] || [ "$(wc -l < "$RULES/map-names")" -lt 20 ]; then
    fail "map — fewer than 20 things or names found; the rule would pass on nothing"
elif [ -s "$RULES/map-missing" ]; then
    fail "map ($(wc -l < "$RULES/map-missing") table rows name something that is not there)"
    sed 's/^/        /' "$RULES/map-missing"
elif [ -s "$RULES/map-new" ]; then
    fail "map ($(wc -l < "$RULES/map-new") exist and MAP.md does not name them)"
    sed 's/^/        /' "$RULES/map-new"
elif [ -s "$RULES/map-gone" ]; then
    fail "map ($(wc -l < "$RULES/map-gone") recorded as unnamed and now named — drop them from check.baseline)"
    sed 's/^/        /' "$RULES/map-gone"
else
    pass "map — MAP.md names everything a reader would look up, and every row of it exists"
fi

# ---- somebody answers ----------------------------------------------------
# Every rule above compares code with code. The first question a serious
# adopter asks is about people — where does a report go, who decides, what if
# he stops — and the answers rot the way a ledger row does: a repository is
# added and the scope list is not; private reporting is switched off and the
# only address in the policy becomes a 404; the manifest never learns the
# files exist and they are true only here. Each is re-derived. The network
# half runs under --public, because it spends GitHub's unauthenticated quota.
MANIFEST="$ROOT/scripts/split.manifest"
: > "$RULES/who-bad"
for f in SECURITY.md GOVERNANCE.md funding.json; do
    if [ ! -f "$ROOT/$f" ]; then echo "$f is missing" >> "$RULES/who-bad"; continue; fi
    grep -q $'\r' "$ROOT/$f" && echo "$f has CRLF" >> "$RULES/who-bad"
    case "$f" in *.md) [ "$(wc -l < "$ROOT/$f")" -le 60 ] || echo "$f is over 60 lines — a policy nobody reads is worse than none" >> "$RULES/who-bad" ;; esac
    grep -qE "^	file $f $f$" "$MANIFEST" \
      || echo "scripts/split.manifest publishes nothing as $f — add 'file $f $f' under the repository it belongs to" >> "$RULES/who-bad"
done
if [ -n "$PY" ] && [ -f "$ROOT/funding.json" ]; then
    "$PY" -c 'import json,sys; json.load(open(sys.argv[1], encoding="utf-8"))' "$ROOT/funding.json" 2>/dev/null \
      || echo "funding.json is not JSON" >> "$RULES/who-bad"
fi
grep -q '"email": *"[^"]*\.invalid"' "$ROOT/funding.json" 2>/dev/null \
  && echo "funding.json entity.email is a reserved placeholder — only the owner can supply an address the manifest lets through" >> "$RULES/who-bad"

# The scope paragraph names repositories, and the manifest is the list of what
# publishes. Both directions: a repository the manifest publishes under a code
# prefix that the policy does not name, and a name the policy uses that the
# manifest does not publish.
awk '$1=="repo" && $2 ~ /^(abstraction|service|addon|adopter)-/ {print $2}' "$MANIFEST" | sort -u > "$RULES/who-repos"
awk '$1=="repo" {print $2}' "$MANIFEST" | sort -u > "$RULES/who-all"
sed -n '/^## Scope/,/^## /p' "$ROOT/SECURITY.md" 2>/dev/null \
  | grep -oE '`[a-z]+-[a-z0-9-]+`' | tr -d '`' | grep -v '\*' | sort -u > "$RULES/who-named"
comm -23 "$RULES/who-repos" "$RULES/who-named" | sed 's/^/published and not in SECURITY.md scope: /' >> "$RULES/who-bad"
comm -23 "$RULES/who-named" "$RULES/who-all" | sed 's/^/named in SECURITY.md scope and not in the manifest: /' >> "$RULES/who-bad"
sort "$RULES/who-repos" "$RULES/who-named" | uniq > "$RULES/who-scope"

# Only the owner can switch private reporting on, and only GitHub can say
# whether it is. One call per repository in scope; the answer is the address
# in SECURITY.md existing or not.
: > "$RULES/who-off"; NASKED=0
if [ "${PUBLIC:-0}" = 1 ] && command -v curl >/dev/null 2>&1; then
    while read -r r; do
        NASKED=$((NASKED+1))
        body="$(curl -sS --max-time 10 -H 'Accept: application/vnd.github+json' \
            "https://api.github.com/repos/openabstractions/$r/private-vulnerability-reporting" 2>/dev/null)"
        case "$body" in *'"enabled":true'*|*'"enabled": true'*) ;; *) echo "$r" >> "$RULES/who-off" ;; esac
    done < "$RULES/who-scope"
fi
if [ -s "$RULES/pub" ]; then
    for f in SECURITY.md GOVERNANCE.md funding.json; do
        grep -qE "^abstractions	$f$" "$RULES/pub" || echo "$f is not in the published charter repository" >> "$RULES/who-bad"
    done
fi
metric answerers_named "$(wc -l < "$RULES/who-scope")" documents.sh
metric reporting_off "$(wc -l < "$RULES/who-off")" documents.sh
note "$(wc -l < "$RULES/who-named") repositories named in SECURITY.md scope against $(wc -l < "$RULES/who-repos") the manifest publishes under a code prefix; $NASKED asked GitHub whether private reporting is on$([ "${PUBLIC:-0}" = 1 ] || echo ' (none without --public)'); who holds organisation-owner rights cannot be read without a token and is asserted in GOVERNANCE.md on a date"
if [ -s "$RULES/who-bad" ]; then
    fail "somebody answers ($(wc -l < "$RULES/who-bad") facts about people the tree contradicts)"
    sed 's/^/        /' "$RULES/who-bad"
elif [ -s "$RULES/who-off" ]; then
    fail "somebody answers ($(wc -l < "$RULES/who-off") repositories in SECURITY.md scope where the report address does not exist — private vulnerability reporting is off, and only an organisation owner can turn it on)"
    sed 's/^/        /' "$RULES/who-off"
else
    pass "somebody answers — the policy names what publishes, and every report address that was asked for exists"
fi
