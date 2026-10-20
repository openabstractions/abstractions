# Close-before-dispatch fault helper

The dependency-free Go helper uses an owned `net.Pipe` connection with IPC-F1's
four-byte big-endian length and complete payload. It reads one selected request
(default `Submit`) completely, then closes the connection before the service
callback. `Fired` records the complete payload byte count. Truncated requests
produce errors without firing or dispatching. Other frames pass through.

The caller's reply read receives `io.EOF`. IPC-E1 treats this end of stream as an
unknown transport outcome: the caller has no closing marker or refusal proving
nondispatch. The fixture observes zero service dispatches independently. It
never retries, sends a second request or grants retry authority; IPC-I2 governs
caller recovery, including keyed-call reconciliation.

    transport := closebeforedispatch.New(service, "Submit")
    _, err := transport.ExchangeFrame(request)
    fired, received := transport.Fired()

The frame limit is one MiB and each owned connection has a five-second fixture
deadline. No external listener, service or network is opened. This response-call
fixture complements `../lostreply`, where the callback completes and its reply
is lost. It proves this local fault boundary, not the AB-1 six-direction journey
or any installed binding's complete recovery behavior.

Run from the workspace root:

    go -C conformance/faults/closebeforedispatch/go test -count=1 ./...
