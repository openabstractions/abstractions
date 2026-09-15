# Lost-reply fault helper

One fault, three bindings. The first request frame naming a chosen method (default
`Submit`) reaches the service unchanged. The service commits and answers, the
complete reply is received, and the client is told the connection was lost. Nothing
is invented and no extra request is sent. The fault fires once. A correct client
treats the outcome as unknown and reconciles the identity it retained before
sending.

| Binding | Client fixtures | Mechanism |
| --- | --- | --- |
| `lostreply.c` | C++ facade, Python over `libabstraction_ipc.so`, Rust over the C ABI, Node addon | `LD_PRELOAD` interposes libc `send`/`recv` in the unchanged application process (Linux) |
| `go/` | Go generated clients and `listen.FrameClient` users | Wraps any `ExchangeFrame([]byte) ([]byte, error)` transport; Go does its own socket calls |
| `python/lostreply.py` | Python fixtures that inject a transport; subprocess environment for native preload | Wraps `exchange_frame`; builds the `LD_PRELOAD` environment |

## Native processes (`lostreply.c`)

    gcc -shared -fPIC -O2 -o lostreply.so conformance/faults/lostreply/lostreply.c -ldl
    LD_PRELOAD=$PWD/lostreply.so OA_LOST_REPLY_METHOD=Submit \
        OA_LOST_REPLY_MARKER=/tmp/lost.txt ./application ...

The marker file records the discarded header and body byte counts. A nonzero body
count proves the service answered. The shim matches a JSON `"method"` member, so
argument values that merely contain the method name do not trigger it. The
interrupted call reports a connection reset; the C++ and Python bindings surface
it as a frame error.

## Go (`go/`)

The directory is the dependency-free module
`github.com/openabstractions/abstractions/conformance/faults/lostreply/go`, and the
root `go.work` uses it. A fixture module in that workspace imports it by path;
a fixture outside the repository tree adds its own module to a copy of `go.work`,
as `conformance/clients/linux_lifecycle/drive.py` does:

    t := lostreply.New(listen.FrameClient{Endpoint: ep}, "Submit")
    client := acceptance.NewRecoverableAcceptanceClient(t)
    _, err := client.Submit(submission) // errors.Is(err, lostreply.ErrLostReply)
    fired, discarded := t.Fired()        // true, reply byte count

`WriteFrame` passes through when the wrapped transport supports it. Test with
`cd conformance/faults/lostreply/go && GOWORK=off go test ./...`.

## Python (`python/lostreply.py`)

    from lostreply import LostReply, preload_environment
    client._acceptance = job.RecoverableAcceptanceClient(LostReply(client._transport))
    env = preload_environment("/path/lostreply.so", "/tmp/lost.txt")  # subprocess.run(..., env=env)

`LostReply` raises `abstraction.ipc.FrameError(DISCONNECTED)` when that package is
importable and `ConnectionResetError` otherwise. It forwards `call_scope` and
`write_frame`. Test with `python3 conformance/faults/lostreply/python/test_lostreply.py`.

## Evidence

`conformance/clients/linux_lifecycle` loads `lostreply.c` into the unchanged
installed C++ application and into the installed Python job client, and wraps the
Go job client's transport with `go/`. All three run against the installed Linux
runtime; the report is
`feedback/linux-complete-lifecycle.md`.
