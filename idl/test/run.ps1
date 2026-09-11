# One definition, every backend, one byte comparison.
#
#   powershell -File test/run.ps1 -Scratch <dir> [-Definition <file>] [-Reference <file>]
#
# Needs go. Every other toolchain is optional and a missing one is reported
# UNPROVEN, never skipped. Writes only under <Scratch>.

param(
  [Parameter(Mandatory = $true)][string]$Scratch,
  [string]$Definition,
  [string]$Reference,
  [string]$Terminal
)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here
$jobData = "$root\testdata"
if (-not (Test-Path "$jobData\job.thrift")) { $jobData = "$root\..\openabstractions-flat\abstraction-job" }
if (-not $Definition) { $Definition = "$jobData\job.thrift" }
$records = $jobData
if (Test-Path "$jobData\testdata") { $records = "$jobData\testdata" }
if (-not $Reference) { $Reference = "$records\ranges-record.json" }
if (-not $Terminal) { $Terminal = "$records\terminal-record.json" }
$corpus = "$here\corpus"

$env:GOCACHE = "$Scratch\gocache"
$env:GOMODCACHE = "$Scratch\gomodcache"
$env:GOTMPDIR = "$Scratch\gotmp"
$env:GOWORK = "off"
$env:GOFLAGS = "-mod=mod"
$env:CARGO_HOME = "$Scratch\cargo"
foreach ($d in "gocache", "gomodcache", "gotmp", "cargo") {
  New-Item -ItemType Directory -Force "$Scratch\$d" | Out-Null
}

$script:results = @()

function Bytes($p) { [System.IO.File]::ReadAllBytes($p) }

function Same($a, $b) {
  if ($a.Length -ne $b.Length) { return $false }
  for ($i = 0; $i -lt $a.Length; $i++) { if ($a[$i] -ne $b[$i]) { return $false } }
  return $true
}

function Have($exe) { $null -ne (Get-Command $exe -ErrorAction SilentlyContinue) }

function Generate($def, $out) {
  Remove-Item -Recurse -Force $out -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $out | Out-Null
  Push-Location "$root\gen"
  & go run . $def $out | Out-Null
  Pop-Location
  [System.IO.File]::WriteAllText("$out\go\go.mod", "module idl/out/go`n`ngo 1.26`n")
}

function RunDrivers($out, $res) {
  Remove-Item -Recurse -Force $res -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $res | Out-Null
  $script:ran = @()

  New-Item -ItemType Directory -Force "$Scratch\d-go" | Out-Null
  Copy-Item "$here\drivers\go\main.go" "$Scratch\d-go\" -Force
  [System.IO.File]::WriteAllText("$Scratch\d-go\go.mod",
    "module d`n`ngo 1.26`n`nrequire idl/out/go v0.0.0`n`nreplace idl/out/go => $($out -replace '\\','/')/go`n")
  Push-Location "$Scratch\d-go"
  & go run . $res $corpus | Out-Null
  Pop-Location
  $script:ran += "go"

  if (Have "py") {
    & py -3 "$here\drivers\py\run.py" "$out\py" $res $corpus | Out-Null
    $script:ran += "py"
  } else { $script:results += "python  UNPROVEN: no interpreter on this machine" }

  $vc = if ($env:VCVARS) { $env:VCVARS }
        else { "C:\Program Files\Microsoft Visual Studio\18\Community\VC\Auxiliary\Build\vcvars64.bat" }
  if (Test-Path $vc) {
    Set-Content -Path "$Scratch\build.bat" -Encoding ascii -Value @"
@echo off
call "$vc" >nul 2>&1
cd /d "$Scratch"
cl /nologo /std:c++20 /EHsc /utf-8 /I "$out\cpp" "$here\drivers\cpp\run.cpp" /Fe:d_cpp.exe
"@
    Remove-Item "$Scratch\d_cpp.exe" -ErrorAction SilentlyContinue
    & cmd /c "$Scratch\build.bat" > "$Scratch\cpp-build.log" 2>&1
    if (Test-Path "$Scratch\d_cpp.exe") {
      & "$Scratch\d_cpp.exe" $res $corpus | Out-Null
      $script:ran += "cpp"
    } else { $script:results += "cpp     UNPROVEN: see $Scratch\cpp-build.log" }
  } else { $script:results += "cpp     UNPROVEN: no MSVC on this machine" }

  if (Have "node") {
    Copy-Item "$out\js\rec.mjs" "$Scratch\rec.mjs" -Force
    Copy-Item "$here\drivers\js\run.mjs" "$Scratch\run.mjs" -Force
    & node "$Scratch\run.mjs" $res $corpus | Out-Null
    $script:ran += "js"
  } else { $script:results += "js      UNPROVEN: no node on this machine" }

  if ((Have "rustc") -and (Test-Path "$out\rs\rec.rs")) {
    Copy-Item "$out\rs\rec.rs" "$Scratch\rec.rs" -Force
    Copy-Item "$here\drivers\rs\main.rs" "$Scratch\main.rs" -Force
    Remove-Item "$Scratch\main.exe" -ErrorAction SilentlyContinue
    & cmd /c "rustc --edition 2021 -O --out-dir ""$Scratch"" ""$Scratch\main.rs"" > ""$Scratch\rs-build.log"" 2>&1"
    if (Test-Path "$Scratch\main.exe") {
      & "$Scratch\main.exe" $res $corpus | Out-Null
      $script:ran += "rs"
    } else { $script:results += "rust    UNPROVEN: see $Scratch\rs-build.log" }
  } else { $script:results += "rust    UNPROVEN: no rustc on this machine" }
}

function CompareRefusals($ran, $res) {
  $expected = @{}
  foreach ($f in Get-ChildItem "$corpus\*.json") {
    $word = ($f.BaseName -split '\.')[0]
    $expected[$f.BaseName] = if ($word -eq "accept") { "ok" } else { $word }
  }
  $first = $script:ran[0]
  $rows = @{}
  foreach ($n in $ran) {
    $p = "$res\$n-corpus.txt"
    if (-not (Test-Path $p)) { Write-Host ("{0,-4} corpus: NOT PRODUCED" -f $n); return }
    $rows[$n] = Get-Content $p
  }
  $wrong = 0
  foreach ($line in $rows[$first]) {
    $p = $line -split "`t"
    if ($expected[$p[0]] -ne $p[1]) {
      Write-Host ("  {0}: expected {1}, {2} said {3}" -f $p[0], $expected[$p[0]], $first, $p[1])
      $wrong++
    }
  }
  $disagreed = 0
  foreach ($n in $ran) {
    if (Compare-Object $rows[$first] $rows[$n]) {
      Write-Host ("  {0,-4} DISAGREES with {1}" -f $n, $first)
      Compare-Object $rows[$first] $rows[$n] | ForEach-Object { Write-Host ("      " + $_.InputObject) }
      $disagreed++
    }
  }
  $words = ($expected.Values | Where-Object { $_ -ne "ok" } | Sort-Object -Unique).Count
  $refused = ($expected.Values | Where-Object { $_ -ne "ok" }).Count
  Write-Host ("  {0} inputs, {1} refused, {2} refusal words, {3} languages" -f
    $expected.Count, $refused, $words, $ran.Count)
  if ($wrong -eq 0 -and $disagreed -eq 0) {
    Write-Host "  every implementation refused every input with the same word at the same byte"
  }
}

function CompareRoundTrip($ran, $res, $record) {
  foreach ($n in $ran) {
    $a = "$res\$n-$record.json"
    $b = "$res\$n-rt-$record.json"
    if (-not (Test-Path $b)) { Write-Host ("  {0,-4} {1}: NO ROUND TRIP" -f $n, $record); continue }
    $verdict = if (Same (Bytes $a) (Bytes $b)) { "identical" } else { "DIFFERS" }
    Write-Host ("  {0,-4} decode then encode  {1}" -f $n, $verdict)
  }
}

function CompareAll($ran, $res, $record) {
  $data = @{}
  foreach ($n in $ran) {
    $p = "$res\$n-$record.json"
    if (Test-Path $p) { $data[$n] = Bytes $p }
    else { Write-Host ("{0,-8} {1}: NOT PRODUCED" -f $n, $record); return $null }
  }
  $first = $ran[0]
  foreach ($n in $ran) {
    $verdict = if (Same $data[$first] $data[$n]) { "identical" } else { "DIFFERS" }
    Write-Host ("  {0,-4} {1,6} bytes  {2}" -f $n, $data[$n].Length, $verdict)
  }
  return $data[$first]
}

Write-Host "=== escape = minimal, as the definition declares ==="
Generate $Definition "$Scratch\out"
RunDrivers "$Scratch\out" "$Scratch\res"
$ran = $script:ran
Write-Host "languages: $($ran -join ' ')"
Write-Host "awkward record:"
$awk = CompareAll $ran "$Scratch\res" "awkward"
Write-Host "ranges record:"
$rng = CompareAll $ran "$Scratch\res" "ranges"
Write-Host "terminal record:"
$trm = CompareAll $ran "$Scratch\res" "terminal"

Write-Host "round trip:"
CompareRoundTrip $ran "$Scratch\res" "awkward"
CompareRoundTrip $ran "$Scratch\res" "ranges"
CompareRoundTrip $ran "$Scratch\res" "terminal"

Write-Host "refusal corpus:"
CompareRefusals $ran "$Scratch\res"

Write-Host ""
# The references were written by hand from the contract page, never by a
# backend, because five generated encoders agreeing proves only that they share
# a parent.
foreach ($pair in @(@($Reference, $rng), @($Terminal, $trm))) {
  $ref, $got = $pair
  if (-not (Test-Path $ref)) { Write-Host "reference: UNPROVEN, $ref is not on this machine"; continue }
  if ($null -ne $got -and (Same (Bytes $ref) $got)) { Write-Host "reference: reproduces $ref byte for byte" }
  else { Write-Host "reference: DIFFERS from $ref" }
}

Write-Host ""
Write-Host "=== escape = ascii, the same definition with one line changed ==="
$alt = "$Scratch\ascii.thrift"
$ascii = [System.IO.File]::ReadAllText($Definition) -replace '(escape\s+=\s+)"minimal"', '$1"ascii"'
[System.IO.File]::WriteAllText($alt, $ascii)
Generate $alt "$Scratch\out-ascii"
RunDrivers "$Scratch\out-ascii" "$Scratch\res-ascii"
$ran2 = $script:ran
Write-Host "awkward record:"
$awk2 = CompareAll $ran2 "$Scratch\res-ascii" "awkward"
if ($null -ne $awk -and $null -ne $awk2) {
  if (Same $awk $awk2) { Write-Host "policy: NOT LOAD-BEARING, the declaration changed nothing" }
  else { Write-Host "policy: every backend moved together, $($awk.Length) bytes -> $($awk2.Length) bytes" }
}

foreach ($r in $script:results) { Write-Host $r }
