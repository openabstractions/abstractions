# Remote job service fixture

From a checkout with the configured Go workspace, run:

```sh
go test -race -count=1 -v conformance/clients/remote_jobs/fixture_test.go
```

The fixture creates an isolated certificate authority and three client keys,
listens only on loopback, and gives the real acceptance provider its own temporary
root. Applications use generated acceptance, operation and inventory clients
through the shared remote TLS FrameClient. Host policy maps certificate keys to
stable caller namespaces; identical certificate display names confer no scope.

The first Submit response is dropped after dispatch, then the TLS host/provider
is restarted at the same address. Reconciliation recovers the same logical owner
and request identity. Inventory proves one operation, conflicting arguments refuse,
a second key gets a separate namespace, and an unmapped key is forbidden.
Method denial prevents admission, negative reconciliation seals and cancellation.
After the explicit method policy permits a previously denied identity it can be
admitted. Revoking the key mapping denies subsequent reads without destroying the
original receipt. All application assertions use service responses.

This uses admission-only jobs. It does not exercise an execution engine or result
content, installation, facade remote registration, non-Go clients, or OS process
identity over TLS. Certificate identity is remote trust evidence. Native execution
and capability authorization remain separately configured host responsibilities.
