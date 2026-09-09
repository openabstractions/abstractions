@echo off
setlocal enabledelayedexpansion

:: ============================================================
:: ComfyUI Launcher / Stopper
:: No PID file. Source of truth = whatever is actually listening
:: on port 8188 right now, identity-verified via its command line.
::
:: Usage:
::   "ComfyUI launcher.cmd"          -> start (or reuse) ComfyUI, open browser
::   "ComfyUI launcher.cmd" stop     -> stop a running ComfyUI instance
:: ============================================================

set "COMFY_ROOT=%LOCALAPPDATA%\AMD\AI_Bundle\ComfyUI"
set "VENV_DIR=%COMFY_ROOT%\venv"
set "VENV_PY=%VENV_DIR%\Scripts\python.exe"
set "VENV_PYW=%VENV_DIR%\Scripts\pythonw.exe"
set "COMFY_APP=%COMFY_ROOT%\ComfyUI"
set "LOGFILE=%COMFY_ROOT%\comfyui_log.txt"
set "ERRFILE=%COMFY_ROOT%\comfyui_err.txt"
set "URL=http://127.0.0.1:8188"

if /I "%~1"=="stop" goto :STOP

echo ============================================
echo  ComfyUI Launcher
echo ============================================

:: --- Is something already listening on 8188? Verify it's really ComfyUI before reusing it ---
call :FIND_PORT_OWNER LIVE_PID
if defined LIVE_PID (
    call :VERIFY_IS_COMFYUI "!LIVE_PID!" VERIFIED
    if "!VERIFIED!"=="1" (
        echo ComfyUI is already running ^(PID !LIVE_PID!, verified^). Opening browser...
        start "" "%URL%"
        exit /b 0
    ) else (
        echo [WARN] Port 8188 is already in use by PID !LIVE_PID!, but it is NOT ComfyUI.
        echo Not touching it. Close whatever that is before launching.
        pause
        exit /b 1
    )
)

:: --- Step 1: locate venv python ---
if not exist "%VENV_PY%" (
    echo [WARN] venv python not found at expected path, searching AI_Bundle ...
    for /f "delims=" %%P in ('dir "%LOCALAPPDATA%\AMD\AI_Bundle" /s /b 2^>nul ^| findstr /i "venv\\Scripts\\python.exe"') do (
        set "VENV_PY=%%P"
    )
    if not exist "!VENV_PY!" (
        echo [FATAL] No venv python.exe found under AI_Bundle. Aborting.
        pause
        exit /b 1
    )
)
echo Found venv python: %VENV_PY%

:: --- Step 2: verify it runs ---
"%VENV_PY%" --version >nul 2>&1
if errorlevel 1 (
    echo [FATAL] python.exe found but failed to execute. Aborting.
    pause
    exit /b 1
)
for /f "tokens=*" %%V in ('"%VENV_PY%" --version 2^>^&1') do set "PYVER=%%V"
echo Verified: %PYVER%

:: --- Step 3: confirm ComfyUI app exists ---
if not exist "%COMFY_APP%\main.py" (
    echo [FATAL] main.py not found at %COMFY_APP%\main.py
    pause
    exit /b 1
)

:: --- Step 4: ensure dependencies are present (skip if already correct) ---
:: NOTE: PyPI's generic torchvision does NOT match AMD's custom ROCm torch build
:: and will crash with "operator torchvision::nms does not exist".
:: Must install the matching version from AMD's own ROCm wheel index.
:: Check the installed version FIRST so a normal launch never touches the network.
set "NEED_TORCHVISION=0.27.0+rocm7.14.0"
echo Checking torchvision (ROCm-matched build) ...
"%VENV_PY%" -c "import torchvision;print(torchvision.__version__)" 2>nul | findstr /C:"%NEED_TORCHVISION%" >nul
if not errorlevel 1 (
    echo Dependency OK: torchvision %NEED_TORCHVISION% already installed - skipping install.
) else (
    echo torchvision %NEED_TORCHVISION% not found. Installing ROCm-matched build ...
    "%VENV_PY%" -m pip install --index-url https://repo.amd.com/rocm/whl-multi-arch/ "torchvision[device-all]==%NEED_TORCHVISION%"
    if errorlevel 1 (
        echo [FATAL] pip install torchvision ^(ROCm build^) failed. See output above.
        pause
        exit /b 1
    )
)

:: --- Step 5: launch via PowerShell (writes the PID to a file, then read it back) ---
:: NOTE: do NOT capture the PID via `for /f` around powershell - the launched
:: pythonw.exe inherits the pipe's write-end and never closes it, so the loop
:: hangs forever even though ComfyUI is already up and serving.
set "RUN_PY=%VENV_PY%"
if exist "%VENV_PYW%" set "RUN_PY=%VENV_PYW%"

echo Launching ComfyUI in background...
del "%LOGFILE%" >nul 2>&1
del "%ERRFILE%" >nul 2>&1
set "PIDFILE=%COMFY_ROOT%\_launchpid.txt"
del "%PIDFILE%" >nul 2>&1

powershell -NoProfile -Command "(Start-Process -FilePath '%RUN_PY%' -ArgumentList 'main.py' -WorkingDirectory '%COMFY_APP%' -WindowStyle Hidden -PassThru -RedirectStandardOutput '%LOGFILE%' -RedirectStandardError '%ERRFILE%').Id | Set-Content -Encoding Ascii -Path '%PIDFILE%'"

set "NEWPID="
if exist "%PIDFILE%" set /p NEWPID=<"%PIDFILE%"
del "%PIDFILE%" >nul 2>&1

if not defined NEWPID (
    echo [FATAL] Failed to launch ComfyUI process.
    pause
    exit /b 1
)
echo Started with PID !NEWPID! ^(this PID is only used to monitor THIS startup - never persisted^).

:: --- Step 5b: write a tiny health-check script (prints 200 when ready, else ERROR) ---
set "HEALTHCHECK_PY=%COMFY_ROOT%\_healthcheck.py"
> "%HEALTHCHECK_PY%" (
    echo import sys, urllib.request
    echo url = sys.argv[1]
    echo try:
    echo     opener = urllib.request.build_opener^(urllib.request.ProxyHandler^({}^)^)
    echo     r = opener.open^(url, timeout=2^)
    echo     print^(r.status^)
    echo except Exception:
    echo     print^('ERROR'^)
    echo     sys.exit^(1^)
)

:: --- Step 6: monitor - poll for readiness AND verify the process is still alive ---
echo Waiting for ComfyUI to become ready...
set /a TRIES=0
:POLL
tasklist /FI "PID eq !NEWPID!" 2>nul | find "!NEWPID!" >nul
if errorlevel 1 (
    echo.
    echo [FATAL] ComfyUI process ^(PID !NEWPID!^) exited unexpectedly during startup.
    echo ---- Error output ^(%ERRFILE%^) ----
    type "%ERRFILE%"
    echo -------------------------------------
    pause
    exit /b 1
)

set /a TRIES+=1
set "HC_OUT=%COMFY_ROOT%\_healthcheck_out.txt"
"%VENV_PY%" "%HEALTHCHECK_PY%" "%URL%" > "%HC_OUT%" 2>nul
set "HTTPCODE="
set /p HTTPCODE=<"%HC_OUT%"
if "!HTTPCODE!"=="200" goto :READY

echo   attempt !TRIES!/40 - not ready yet ^(server still starting^)...

if !TRIES! GEQ 40 (
    echo [FATAL] Timed out after !TRIES! attempts ^(~2 minutes^), but the process is still alive.
    echo Check %LOGFILE% / %ERRFILE%, or just open %URL% manually.
    pause
    exit /b 1
)
timeout /t 3 >nul
goto :POLL

:READY
del "%HEALTHCHECK_PY%" >nul 2>&1
del "%COMFY_ROOT%\_healthcheck_out.txt" >nul 2>&1
echo ComfyUI is up and responding. Opening browser...
start "" "%URL%"
exit /b 0

:STOP
echo Stopping ComfyUI...
call :FIND_PORT_OWNER LIVE_PID
if not defined LIVE_PID (
    echo Nothing is listening on port 8188. Nothing to stop.
    exit /b 0
)
call :VERIFY_IS_COMFYUI "!LIVE_PID!" VERIFIED
if "!VERIFIED!"=="1" (
    taskkill /PID !LIVE_PID! /F >nul 2>&1
    echo Stopped PID !LIVE_PID! ^(verified ComfyUI^).
) else (
    echo [WARN] PID !LIVE_PID! is on port 8188 but is NOT ComfyUI. Not touching it.
)
exit /b 0

:: ============================================================
:: Subroutine: FIND_PORT_OWNER <result_var>
:: Finds the PID currently LISTENING on port 8188, if any.
:: This IS the source of truth - no file, always live OS state.
:: ============================================================
:FIND_PORT_OWNER
set "%~1="
for /f "tokens=5" %%a in ('netstat -ano ^| findstr ":8188" ^| findstr "LISTENING"') do (
    set "%~1=%%a"
)
exit /b 0

:: ============================================================
:: Subroutine: VERIFY_IS_COMFYUI <pid> <result_var>
:: Confirms a PID is alive AND its command line points at our
:: ComfyUI main.py - never trust a bare PID number alone.
:: Sets result_var to 1 if verified, 0 otherwise.
:: ============================================================
:VERIFY_IS_COMFYUI
setlocal
set "CHECK_PID=%~1"
set "RESULT=0"

tasklist /FI "PID eq %CHECK_PID%" 2>nul | find "%CHECK_PID%" >nul
if errorlevel 1 (
    endlocal & set "%~2=0" & exit /b 0
)

set "CMDFILE=%TEMP%\_comfy_cmdline_%CHECK_PID%.txt"
powershell -NoProfile -Command "(Get-CimInstance Win32_Process -Filter \"ProcessId=%CHECK_PID%\").CommandLine" > "%CMDFILE%" 2>nul
set "CMDLINE="
set /p CMDLINE=<"%CMDFILE%"
del "%CMDFILE%" >nul 2>&1

echo !CMDLINE! | findstr /I "main.py" >nul
if not errorlevel 1 (
    echo !CMDLINE! | findstr /I "ComfyUI" >nul
    if not errorlevel 1 set "RESULT=1"
)

endlocal & set "%~2=%RESULT%"
exit /b 0