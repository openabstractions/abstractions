# Two peers built from the definition, one written by hand, and every operation
# between them.
#
#   powershell -File test/wire.ps1 -Scratch <dir> [-Definition <file>]
#
# Needs go. Python is optional and a missing one is reported UNPROVEN, never
# skipped. Writes only under <Scratch>.

param(
  [Parameter(Mandatory = $true)][string]$Scratch,
  [string]$Definition
)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here
$repo = Split-Path -Parent $root
if (-not $Definition) { $Definition = "$repo\job\job.thrift" }

$env:GOCACHE = "$Scratch\gocache"
$env:GOMODCACHE = "$Scratch\gomodcache"
$env:GOTMPDIR = "$Scratch\gotmp"
$env:GOWORK = "off"
$env:GOFLAGS = "-mod=mod"
# The stores are emptied first because every transcript counts what a sweep
# returns, so a second run against a first run's records disagrees with itself.
foreach ($d in "out", "peer", "run") {
  Remove-Item -Recurse -Force "$Scratch\$d" -ErrorAction SilentlyContinue
}
foreach ($d in "gocache", "gomodcache", "gotmp", "out", "peer", "run") {
  New-Item -ItemType Directory -Force "$Scratch\$d" | Out-Null
}

function Slashes($p) { $p.Replace([char]92, [char]47) }

Push-Location "$root\gen"
& go run . $Definition "$Scratch\out" go python | Out-Null
Pop-Location
[System.IO.File]::WriteAllText("$Scratch\out\go\go.mod", "module idl/out/go`n`ngo 1.26`n")

Copy-Item "$here\wire\*.go" "$Scratch\peer\" -Force
$mod = [System.IO.File]::ReadAllText("$here\wire\go.mod.tmpl")
$mod = $mod.Replace("@REPO@", (Slashes $repo)).Replace("@OUT@", (Slashes "$Scratch\out"))
[System.IO.File]::WriteAllText("$Scratch\peer\go.mod", $mod)

$other = @()
if ($null -ne (Get-Command "py" -ErrorAction SilentlyContinue)) {
  $other = @("py", "-3", "$here\wire\peer.py", "$Scratch\out\py")
} else {
  Write-Host "python  UNPROVEN: no interpreter on this machine"
}

Push-Location "$Scratch\peer"
& go run . "$Scratch\run" @other
Pop-Location
