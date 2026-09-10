# The repeated record and the string map, in five languages, byte for byte.
#
#   powershell -File test/repeated/run.ps1 -Scratch <dir>
#
# test/run.ps1 proves the profile on job/job.thrift, which has neither shape.
# This proves the two the generator learned on 2026-09-08, on the smallest
# definition that reaches every rule they touch. Needs go. Every other
# toolchain is optional and a missing one is reported UNPROVEN, never skipped.
# Writes only under <Scratch>.

param([Parameter(Mandatory = $true)][string]$Scratch)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$gen = "$here\..\..\gen"
$definition = "$here\repeated.thrift"
$corpus = "$here\corpus"

$env:GOCACHE = "$Scratch\gocache"
$env:GOMODCACHE = "$Scratch\gomodcache"
$env:GOTMPDIR = "$Scratch\gotmp"
$env:GOWORK = "off"
$env:GOFLAGS = "-mod=mod"
foreach ($d in "gocache", "gomodcache", "gotmp") {
  New-Item -ItemType Directory -Force "$Scratch\$d" | Out-Null
}

function Bytes($p) { [System.IO.File]::ReadAllBytes($p) }
function Have($exe) { $null -ne (Get-Command $exe -ErrorAction SilentlyContinue) }

function Same($a, $b) {
  if ($a.Length -ne $b.Length) { return $false }
  for ($i = 0; $i -lt $a.Length; $i++) { if ($a[$i] -ne $b[$i]) { return $false } }
  return $true
}

$out = "$Scratch\out"
$res = "$Scratch\res"
foreach ($d in $out, $res) { Remove-Item -Recurse -Force $d -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Force $out, $res | Out-Null

Push-Location $gen
& go run . $definition $out | Out-Null
Pop-Location
[System.IO.File]::WriteAllText("$out\go\go.mod", "module idl/out/go`n`ngo 1.26`n")

$ran = @()
$unproven = @()

New-Item -ItemType Directory -Force "$Scratch\d-go" | Out-Null
Copy-Item "$here\drivers\go\main.go" "$Scratch\d-go\" -Force
[System.IO.File]::WriteAllText("$Scratch\d-go\go.mod",
  "module d`n`ngo 1.26`n`nrequire idl/out/go v0.0.0`n`nreplace idl/out/go => $($out -replace '\\','/')/go`n")
Push-Location "$Scratch\d-go"
& go run . $res $corpus | Out-Null
Pop-Location
$ran += "go"

if (Have "py") {
  & py -3 "$here\drivers\py\run.py" "$out\py" $res $corpus | Out-Null
  $ran += "py"
} else { $unproven += "python  UNPROVEN: no interpreter on this machine" }

if (Have "node") {
  Copy-Item "$out\js\rec.mjs" "$Scratch\rec.mjs" -Force
  Copy-Item "$here\drivers\js\run.mjs" "$Scratch\run.mjs" -Force
  & node "$Scratch\run.mjs" $res $corpus | Out-Null
  $ran += "js"
} else { $unproven += "js      UNPROVEN: no node on this machine" }

if (Have "rustc") {
  New-Item -ItemType Directory -Force "$Scratch\d-rs" | Out-Null
  Copy-Item "$out\rs\rec.rs" "$Scratch\d-rs\rec.rs" -Force
  Copy-Item "$here\drivers\rs\main.rs" "$Scratch\d-rs\main.rs" -Force
  Remove-Item "$Scratch\d-rs\main.exe" -ErrorAction SilentlyContinue
  & rustc --edition 2021 -O --out-dir "$Scratch\d-rs" "$Scratch\d-rs\main.rs" > "$Scratch\rs-build.log" 2>&1
  if (Test-Path "$Scratch\d-rs\main.exe") {
    & "$Scratch\d-rs\main.exe" $res $corpus | Out-Null
    $ran += "rs"
  } else { $unproven += "rust    UNPROVEN: see $Scratch\rs-build.log" }
} else { $unproven += "rust    UNPROVEN: no rustc on this machine" }

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
    $ran += "cpp"
  } else { $unproven += "cpp     UNPROVEN: see $Scratch\cpp-build.log" }
} else { $unproven += "cpp     UNPROVEN: no MSVC on this machine" }

Write-Host "languages: $($ran -join ' ')"

# The name says the word and the corpus says the offset, so a driver that
# agreed with the others about the wrong thing still fails here.
$expected = @{}
foreach ($f in Get-ChildItem "$corpus\*.json") {
  $word = ($f.BaseName -split '\.')[0]
  $expected[$f.BaseName] = if ($word -eq "accept") { "ok" } else { $word }
}
$rows = @{}
foreach ($n in $ran) { $rows[$n] = Get-Content "$res\$n-corpus.txt" }
$wrong = 0
foreach ($line in $rows[$ran[0]]) {
  $p = $line -split "`t"
  if ($expected[$p[0]] -ne $p[1]) {
    Write-Host ("  {0}: expected {1}, {2} said {3}" -f $p[0], $expected[$p[0]], $ran[0], $p[1])
    $wrong++
  }
}
$disagreed = 0
foreach ($n in $ran) {
  if (Compare-Object $rows[$ran[0]] $rows[$n]) {
    Write-Host ("  {0,-4} DISAGREES with {1}" -f $n, $ran[0])
    Compare-Object $rows[$ran[0]] $rows[$n] | ForEach-Object { Write-Host ("      " + $_.InputObject) }
    $disagreed++
  }
}
Write-Host ("  {0} inputs, {1} refused, {2} languages" -f
  $expected.Count, ($expected.Values | Where-Object { $_ -ne "ok" }).Count, $ran.Count)
if ($wrong -eq 0 -and $disagreed -eq 0) {
  Write-Host "  every implementation refused every input with the same word at the same byte"
}

# A round trip is the encoder's whole argument: an accepted document decoded and
# written back is the one legal spelling, so it equals the fixture, and the four
# other languages equal it too. A fixture named accept.wide.* is deliberately
# not in that spelling and is only asked to agree across languages.
$rtWrong = 0
foreach ($f in Get-ChildItem "$res\$($ran[0])-rt-*.json") {
  $name = $f.Name -replace "^$($ran[0])-rt-", ""
  $first = Bytes $f.FullName
  foreach ($n in $ran) {
    if (-not (Same $first (Bytes "$res\$n-rt-$name"))) {
      Write-Host ("  {0,-4} re-encodes {1} differently" -f $n, $name); $rtWrong++
    }
  }
  if ($name -notlike "accept.wide.*") {
    if (-not (Same $first (Bytes "$corpus\$name"))) {
      Write-Host ("  {0} is not the spelling its own encoder produces" -f $name); $rtWrong++
    }
  }
}
if ($rtWrong -eq 0) {
  Write-Host "  every accepted document re-encodes to one spelling in every language"
}

foreach ($u in $unproven) { Write-Host $u }
if ($wrong -or $disagreed -or $rtWrong) { exit 1 }
