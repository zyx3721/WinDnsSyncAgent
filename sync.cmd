@echo off
setlocal
cd /d "%~dp0"

echo Starting WinDnsSyncAgent sync...
echo Working directory: %CD%
echo Command: powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0sync.ps1" %*
echo.

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0sync.ps1" %*

set EXIT_CODE=%ERRORLEVEL%
echo.
echo Sync exited with code %EXIT_CODE%.
pause
exit /b %EXIT_CODE%
