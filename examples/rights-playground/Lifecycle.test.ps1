param([switch]$Run, [string]$Dir)
$ErrorActionPreference = 'Stop'
if (-not $Run -or -not $Dir) {
    Write-Output 'Lifecycle.test.ps1 -Run -Dir <test-owned temporary folder>'
    exit 0
}
$target = [IO.Path]::GetFullPath($Dir)
if (-not (Test-Path -LiteralPath $target -PathType Container)) { throw 'Test caller must create its own temporary folder.' }
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Lifecycle.ps1') -Destination (Join-Path $target 'Lifecycle.ps1')
foreach ($name in @('openabstractions.exe','Abstraction Panel.exe','Playground A.exe','Playground B.exe')) {
    [IO.File]::WriteAllBytes((Join-Path $target $name), [byte[]]@())
}
$runDir = Join-Path $target 'run'
New-Item -ItemType Directory -Path $runDir | Out-Null
$children = [Collections.Generic.List[object]]::new()
try {
    foreach ($role in @('app-a','app-b','source','runtime')) {
        $child = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 60') -PassThru -WindowStyle Hidden
        $native = Get-CimInstance Win32_Process -Filter "ProcessId=$($child.Id)"
        if (-not $native) { throw 'Owned test child was not observable.' }
        $children.Add([ordered]@{ role=$role; pid=$child.Id; exe=$native.ExecutablePath; created=$native.CreationDate.ToUniversalTime().ToString('o') })
    }
    $created = $children[0].created
    $children[0].created = '1900-01-01T00:00:00.0000000Z'
    ConvertTo-Json -InputObject @($children.ToArray()) | Set-Content -LiteralPath (Join-Path $runDir 'processes.json') -Encoding utf8
    $refused = Start-Process -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File',('"{0}"' -f (Join-Path $target 'Lifecycle.ps1')),'-Action','Stop') -WindowStyle Hidden -PassThru -Wait -RedirectStandardError (Join-Path $target 'mismatch.stderr.log') -RedirectStandardOutput (Join-Path $target 'mismatch.stdout.log')
    if ($refused.ExitCode -eq 0) { throw 'Wrong process creation stamp was accepted.' }
    foreach ($child in $children) {
        if (-not (Get-CimInstance Win32_Process -Filter "ProcessId=$($child.pid)")) { throw 'Mismatch stop affected an owned process.' }
    }
    $children[0].created = $created
    # This JSON includes both app windows and the runtime/source processes.
    # PowerShell5's pipeline array behavior
    # used to make Read-Owned admit one array object and Stop skip its PIDs.
    ConvertTo-Json -InputObject @($children.ToArray()) | Set-Content -LiteralPath (Join-Path $runDir 'processes.json') -Encoding utf8
    & (Join-Path $PSHOME 'powershell.exe') -NoProfile -ExecutionPolicy Bypass -File (Join-Path $target 'Lifecycle.ps1') -Action Stop
    if ($LASTEXITCODE -ne 0) { throw 'Actual lifecycle Stop failed.' }
    foreach ($child in $children) {
        if (Get-CimInstance Win32_Process -Filter "ProcessId=$($child.pid)") { throw 'Actual lifecycle Stop skipped an owned JSON row.' }
    }
    Write-Output 'PASS: actual lifecycle stopped both app roles and runtime/source creation/path-matched owned PIDs from a PowerShell5 JSON array.'
} finally {
    foreach ($child in $children) {
        $native = Get-CimInstance Win32_Process -Filter "ProcessId=$($child.pid)" -ErrorAction SilentlyContinue
        if ($native -and $native.ExecutablePath -eq $child.exe -and $native.CreationDate.ToUniversalTime().ToString('o') -eq $child.created) {
            Stop-Process -Id $child.pid -Force
        }
    }
}
