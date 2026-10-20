<#
.SYNOPSIS
Start, stop or reset this prepared isolated Windows rights playground.
.DESCRIPTION
Start owns a unique runtime/state, native Panel/presenter, dummy receiver and A/B
windows. Setup uses only the legitimate runtime CLI and leaves credential
Use/Read ungranted. Stop preserves state. Reset publicly revokes the two owned dummy
registrations before stopping verified PIDs and deleting only this pack's run.
.EXAMPLE
powershell -File Lifecycle.ps1 -Action Start
#>
param([ValidateSet('Start','Stop','Reset')][string]$Action, [switch]$Help, [switch]$NoWindows)
$ErrorActionPreference = 'Stop'
if ($Help -or -not $Action) {
    Write-Output 'Lifecycle.ps1 -Action Start|Stop|Reset [-NoWindows for headless development checks]'
    exit 0
}
$pack = [IO.Path]::GetFullPath($PSScriptRoot)
$run = Join-Path $pack 'run'
$record = Join-Path $run 'processes.json'
$connectionFile = Join-Path $run 'connection.json'
$runtimeExe = Join-Path $pack 'openabstractions.exe'
$panelExe = Join-Path $pack 'Abstraction Panel.exe'
$appA = Join-Path $pack 'Playground A.exe'
$appB = Join-Path $pack 'Playground B.exe'
$owned = [Collections.Generic.List[object]]::new()

function Save-Owned {
    ConvertTo-Json -InputObject @($owned.ToArray()) -Depth 5 | Set-Content -LiteralPath $record -Encoding utf8
}
function Start-Owned([string]$Role, [string]$Image, [string[]]$Arguments, [bool]$Visible) {
    $parameters = @{ FilePath = $Image; ArgumentList = $Arguments; PassThru = $true; WindowStyle = $(if ($Visible) {'Normal'} else {'Hidden'}) }
    if (-not $Visible -or $Role -eq 'panel') {
        $parameters.RedirectStandardOutput = Join-Path $run "$Role.stdout.log"
        $parameters.RedirectStandardError = Join-Path $run "$Role.stderr.log"
    }
    $process = Start-Process @parameters
    $native = Get-CimInstance Win32_Process -Filter "ProcessId=$($process.Id)"
    if (-not $native -or -not $native.ExecutablePath) { throw "Could not record owned $Role process." }
    $owned.Add([ordered]@{ role = $Role; pid = $process.Id; exe = $native.ExecutablePath; created = $native.CreationDate.ToUniversalTime().ToString('o') })
    Save-Owned
    return $process
}
function Read-Owned {
    if (-not (Test-Path -LiteralPath $record)) { throw 'No owned processes record. Start the pack first.' }
    $items = Get-Content -LiteralPath $record -Raw | ConvertFrom-Json
    foreach ($item in $items) {
        if (-not $item.pid -or -not $item.exe -or -not $item.created) { throw 'Incomplete process ownership record.' }
        $owned.Add($item)
    }
}
function Matching-Process($Item) {
    $native = Get-CimInstance Win32_Process -Filter "ProcessId=$($Item.pid)" -ErrorAction SilentlyContinue
    if (-not $native) { return $null }
    if ($native.ExecutablePath -ne $Item.exe -or $native.CreationDate.ToUniversalTime().ToString('o') -ne $Item.created) {
        throw "PID $($Item.pid) has different ownership; preserved."
    }
    return $native
}
function Stop-Owned {
    foreach ($role in @('app-a','app-b','presenter','panel','source','runtime')) {
        foreach ($item in @($owned | Where-Object role -eq $role)) {
            if (Matching-Process $item) {
                Stop-Process -Id $item.pid -Force
                Write-Output "Stopped owned $role PID $($item.pid)."
            }
        }
    }
}
function Assert-PlainTree([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reset refuses a linked run folder; preserved.' }
    foreach ($child in Get-ChildItem -LiteralPath $Path -Force) {
        if ($child.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reset refuses a linked child; preserved.' }
        if ($child.PSIsContainer) { Assert-PlainTree $child.FullName }
    }
}
function Wait-Line([string]$File, [string]$Pattern) {
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $File) {
            $match = Select-String -LiteralPath $File -Pattern $Pattern | Select-Object -First 1
            if ($match) { return $match.Matches[0].Groups[1].Value.Trim() }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Readiness not observed in $File; owned records preserved for Stop/Reset."
}
function Panel-Arguments([string]$Endpoint, [bool]$Background) {
    $arguments = @('-runtime-endpoint', $Endpoint, '-runtime-program', ('"{0}"' -f $runtimeExe), '-runtime-state-dir', ('"{0}"' -f (Join-Path $run 'state')))
    if ($Background) { return @('-background') + $arguments }
    return @('-native=' + (-not $NoWindows).ToString().ToLowerInvariant(), '-open=false', '-addr', '127.0.0.1:0') + $arguments
}
function Invoke-AppCommand([string]$Command, [string]$Role) {
    # A Windows GUI subsystem binary still supports redirected CLI commands.
    # Wait explicitly for definite setup/deletion before changing owned state.
    $stdout = Join-Path $run "$Role.stdout.log"
    $stderr = Join-Path $run "$Role.stderr.log"
    $process = Start-Process -FilePath $appA -ArgumentList @($Command,'--connection',('"{0}"' -f $connectionFile)) -PassThru -Wait -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    if (Test-Path -LiteralPath $stdout) { Get-Content -LiteralPath $stdout }
    if ($process.ExitCode -ne 0) { throw "$Role was not definite; owned state and processes preserved. See $stderr." }
}

foreach ($file in @($runtimeExe,$panelExe,$appA,$appB)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Prepared pack missing $file." }
}
if ($Action -eq 'Stop') {
    Read-Owned
    Stop-Owned
    Write-Output 'Stopped. State and dummy registrations remain; Reset removes them through public APIs.'
    exit 0
}
if ($Action -eq 'Reset') {
    Read-Owned
    $runtimeRecord = @($owned | Where-Object role -eq 'runtime')
    if ($runtimeRecord.Count -ne 1 -or -not (Matching-Process $runtimeRecord[0])) {
        throw 'Reset needs this recorded runtime alive. Start resumes this run, then Reset can delete owned registrations.'
    }
    Invoke-AppCommand 'reset-secrets' 'reset-secrets'
    Stop-Owned
    $resolvedRun = (Resolve-Path -LiteralPath $run).Path
    if ($resolvedRun -ne $run -or [IO.Path]::GetFullPath($resolvedRun) -ne (Join-Path $pack 'run')) { throw 'Reset target changed; folder preserved.' }
    Assert-PlainTree $resolvedRun
    Remove-Item -LiteralPath $resolvedRun -Recurse -Force
    Write-Output 'Reset complete: public dummy deletion succeeded and this pack run folder was removed.'
    exit 0
}

$resume = Test-Path -LiteralPath $connectionFile
if (Test-Path -LiteralPath $record) {
    Read-Owned
    foreach ($item in $owned) {
        if (Matching-Process $item) { throw 'This pack owns running processes. Reopen App-A/App-B, or Stop before Start.' }
    }
    $owned.Clear()
}
New-Item -ItemType Directory -Force -Path (Join-Path $run 'state') | Out-Null
if ($resume) {
    $connection = Get-Content -LiteralPath $connectionFile -Raw | ConvertFrom-Json
    $name = $connection.isolated
} else {
    $name = 'rights-playground-' + [Guid]::NewGuid().ToString('N').Substring(0,20)
}
$runtime = Start-Owned 'runtime' $runtimeExe @('serve','runtime','--isolated',$name,'--state-dir',('"{0}"' -f (Join-Path $run 'state')),'--operator-program',('"{0}"' -f $panelExe)) $false
$endpoint = Wait-Line (Join-Path $run 'runtime.stderr.log') '^\s*ABSTRACTION_RUNTIME_ENDPOINT=(.+)$'
$readyFile = Join-Path $run 'source-ready.json'
Remove-Item -LiteralPath $readyFile -ErrorAction SilentlyContinue
$source = Start-Owned 'source' $appA @('source','--ready-file',('"{0}"' -f $readyFile)) $false
$deadline = (Get-Date).AddSeconds(20)
while (-not (Test-Path -LiteralPath $readyFile)) {
    if ((Get-Date) -gt $deadline) { throw 'Dummy source did not become ready.' }
    Start-Sleep -Milliseconds 100
}
$origin = (Get-Content -LiteralPath $readyFile -Raw | ConvertFrom-Json).origin
$connectionText = [ordered]@{ version=1; isolated=$name; endpoint=$endpoint; runtime_program=$runtimeExe; state_dir=(Join-Path $run 'state'); app_a=$appA; app_b=$appB; origin=$origin } | ConvertTo-Json
[IO.File]::WriteAllText($connectionFile, $connectionText, [Text.UTF8Encoding]::new($false))
if (-not (Test-Path -LiteralPath (Join-Path $run 'setup.complete'))) {
    Invoke-AppCommand 'setup' 'setup'
    Set-Content -LiteralPath (Join-Path $run 'setup.complete') -Value 'Public dummy registration and narrow job prerequisite complete.' -Encoding utf8
}
$panel = Start-Owned 'panel' $panelExe (Panel-Arguments $endpoint $false) (-not $NoWindows)
$null = Wait-Line (Join-Path $run 'panel.stdout.log') '^service control panel: (.+)$'
if (-not $NoWindows) {
    $presenter = Start-Owned 'presenter' $panelExe (Panel-Arguments $endpoint $true) $false
    $null = Start-Owned 'app-a' $appA @('window','--connection',('"{0}"' -f $connectionFile)) $true
    $null = Start-Owned 'app-b' $appB @('window','--connection',('"{0}"' -f $connectionFile)) $true
}
Write-Output "Ready: $connectionFile"
Write-Output 'A/B select alpha/beta locally. Missing Use/Read may open the real OA permission request; rerun after the decision.'
Write-Output 'Stop preserves state. Reset publicly deletes only both dummy names before stopping this pack.'
