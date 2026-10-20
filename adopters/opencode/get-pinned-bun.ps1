<#
.SYNOPSIS
Fetch the pinned Bun 1.3.14 for the plugin's Bun-only tests.

.DESCRIPTION
Downloads bun-v1.3.14/bun-windows-x64.zip from oven-sh/bun's GitHub releases,
checks its SHA-256 against the digest this script pins ($expected, GitHub's
asset digest for that release file) and extracts bun.exe into
<Forks>/bun-1.3.14/. An archive already present there is checked again and
reused. A digest mismatch deletes the downloaded archive and fails.

.PARAMETER Forks
The caller's ignored .forks directory. Defaults to .forks at the root of the
Git checkout holding the current directory.

.EXAMPLE
powershell -NoProfile -File adopters/opencode/get-pinned-bun.ps1
#>
[CmdletBinding()]
param(
  [string]$Forks
)

$ErrorActionPreference = 'Stop'
$url = 'https://github.com/oven-sh/bun/releases/download/bun-v1.3.14/bun-windows-x64.zip'

# GitHub's asset digest for bun-v1.3.14 bun-windows-x64.zip, compared 2026-10-01.
$expected = '0a0620930b6675d7ba440e81f4e0e00d3cfbe096c4b140d3fff02205e9e18922'

if (-not $Forks) {
  $top = (& git rev-parse --show-toplevel)
  if ($LASTEXITCODE -ne 0 -or -not $top) { throw 'Not inside a Git checkout; pass -Forks.' }
  $Forks = Join-Path $top.Trim() '.forks'
}
$target = Join-Path ([IO.Path]::GetFullPath($Forks)) 'bun-1.3.14'
New-Item -ItemType Directory -Path $target -Force | Out-Null
$archive = Join-Path $target 'bun-windows-x64.zip'
$bun = Join-Path $target 'bun.exe'

if (-not (Test-Path -LiteralPath $archive)) {
  $partial = "$archive.part"
  $previous = $ProgressPreference
  $ProgressPreference = 'SilentlyContinue'
  try {
    Invoke-WebRequest -Uri $url -OutFile $partial -UseBasicParsing
  } finally {
    $ProgressPreference = $previous
  }
  Move-Item -LiteralPath $partial -Destination $archive -Force
}
$digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($digest -ne $expected) {
  Remove-Item -LiteralPath $archive -Force
  throw "Bun archive $archive ($url) has SHA-256 $digest; this script pins $expected. The archive was deleted."
}

Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [IO.Compression.ZipFile]::OpenRead($archive)
try {
  $entry = $zip.GetEntry('bun-windows-x64/bun.exe')
  if (-not $entry) { throw 'The Bun archive has no bun-windows-x64/bun.exe.' }
  [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $bun, $true)
} finally {
  $zip.Dispose()
}
$version = (& $bun --version).Trim()
if ($version -ne '1.3.14') { throw "$bun reports $version; 1.3.14 wanted." }
Write-Output $bun
