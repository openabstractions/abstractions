<#
.SYNOPSIS
Run the abstraction-job-store branch of the Lemonade fork under forks/lemonade,
instead of the installed Lemonade and without uninstalling it.

.DESCRIPTION
The two builds can coexist, but three things are shared and exactly one process
may own each: the HTTP port, the UDP discovery beacon on 13305, and the
`lemonade://` URI handler that decides which desktop app the tray opens.

This stops the installed server, starts this branch's, and launches this
branch's app -- which re-registers the URI handler to itself on startup, because
Tauri's deep-link plugin does that for dev builds. Running the installed app
once puts the handler back. Whichever app ran last owns it.

Nothing here uninstalls anything, and your models are untouched: they live in
the HuggingFace cache, which both builds share.

.PARAMETER Cache
Cache directory for this branch. Defaults to a scratch dir, deliberately: with
-Port set, the server PERSISTS the port into that directory's config.json, and
writing 8123 into your real config would make the installed build come up there
too.

Point this at $env:USERPROFILE\.cache\lemonade only when you want this branch to
use your real settings -- and back up config.json first.

.PARAMETER Port
HTTP port. Default 8123 so it cannot collide with the installed server's 13305.
Pass 13305 to take over the normal port once you are happy.

.PARAMETER Store
Job store for the system downloader. When given, jobd is started against it and
downloads are handed off instead of being fetched in-process. Requires jobd on
PATH or beside this repo.

.PARAMETER Restore
Stop this branch, hand the URI handler back to the installed app, and start it.

.EXAMPLE
  .\scripts\lemonade-dev-run.ps1
  .\scripts\lemonade-dev-run.ps1 -Store C:\temp\sysstore
  .\scripts\lemonade-dev-run.ps1 -Restore
#>
[CmdletBinding()]
param(
    [string]$Cache = "$env:TEMP\lemonade-dev",
    [int]$Port = 8123,
    [string]$Store,
    [switch]$Restore
)

$ErrorActionPreference = 'Stop'
$abstractions = Split-Path -Parent $PSScriptRoot
$repo = Join-Path $abstractions 'forks\lemonade'
$build = Join-Path $repo 'build'
$server = Join-Path $build 'Release\LemonadeServer.exe'
$app = Join-Path $build 'app\lemonade-app.exe'
$installedApp = "$env:LOCALAPPDATA\lemonade_server\app\lemonade-app.exe"
$installedServer = "$env:LOCALAPPDATA\lemonade_server\bin\LemonadeServer.exe"
$uriKey = 'HKCU:\SOFTWARE\Classes\lemonade\shell\open\command'

function Get-UriHandler {
    (Get-ItemProperty $uriKey -ErrorAction SilentlyContinue).'(default)'
}

function Stop-Everything {
    foreach ($n in @('LemonadeServer', 'lemonade-app', 'lemond', 'jobd')) {
        Get-Process -Name $n -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep -Seconds 2
}

if ($Restore) {
    Write-Host "Stopping this branch..." -ForegroundColor Cyan
    Stop-Everything
    if (Test-Path $installedApp) {
        Write-Host "Handing the lemonade:// handler back to the installed app..." -ForegroundColor Cyan
        $p = Start-Process $installedApp -PassThru
        Start-Sleep -Seconds 8
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path $installedServer) { Start-Process $installedServer }
    Write-Host "lemonade:// -> $(Get-UriHandler)"
    Write-Host "Restored." -ForegroundColor Green
    return
}

foreach ($required in @($server, $app)) {
    if (-not (Test-Path $required)) {
        throw "Missing $required. Build first: cmake --build --preset vs18 --target LemonadeServer tauri-app"
    }
}

Write-Host "lemonade:// currently -> $(Get-UriHandler)" -ForegroundColor DarkGray
Write-Host "Stopping the installed Lemonade (not uninstalling it)..." -ForegroundColor Cyan
Stop-Everything

New-Item -ItemType Directory -Force -Path $Cache | Out-Null

if ($Store) {
    $jobd = @(
        (Join-Path $abstractions 'bin\jobd.exe'),
        'jobd.exe'
    ) | Where-Object { $_ -and (Get-Command $_ -ErrorAction SilentlyContinue) } | Select-Object -First 1
    if (-not $jobd) {
        Write-Warning "jobd not found; downloads will be fetched in-process instead of handed off."
    } else {
        New-Item -ItemType Directory -Force -Path $Store | Out-Null
        # Record the store so every tool finds it, including this server. Setup
        # is once per machine; the server reads it, never asks for it.
        & $jobd setup --store $Store | Out-Null
        # Start-Process -Environment is PowerShell 7+; this has to run on the
        # 5.1 that ships with Windows, so set it on this process and let the
        # child inherit.
        $env:ABSTRACTION_STORE = $Store
        Start-Process $jobd -ArgumentList 'run', '--interval', '30s'
        Write-Host "System downloader running against $Store" -ForegroundColor Green
    }
}

Write-Host "Starting this branch's server on port $Port (cache: $Cache)..." -ForegroundColor Cyan
Start-Process $server -ArgumentList $Cache, '--port', $Port, '--host', '127.0.0.1'
Start-Sleep -Seconds 6

Write-Host "Starting this branch's app (it claims lemonade:// on startup)..." -ForegroundColor Cyan
Start-Process $app
Start-Sleep -Seconds 8

Write-Host ""
Write-Host "lemonade:// now -> $(Get-UriHandler)"
Write-Host "web UI          -> http://127.0.0.1:$Port/" -ForegroundColor Green
Write-Host ""
Write-Host "The tray icon's Open item now opens THIS branch's app." -ForegroundColor Green
Write-Host "To go back:  .\scripts\lemonade-dev-run.ps1 -Restore"
