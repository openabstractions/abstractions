# Logging write/read quickstart

This example writes one structured event through the facade, pages through the
service-owned history, and prints the same event after it is retained. Each run
uses a unique `event_id`, so an existing history cannot satisfy the readback.

The source example uses an explicit isolated-development endpoint. That endpoint
is unverified transport. Local IPC still supplies the application's OS-backed
program and account identity to the runtime. Installed applications should use
`facade.Discover()` and registered installation trust.

## Windows PowerShell

Requirements: Go 1.26 or newer and the matching OpenAbstractions 0.2.0 source
and dependencies. This example targets the upcoming release. The published
v0.1.7 packages have a different API.
Run these commands at the checkout root. Build each executable once because the
history permission names the application's exact executable path.

```powershell
New-Item -ItemType Directory -Force .\out | Out-Null
go -C .\serve build -o ..\out\openabstractions.exe .

go -C .\examples\logging-quickstart build -o ..\..\out\logging-quickstart.exe .

$state = Join-Path (Resolve-Path .) '.build\logging-quickstart-state'
New-Item -ItemType Directory -Force $state | Out-Null
.\out\openabstractions.exe serve runtime --isolated logging-quickstart --state-dir $state
```

Keep the runtime running. It prints `ABSTRACTION_RUNTIME_ENDPOINT=...`. In a
second PowerShell window at the checkout root, copy the value after `=`:

```powershell
$env:ABSTRACTION_RUNTIME_ENDPOINT = '<printed endpoint>'
$program = & .\out\logging-quickstart.exe --program

.\out\openabstractions.exe rights grant `
  --endpoint $env:ABSTRACTION_RUNTIME_ENDPOINT `
  --program $program `
  --action abstraction.logging/history.read `
  --resource abstraction.logging/history `
  --why 'logging quickstart'

.\out\logging-quickstart.exe
```

The useful result is:

```text
rights grant: applied
wrote: worker started (quickstart-...)
read back: worker started (component=quickstart)
```

The isolated runtime stores its log at
`.build\logging-quickstart-state\logging\records.jsonl`.

## POSIX shell

The same source path is supported on Linux. Current macOS program-proof limits
can prevent the exact history grant.

```sh
mkdir -p out .build/logging-quickstart-state
go -C ./serve build -o ../out/openabstractions .
go -C ./examples/logging-quickstart build -o ../../out/logging-quickstart .

./out/openabstractions serve runtime \
  --isolated logging-quickstart \
  --state-dir "$(pwd)/.build/logging-quickstart-state"
```

In a second shell, copy the printed endpoint:

```sh
export ABSTRACTION_RUNTIME_ENDPOINT='<printed endpoint>'
PROGRAM="$(./out/logging-quickstart --program)"

./out/openabstractions rights grant \
  --endpoint "$ABSTRACTION_RUNTIME_ENDPOINT" \
  --program "$PROGRAM" \
  --action abstraction.logging/history.read \
  --resource abstraction.logging/history \
  --why 'logging quickstart'

./out/logging-quickstart
```

Log writes are open in the shipped runtime composition. Retained history is
gated by the exact action `abstraction.logging/history.read` on the exact
resource `abstraction.logging/history`. The example uses a five-second bound,
walks every history page, and continues from the end cursor while it waits for
the one-way write to be retained.
