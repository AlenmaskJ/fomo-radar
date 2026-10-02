@echo off
setlocal
cd /d "%~dp0"

set "DEMO_DB=%CD%\fomo.db"
set "DEMO_URL=http://127.0.0.1:18080/market"
set "HEALTH_URL=http://127.0.0.1:18080/health/market"
set "STATUS_URL=http://127.0.0.1:18080/api/market/status"
if not defined FOMO_PROXY set "FOMO_PROXY=http://127.0.0.1:10808"

rem 新币扫描器通过环境变量复用同一数据库和代理配置。
set "FOMO_DB=%DEMO_DB%"
set "HTTP_PROXY=%FOMO_PROXY%"
set "HTTPS_PROXY=%FOMO_PROXY%"

if /i "%~1"=="--dry-run" goto dry_run

if not exist "%CD%\fomo-server.exe" (
  echo Missing fomo-server.exe. Build the project first.
  pause
  exit /b 1
)
if not exist "%CD%\fomo-scanner.exe" (
  echo Missing fomo-scanner.exe. Build the project first.
  pause
  exit /b 1
)

powershell -NoProfile -Command "$exe=[IO.Path]::GetFullPath('%CD%\fomo-server.exe'); $db=[IO.Path]::GetFullPath('%DEMO_DB%'); foreach ($p in @(Get-CimInstance Win32_Process)) { if ($p.Name -eq 'fomo-server.exe' -and $p.ExecutablePath -eq $exe -and $p.CommandLine.Contains('-serve') -and $p.CommandLine.Contains('-market-watch') -and $p.CommandLine.Contains($db) -and $p.CommandLine.Contains('127.0.0.1:18080')) { exit 0 } }; exit 1"
if errorlevel 1 (
  echo [1/3] Starting FomoRadar dashboard and mainstream scanner...
  powershell -NoProfile -Command "$exe=[IO.Path]::GetFullPath('%CD%\fomo-server.exe'); $arguments=@('-serve','-market-watch','-db','%DEMO_DB%','-proxy','%FOMO_PROXY%','-listen','127.0.0.1:18080'); Start-Process -FilePath $exe -ArgumentList $arguments -WorkingDirectory '%CD%' -WindowStyle Hidden"
) else (
  echo [1/3] FomoRadar dashboard is already running.
)

powershell -NoProfile -Command "$exe=[IO.Path]::GetFullPath('%CD%\fomo-scanner.exe'); foreach ($p in @(Get-CimInstance Win32_Process)) { if ($p.Name -eq 'fomo-scanner.exe' -and $p.ExecutablePath -eq $exe -and $p.CommandLine.Contains('-watch')) { exit 0 } }; exit 1"
if errorlevel 1 (
  echo [2/3] Starting automatic new-token scanner...
  powershell -NoProfile -Command "$exe=[IO.Path]::GetFullPath('%CD%\fomo-scanner.exe'); Start-Process -FilePath $exe -ArgumentList @('-watch') -WorkingDirectory '%CD%' -WindowStyle Hidden"
) else (
  echo [2/3] Automatic new-token scanner is already running.
)

echo [3/3] Waiting up to 240 seconds for the first complete mainstream scan...
for /L %%I in (1,1,240) do (
  powershell -NoProfile -Command "try { $status=Invoke-RestMethod -Uri '%STATUS_URL%' -TimeoutSec 2; if ($status.state -eq 'failed') { exit 2 }; $health=Invoke-WebRequest -UseBasicParsing -Uri '%HEALTH_URL%' -TimeoutSec 2; if ($health.StatusCode -eq 200 -and $status.state -in @('completed','degraded')) { exit 0 } } catch {}; exit 1"
  if errorlevel 2 goto scan_failed
  if not errorlevel 1 goto ready
  ping 127.0.0.1 -n 2 >nul
)

echo Dashboard did not become ready. Run fomo-server.exe in a terminal to inspect the error.
pause
exit /b 1

:scan_failed
echo Startup scan failed. Run fomo-server.exe in a terminal to inspect the error.
pause
exit /b 1

:ready
start "" "%DEMO_URL%"
echo Ready: %DEMO_URL%
exit /b 0

:dry_run
echo fomo-server.exe -serve -market-watch -db "%DEMO_DB%" -proxy %FOMO_PROXY% -listen 127.0.0.1:18080
echo fomo-scanner.exe -watch FOMO_DB="%FOMO_DB%" HTTP_PROXY=%HTTP_PROXY% HTTPS_PROXY=%HTTPS_PROXY%
echo wait up to 240 seconds for %HEALTH_URL%
echo %HEALTH_URL%
echo %STATUS_URL%
echo %DEMO_URL%
exit /b 0
