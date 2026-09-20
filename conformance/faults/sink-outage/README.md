# Sink-outage fault

One fixture, `sink-outage.json`, run by every language's logging handler test
against an in-process fake sink. It states `LOG-S12` and `LOG-S13` of
`openabstractions-flat/abstraction-logging/CONTRACT.md`: a bound sink whose
service disappears keeps its records queued, drops the newest when full, retries
and rebinds, and on recovery delivers one gap record ahead of the queued records.

## The fake sink

- While the outage is on, every write fails with `outage.write_error`, and every
  rebind fails with the language's typed resolution error carrying
  `outage.rebind_error.status`.
- While it is off, writes are recorded in order and rebinds succeed.
- After a `hold` step, the next successful write of the held message waits
  inside the sink until the `release` step, then is recorded.
- The handler is built with `options`: its queue capacity, its first and largest
  backoff in milliseconds, and the program named in its hop 0 claim.

## Steps

| step | meaning |
| --- | --- |
| `log` | hand each message to the handler at INFO, in order |
| `outage` | turn the outage on (`true`) or off (`false`) |
| `await settled` | wait until nothing is queued, the handler is not failing, and every accepted record is written, failed or abandoned |
| `await failing` | wait until the handler is failing and its failure transition has been reported |
| `await retry` | wait until the fake sink has seen at least two rebind attempts |
| `hold` | hold the next successful write of this message |
| `await held` | wait until the held write has begun |
| `release` | let the held write finish |
| `counts` | the handler's counts equal these now |
| `close` | close the handler with a 5 s deadline; its counts are the final counts |

## Expectations

Each language runs the steps twice.

- **With the three callbacks.** The final counts, `last_error`, the delivered
  messages, the gap record, the transitions in order, and the failure's
  resolution status are asserted.
- **With no callbacks.** The same counts, delivered messages and gap record are
  asserted, and the language's error stream holds exactly the `reporter` lines.

The gap record is the third delivered record. The steps hold its delivery and
log one more record, which the full queue drops. That drop is reported by a
second gap record, delivered once the queue has drained. Every language asserts
the `dropped` attribute of each delivered gap record (`gaps_dropped`), that
they sum to the final `dropped` count, that each `since` and `until` is a
fixed-width instant with `since` not after `until`, and that a later gap
record's `since` is the previous one's `until` (`LOG-S13`).

## Where it runs

| language | test |
| --- | --- |
| Go | `abstraction-logging/go/sink_outage_test.go` `TestSinkOutageFixture` |
| Python | `abstraction-logging/py/test_sink_outage.py` |
| C++ | `abstraction-logging/cpp/test/sink_outage.cpp` (ctest `logging_sink_outage`) |
| Rust | `abstraction-facade/rust-logging` `sink_outage_fixture` |
| JavaScript | `abstraction-facade/javascript/test/logging.test.mjs` "sink-outage fixture" |
