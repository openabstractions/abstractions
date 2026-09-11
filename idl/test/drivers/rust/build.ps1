# Generate the Rust record codec from the definition and build the conformance
# driver on it. Writes only under <Scratch>; leaves <Scratch>\replay.exe.
#
#   powershell -File build.ps1 -Scratch <dir> [-Definition <file>]
#   sh conformance/run.sh --scenarios conformance/scenarios --contracts <pages> --no-fixture -- <Scratch>/replay.exe

param(
  [Parameter(Mandatory = $true)][string]$Scratch,
  [string]$Definition
)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$idl = (Resolve-Path "$here\..\..\..").Path
if (-not $Definition) {
  $Definition = "$idl\testdata\job.thrift"
  if (-not (Test-Path $Definition)) { $Definition = "$idl\..\openabstractions-flat\abstraction-job\job.thrift" }
}

$env:GOCACHE = "$Scratch\gocache"
$env:GOMODCACHE = "$Scratch\gomodcache"
$env:GOTMPDIR = "$Scratch\gotmp"
$env:GOWORK = "off"
$env:GOFLAGS = "-mod=mod"
foreach ($d in "gocache", "gomodcache", "gotmp", "build") {
  New-Item -ItemType Directory -Force "$Scratch\$d" | Out-Null
}

Remove-Item -Recurse -Force "$Scratch\out" -ErrorAction SilentlyContinue
Push-Location "$idl\gen"
& go run . $Definition "$Scratch\out"
Pop-Location

Copy-Item "$Scratch\out\rs\rec.rs" "$Scratch\build\rec.rs" -Force
Copy-Item "$here\replay.rs" "$Scratch\build\replay.rs" -Force
Remove-Item "$Scratch\replay.exe" -ErrorAction SilentlyContinue
& rustc --edition 2021 -O -o "$Scratch\replay.exe" "$Scratch\build\replay.rs"
if (Test-Path "$Scratch\replay.exe") { Write-Host "built $Scratch\replay.exe" } else { throw "rustc produced nothing" }
