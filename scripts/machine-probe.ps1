# Answers, for each FOOTPRINT.md location handed to it on stdin, whether the
# thing is on this machine. Reads `row<TAB>kind<TAB>location cell` lines and
# writes `row<TAB>present|absent<TAB>target<TAB>what was asked` lines.
#
# It exists because the same question shelled out to schtasks and was answered
# wrong: Git Bash rewrites an argument that looks like a POSIX path, so
# `schtasks /query` reached the binary as `schtasks C:/Program Files/Git/query`,
# which exits non-zero, and a scheduled task that has been running since 07:39
# was reported missing while the ledger saying it was there was called a defect.
# Nothing here parses a command's text or depends on which shell invoked it.

$out = [System.Text.StringBuilder]::new()
function Answer($row, $state, $target, $ran) {
    [void]$out.Append("$row`t$state`t$target`t$ran`n")
}

function Ticked($cell) {
    [regex]::Matches($cell, '`([^`]+)`') | ForEach-Object { $_.Groups[1].Value }
}

function WindowsPath($p) {
    # A location with an elided middle or a placeholder is not a path. The test
    # is on bytes rather than on U+2026 because stdin here is decoded with the
    # console codepage, under which that character does not survive.
    if ($p -match '[^\x20-\x7E]' -or $p -match '[<>*{]') { return $null }
    if ($p -notmatch '^(%[A-Za-z_]+%|~|[A-Za-z]:\\)') { return $null }
    $e = [Environment]::ExpandEnvironmentVariables($p)
    if ($e.StartsWith('~')) { $e = $HOME + $e.Substring(1) }
    if ($e -match '%[A-Za-z_]+%') { return $null }
    return $e
}

foreach ($line in $input) {
    $f = ($line -replace "`r", '') -split "`t", 3
    if ($f.Count -lt 3) { continue }
    $row = $f[0]; $kind = $f[1]; $cell = $f[2]
    switch ($kind) {
        'task' {
            foreach ($t in @(Ticked $cell) | Where-Object { $_ -match '^\\[A-Za-z0-9_-]+$' }) {
                $name = $t.TrimStart('\')
                $task = Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
                if ($task) { Answer $row present "task $t" "Get-ScheduledTask -TaskName $name -> State=$($task.State)" }
                else       { Answer $row absent  "task $t" "Get-ScheduledTask -TaskName $name -> nothing" }
            }
        }
        'registry' {
            foreach ($k in @(Ticked $cell) | Where-Object { $_ -match '^HK[CL][UM]\\' -and $_ -notmatch '[{<*]' }) {
                $hive = @{ 'HKCU' = 'HKEY_CURRENT_USER'; 'HKLM' = 'HKEY_LOCAL_MACHINE' }[$k.Split('\')[0]]
                $ps = "Registry::$hive\" + $k.Substring($k.IndexOf('\') + 1)
                if (Test-Path -LiteralPath $ps) { Answer $row present $k "Test-Path $ps" }
                else                            { Answer $row absent  $k "Test-Path $ps" }
            }
        }
        'path' {
            foreach ($p in @(Ticked $cell)) {
                $e = WindowsPath $p
                if ($null -eq $e) { continue }
                if (Test-Path -LiteralPath $e) { Answer $row present $p "Test-Path $e" }
                else                           { Answer $row absent  $p "Test-Path $e" }
            }
        }
    }
}

[Console]::Out.Write($out.ToString())
