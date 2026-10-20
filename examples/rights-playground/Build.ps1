<#
.SYNOPSIS
Prepare a compiler-free Windows rights playground using reviewed runtime/Panel binaries.
.DESCRIPTION
Builds this Go application once, copies it into two separate A/B executable files,
and copies the exact runtime/Panel binaries from -PrebuiltDir. No process is launched.
The copied runtime and Panel must match the composed source of the demo. Start.cmd
opens the native Panel/presenter and two app-owned windows. Read and typed
Use report unavailable until the required service contracts are served.
.EXAMPLE
powershell -File examples/rights-playground/Build.ps1 -Dir .forks/rights-pack -PrebuiltDir .forks/composed-native-pack
#>
param([string]$Dir, [string]$PrebuiltDir, [string]$Go = 'go', [switch]$Help)
$ErrorActionPreference = 'Stop'
if ($Help -or -not $Dir -or -not $PrebuiltDir) {
    Write-Output 'Build.ps1 -Dir <new pack> -PrebuiltDir <reviewed runtime/Panel folder> [-Go go]'
    exit $(if ($Help -or (-not $Dir -and -not $PrebuiltDir)) { 0 } else { 2 })
}
$pack = [IO.Path]::GetFullPath($Dir)
$inputDir = (Resolve-Path -LiteralPath $PrebuiltDir).Path
if (Test-Path -LiteralPath (Join-Path $pack 'run')) { throw 'Use a new pack folder; an existing run is preserved.' }
New-Item -ItemType Directory -Force -Path $pack | Out-Null
foreach ($name in @('openabstractions.exe', 'Abstraction Panel.exe')) {
    $source = Join-Path $inputDir $name
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "Missing reviewed binary: $source" }
    $target = Join-Path $pack $name
    if ([IO.Path]::GetFullPath($source) -ne $target) { Copy-Item -LiteralPath $source -Destination $target }
}
$appA = Join-Path $pack 'Playground A.exe'
& $Go -C $PSScriptRoot build -ldflags '-H windowsgui' -o $appA .
if ($LASTEXITCODE -ne 0) { throw 'Playground build failed.' }
Copy-Item -LiteralPath $appA -Destination (Join-Path $pack 'Playground B.exe')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Lifecycle.ps1') -Destination (Join-Path $pack 'Lifecycle.ps1')
foreach ($action in @('Start', 'Stop', 'Reset')) {
    $text = "@echo off`r`npowershell.exe -NoProfile -ExecutionPolicy Bypass -File `"%~dp0Lifecycle.ps1`" -Action $action`r`nif errorlevel 1 pause`r`n"
    Set-Content -LiteralPath (Join-Path $pack "$action.cmd") -Value $text -Encoding ascii
}
foreach ($label in @('A', 'B')) {
    $text = "@echo off`r`nstart `"`" `"%~dp0Playground $label.exe`" window --connection `"%~dp0run\connection.json`"`r`n"
    Set-Content -LiteralPath (Join-Path $pack "App-$label.cmd") -Value $text -Encoding ascii
}
$hashes = foreach ($name in @('openabstractions.exe', 'Abstraction Panel.exe', 'Playground A.exe', 'Playground B.exe', 'Lifecycle.ps1')) {
    $file = Join-Path $pack $name
    $algorithm = [Security.Cryptography.SHA256]::Create()
    $stream = [IO.File]::OpenRead($file)
    try { $digest = [BitConverter]::ToString($algorithm.ComputeHash($stream)).Replace('-','').ToLowerInvariant() }
    finally { $stream.Dispose(); $algorithm.Dispose() }
    [ordered]@{ name = $name; sha256 = $digest }
}
[ordered]@{ application = 'rights-playground'; preparation = 'not launched'; binaries = $hashes } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $pack 'build-inputs.json') -Encoding utf8
Write-Output "Prepared $pack. No runtime, dialog or application was launched."
