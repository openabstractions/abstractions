# The driver contract

A conformance driver is a program. It reads a scenario, applies each line to
your implementation, and prints what an observer would have seen. The runner
judges the printout against the rules on the contract pages.

Nothing here is specific to a language, a build system, or our source tree. If
your program obeys this page, `run.sh` can judge it. The first half of this page
binds every driver; the second half is the vocabulary of each layer, and a
driver needs only the layers it declares.

## Invocation

    <driver> --capabilities
    <driver> <workdir> <scenario-file>

`<workdir>` is an empty directory the driver owns for the run: put the store,
the partials and the delivered files there and nowhere else. The runner makes a
fresh one per scenario, so a driver may assume it is alone in it.

Exit 0 when the scenario was applied. A non-zero exit means the driver broke,
which is not the same as the implementation refusing an operation — a refusal is
a word on stdout, and the runner reports a non-zero exit as an error, never as a
conformance answer.

Write LF. A CR anywhere in the transcript is a failure: the byte comparison this
suite rests on is invisible to Windows text mode, so the runner checks.

## `--capabilities`

One line, space-separated tokens. A capability names a vocabulary of
operations, and `capabilities.list` beside `run.sh` is the closed set: which
operations need which capability, one line per capability.

| token | what the driver can do |
|---|---|
| `store` | submit records, lease them, record progress, sweep orphans |
| `transfer` | move bytes from a `file://` source to a sink and verify a digest |
| `wire` | the same over HTTP against the fixture named by `ABSTRACTION_FIXTURE` |
| `wanted` | answer a drop folder of requests |
| `identity` | say who is on the other end of a connection, and how well |
| `logging` | encode and read the part of a log line that leaves the process. **Declared, not yet judgeable here** — see below |

What a scenario needs is read from the operations in it, by that list, and
unioned with its `# requires:` line, because a declaration drifts from the file
it describes: almost every scenario declaring `transfer` also submits records
and takes leases, which is `store`. A driver that does not declare a token a
scenario needs is not asked to run it, and every rule it would have proved is
reported **unreachable** — never as a pass, and never as a failure of a rule the
driver never claimed. A driver declaring `identity` alone is judged on the
identity scenarios and told the rest are out of its reach.

An operation that `capabilities.list` places under no capability is a scenario
nobody can classify. It is reported unreachable with that reason and no driver
is asked to run it: the fix is a line in the list, never a guess at which driver
was meant.

Printing nothing is an error. An implementation that can do none of these has
nothing this suite can judge, and the runner says so and stops.

## The scenario file

Plain text. A line is an operation; a blank line and a line beginning with `#`
are not. The runner never rewrites a scenario, so the operations arrive exactly
as they are written.

Three comment forms carry meaning, and they are read by the runner, not by the
driver:

    # requires: wire
    # expect 6: state=cancelled ... [DL-R28]
    # expect 9: ok A C [JOB-I11]
    # undecided 12: no page says whether renew is refused on a lapsed lease

`# requires:` names capabilities the scenario needs beyond what its operations
imply; absent, the operations alone decide. `# undecided N:` asserts nothing
and marks a cell no page settles.

### What an expectation means

`# expect N:` is judged against step N's answer **token by token, in order**.
An answer is a verdict and a body; an expectation is read the same way, and
every token of it is exact:

| in the expectation | what must be true of the answer |
|---|---|
| a first token with no `=` | the verdict is that word, whole |
| every token after it | the body carries that token, whole, in this position |
| `...`, last | tokens this expectation does not name may sit between and after the ones it does |

**Without `...` the expectation is the whole body, in that order.** `ok A` is
satisfied by the answer `ok A` and by nothing else: `ok A B C` is refused, which
is the assertion a scenario naming an orphan list is making, and so is `ok C B
A`. Order is compared because every body this page defines has one — a record's
fields are in a fixed order, a list is sorted, a ladder is weakest first — and
because a transcript is bytes: two drivers whose transcripts differ do not do
the same thing, and a judge that forgave order would certify that they do.
There is no marker for *unordered*. A page that leaves an order open fixes it,
the way `orphans`, `sweep` and `next` say *sorted*, rather than asking the
runner to forgive it.

With `...` the named tokens must appear in the answer's order and need not be
adjacent: `done=64 err=none ...` holds against a record where five fields
precede `done`, and `ok ...` is satisfied by any answer whose verdict is `ok`.
So an expectation naming one field of a record ends in `...`, because a record
carries eleven fields and the scenario is claiming one; an expectation naming a
list does not, because there the list is the whole answer. Forgetting `...`
costs a red, never a green.

### Tokens

A body that opens with `key=value` is cut before every ` key=`, where a key is
a letter or `_` followed by letters, digits, `_`, `.` or `-`. So a value may
hold spaces — a recall reason, a checkpoint carrying a date — and may never
hold ` word=`, because that is where the next field begins. Any other body is
cut at spaces. An expectation is cut by the same rule.

### The rule tag

An expectation ends with the rules it exercises: one or more `[TAG]`, separated
by spaces, at the end of the line. **That trailing run is the only tag position.**
It is removed before the comparison and nothing else is, so a bracketed word
anywhere earlier is a token of the expectation and is compared like any other —
a tag written mid-line fails the step instead of quietly weakening it.

    # expect 15: ok A C [JOB-I11] [JOB-T2]

A tag names a rule on a contract page, and `--contracts` is where the runner
reads those pages. `contracts.list` beside `run.sh` says which page declares
which tag prefix and where each one is fetched from. A page the selected
scenarios cite and the run does not have is named with the command that fetches
it, and a tag declared on none of the pages supplied is reported with its
scenario and its step. Either way the run is **incomplete**: the expectation
claims to test a rule and this run judged it against nothing. Coverage is
counted from these tags and from nothing else — a rule named in a scenario's
prose is not a rule the scenario tests.

An expectation may carry no tag. It is judged like any other and counts towards
no rule, which is why the runner reports how many rules no expectation reaches.

## The transcript

One line per operation, in order, on stdout:

    NN <the operation line, verbatim> -> <answer>

`NN` counts operations from 01, zero-padded to two digits, and does not count
blank or comment lines. The arrow is exactly ` -> `.

An answer is a verdict — one word — and a body, whose shape each layer fixes
below. Three verdicts belong to every layer:

| word | meaning |
|---|---|
| `ok` | the operation was applied |
| `invalid` | the operation is malformed or the value is not one the contract allows |
| `unknown-op` | this driver does not know that operation |

The **wording** of an error is not a contract and must never become one. Which
class of refusal happened is exactly what a caller branches on, so that is what
is compared. Deliberately absent from every body, and they must stay absent:
error messages, timestamps, identifiers, owner strings, and anything else three
implementations cannot agree on.

## `download`: `store`, `transfer`, `wire`, `wanted`

The body is the record's fields after the verdict:

    03 claim A beta 1500 -> lease-held state=running epoch=1 held=yes recall=none want=run done=0 err=none cp=none content= crit= awake=no

The record is printed even when the operation was refused. What a refusal left
behind is the half of a refusal a caller has to live with.

### The verdict

Six more any operation may answer:

| word | meaning |
|---|---|
| `not-found` | no such record |
| `lease-held` | somebody else holds a live lease |
| `stale-epoch` | the epoch offered is not the one the record carries |
| `lease-expired` | the lease this was issued against has lapsed |
| `terminal` | the record is in a state that accepts no further change |
| `unknown-model` | the record declares a critical schema this reader cannot read. The definition spells this same refusal `unknown_schema`, and says so: `openabstractions-flat/abstraction-job/job.thrift` carries the word above beside the member |
| `refused` | refused for a reason with no word of its own |

and four that belong to one operation each, because they say something no
refusal class does:

| word | answered by | meaning |
|---|---|---|
| `transfer-failed` | `run`, `runshared` | the bytes did not arrive proven. What the record carries afterwards is the assertion |
| `changed` | `next` | the listener was handed a snapshot that differs from the last one it took |
| `quiet` | `next` | the budget passed and the snapshot did not change |
| `closed` | `next` | the subscription was closed |

### The fields

In this order, space-separated, `key=value`:

| field | value |
|---|---|
| `state` | the record's state |
| `epoch` | the lease epoch, an integer |
| `held` | `yes` when a lease is live at this instant, else `no` |
| `recall` | the recall reason, or `none` |
| `want` | the intent the record carries |
| `done` | units transferred, an integer |
| `err` | `set` when the record carries an error, else `none` |
| `cp` | the checkpoint as compact JSON, or `none`, or `unreadable` |
| `content` | the declared content-set names, comma-separated, possibly empty |
| `crit` | the subset marked critical, comma-separated, possibly empty |
| `awake` | `yes` when this process holds the machine awake for the record |

Mid-transfer progress is absent on purpose: pinning it would make a buffer size
a contract.

### The operations

`<alias>` is a name the scenario gives a record; the driver maps it to whatever
identifier it minted. `<owner>` names a lease holder; the driver remembers the
epoch it last handed that owner and issues later operations against it.

| operation | what it does |
|---|---|
| `submit <alias> [k=v ...]` | create a record. Keys below |
| `claim <alias> <owner> <ttl-ms>` | take the lease for that long |
| `renew <alias> <owner> <ttl-ms>` | extend the lease the owner holds |
| `progress <alias> <owner> <done> [checkpoint]` | record units done, and a checkpoint if one is given. The checkpoint is the rest of the line, because JSON carries spaces |
| `release <alias> <owner>` | give the lease back |
| `finish <alias> <owner> <state>` | set the record's state |
| `intent <alias> <want>` | set the intent |
| `recall <alias> <owner> <grace-ms> [reason]` | ask the holder to stop, against the epoch that owner holds |
| `hold <alias>` | keep the machine awake for the lease the record carries now |
| `state <alias>` | print the record, change nothing |
| `failure <alias>` | print the class the record's last failure recovers as: `ok class=permanent`, `ok class=retryable`, or `ok class=none`. Never the sentence — wording is not a contract |
| `orphans` | `ok -`, or `ok` and the aliases of records whose lease lapsed, sorted |
| `run <alias> <owner>` | transfer the record to its sink. `ok <fields>`, or `transfer-failed <fields>` |
| `runshared <alias> <owner>` | the same, as a supervisor over a store several machines write |
| `stage <alias> <n> [stale]` | put `n` bytes in the partial the next run would resume from. `stale` writes bytes from a *different* artifact, which is the case a bare `Range` cannot see |
| `plant <alias> content\|critical <name>` | forge a record naming a model no conforming writer would produce, so the refusal path can be reached at all |
| `credential <name> [hosts]` | hold the canary token under that name, bound to those hosts. `-` means bound to none |
| `refuse <host> <reason>` | that host is unreachable from now on |
| `allow <host>` | undo a `refuse` |
| `drop <name> [k=v ...]` | write one request into the drop folder. `text=` replaces the whole line, for a request that is not one |
| `sweep` | one pass over the drop folder. `ok` and each request as `<name>=<state>`, sorted |
| `watch <name> [budget-ms]` | open a subscription |
| `next <name>` | what the listener was handed: `changed <jobs>`, `quiet <jobs>`, `closed`, `not-found`, or `refused`. `<jobs>` is `alias=state/done` for each named record, sorted, or `-`. Never the silence between notices — clocks are not compared |
| `close <name>` | close the subscription |
| `sleep <ms>` | wait. The one place a duration is the point: a lease expiry is measured in wall time and cannot be observed any other way |

`submit` and `drop` take keys:

| key | values |
|---|---|
| `size=N` | the artifact is `N` bytes |
| `digest=good\|bad` | the correct SHA-256 of those bytes, or 64 zeros |
| `src=file` | a `file://` source the driver wrote |
| `src=missing` | a `file://` source that does not exist |
| `src=nofetcher` | a scheme no implementation is expected to have |
| `src=http:<behaviour>` | `<base>/<behaviour>/<size>` against the fixture |
| `sink=foreign` | an absolute path in the *other* platform's convention |
| `sink=abs` | an absolute path in *this* platform's convention, which a record written by another machine would carry |
| `cred=<name>` | the sources carry that credential name |
| `dest=<path>` | `drop` only: the destination the request asks for |
| `text=<line>` | `drop` only: the literal line, for a request that is malformed |

### The artifact

Byte `i` of an `n`-byte artifact is `i % 251`. Every implementation makes the
same bytes and the same digest without being handed a file, which is what lets a
scenario name a digest as `good` rather than spelling it.

### `--models`

A `store` driver also answers `--models`: every data-model name it understands,
one per line, followed by `critical-ok` or `never-critical`.

    abstraction.job/ranges@1 critical-ok
    abstraction.job/verified-prefix@1 never-critical

A record declares the models it carries and the subset a reader must understand
or refuse it, and this roster is what an `unknown-model` answer is decided
against. The runner does not ask for it: a scenario reaches the refusal through
`plant`, and a reader checking a transcript by hand reads the roster.

### `--refusals`

A `store` driver also answers `--refusals`: every verdict word above that it can
print for a refusal, one per line, sorted, without `ok` and without the four that
belong to one operation each.

    invalid
    lease-expired
    lease-held
    not-found
    refused
    stale-epoch
    terminal
    unknown-model

The words are one vocabulary with two spellings — `openabstractions-flat/abstraction-job/job.thrift` declares the
member and the `transcript` annotation beside it declares the word here — and
this roster is where the two are compared. Byte-comparing transcripts cannot do
it: it proves three drivers agree, and three drivers agreeing on a word the
definition does not declare is exactly how `unknown-model` and `unknown_schema`
drifted apart unnoticed. A driver that can derive the roster from the definition
should; one that spells it by hand is conformant and this is what checks it.

### The wire fixture

`ABSTRACTION_FIXTURE` is a base URL. The driver builds `<base>/<behaviour>/<size>`
and requests it; the behaviour is a path segment, so a new wire case costs a
fixture answer and a scenario and changes no driver in any language.

**Read the variable when the scenario starts, never once at startup.** Its value
is not the same in the two places a driver sees it. The runner sets a bare base
before it asks for `--capabilities`, so a driver may decide whether it can claim
`wire` by looking for the variable; it then sets a *different* value for each
scenario, with one more path segment on the end naming the run. That segment is
why two drivers reading the same behaviour cannot see each other, and the
fixture requires it — a request built from the startup value is one segment
short and is answered 404. Nothing else about the value is a driver's business:
join it to `<behaviour>/<size>` and ask.

When the runner could not start a fixture the variable is empty, and a driver
that claims `wire` anyway will fail scenarios it cannot reach — declare it only
when the variable has a value.

`ABSTRACTION_CRED_<NAME>` holds a credential the `credential` operation
installed, and `ABSTRACTION_CRED_<NAME>_HOSTS` the hosts it is bound to, where
`<NAME>` is the name the `credential` operation gave it, upper-cased. The value
is fixed, because the fixture's `gated` behaviour is what checks it:

    hf_thisMustNeverAppearOnDisk_EXAMPLE

sent as `Authorization: Bearer <that>`. It carries `EXAMPLE` for the reason the
documented example keys of every cloud vendor do — a token that cannot
authenticate anything is safe to write down, and this one has to be written down
or no two drivers can agree on what a bound credential looks like on the wire.
What arrives on the fixture's wire is what your runner chose to send, which is
the whole question the [confused-deputy](https://en.wikipedia.org/wiki/Confused_deputy_problem)
scenarios ask: whether a downloader hands a credential it holds to a host that
did not earn it.

## `identity`

The body carries proof levels and attribute names and never a value: a SID, a
uid and an image path are one machine's, so no scenario could spell them on
another. An attribute is one of `user`, `process`, `path`, `package`, `code`; a
proof is one rung of the ladder the contract page fixes, `none` to `signed`.

### The verdict

| word | meaning |
|---|---|
| `unproven` | the answer exists and falls short of what was asked; the body names what fell short |
| `no-answer` | there is no identity to give, and the body says why in one word |
| `differs` | two answers that should agree do not; the body names where |
| `unimplemented` | this platform has no implementation, and the driver did not fall back to a weaker one |

### The operations

`<alias>` names a listener or a benched attribute; `<name>` files a resolved
peer so later operations can ask about it.

| operation | what it does |
|---|---|
| `ladder` | `ok` and the proof names, weakest first |
| `bench <alias> <proof>` | build an attribute at that proof and nothing else, so `AtLeast` is judgeable on any machine. `invalid` for a proof this package never produces |
| `value <alias> <min>` | `ok proof=<p>`, or `unproven want=<p> got=<p>` |
| `ceiling` | `ok` and the platform's best per attribute, `<attr>=<proof>`, then `bindable=yes\|no` and `stronger=<transport>\|none` |
| `caneve [attr=proof ...]` | `ok`, or `unproven` and each attribute out of reach |
| `serve <alias>` | open a listener the driver owns |
| `speak <alias> <kind> [say=<text>]` | connect a peer of that kind: `self`, or `untrusted` for one whose code signature the platform refused. `say=` is an identity the peer writes into its first frame |
| `identify <alias> <name> [served\|listening\|client]` | resolve at that end of the connection, filing the answer under `<name>`. `ok` and `<attr>=<proof>` per attribute, or `no-answer not-server-end` |
| `proof <name> <attr>` | `ok proof=<p>` |
| `trusted <name>` | `ok trusted=yes\|no` |
| `check <name> [attr=proof ...]` | `ok`, or `unproven` and each attribute short, in the order above |
| `same <name> <name>` | `ok` when two answers agree on user, path, package and code, value and proof; `differs` and the first attribute that does not. Never the process: two connections are two processes |
| `traces <name>` | `ok` when no text the peer wrote appears anywhere in the rendered identity |
| `claimed <name>` | `ok -` when no attribute sits at `claimed`, else the attributes |

A peer kind a scenario does not use is not listed; one is added with the
scenario that needs it.

## `logging`: declared, and half joined

Of the four things below that a layer arrives with, `logging` has two:
`capabilities.list` names its capability and its operations, and
`contracts.list` names the page its rules live on. It has neither a section on
this page fixing its verdicts, its body and its operations, nor its scenarios
published beside the others.

So a logging driver cannot be written from these pages, and no published
scenario asks for one. Declaring `logging` buys nothing today. It is named here
rather than left out because a reader who meets it in `capabilities.list` is
owed the reason it does nothing, and because a capability nothing published can
exercise is a gap, not a pass.

## Joining with a third layer

A layer arrives with four things and changes `run.sh` in none of them: a line
in `capabilities.list` naming its capability and its operations; a section on
this page fixing its verdicts, its body and its operations; a line in
`contracts.list` for its tag prefix and page; and its scenarios published beside
the others. Its answers carry nothing a scenario could not spell on another
machine, and every body it defines has an order.

## What this contract does not settle

The store's on-disk format, the identifier scheme, the transport, the concurrency
model, the language. Two drivers can obey every line above and share no code, and
that is the point: a transcript that matches proves the implementations *do* the
same thing, and a transcript produced from a shared implementation proves only
that it agrees with itself.
