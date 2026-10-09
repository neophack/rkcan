@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
rem ===================================================================
rem  RKCAN build script for Windows
rem  Builds install packages in dist\rkcan-linux-<arch>\ containing the
rem  binary and the installer. Copy one folder to the board and run:
rem      sh install.sh
rem ===================================================================

where go >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] Go not found. Install it from https://go.dev/dl/
    pause
    exit /b 1
)

for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set VERSION=%%v
if "%VERSION%"=="" set VERSION=dev
echo Building RKCAN %VERSION%
echo.

set CGO_ENABLED=0
set GOOS=linux
set LDFLAGS=-s -w -X main.version=%VERSION%

call :build arm64 arm64 ""
if errorlevel 1 goto :fail
call :build arm32 arm 7
if errorlevel 1 goto :fail

echo.
echo ===================================================================
echo  Build SUCCESS
echo ===================================================================
echo  Packages:
echo    dist\rkcan-linux-arm64   (RK3566/RK3568/RK3588/RK3576 64-bit)
echo    dist\rkcan-linux-arm32   (RK3288 / 32-bit systems)
echo.
echo  Install on the board:
echo    scp -r dist\rkcan-linux-arm64 root@^<board-ip^>:/tmp/
echo    ssh root@^<board-ip^> "cd /tmp/rkcan-linux-arm64 && sh install.sh"
echo.
echo  Then open http://^<board-ip^>/ in a browser.
echo ===================================================================
pause
exit /b 0

:build
set ARCH=%~1
set GOARCH=%~2
set GOARM=%~3
set OUT=dist\rkcan-linux-%ARCH%
echo   - linux/%ARCH% ...
if exist "%OUT%" rmdir /s /q "%OUT%"
mkdir "%OUT%"
go build -trimpath -ldflags="%LDFLAGS%" -o "%OUT%\rkcan" .
if errorlevel 1 exit /b 1
for %%f in (install.sh uninstall.sh rkcan.conf rkcan-can-setup rkcan-ctl rkcan.service rkcan-can.service rkcan.init) do (
    copy /y "deploy\%%f" "%OUT%\" >nul
)
copy /y README.md "%OUT%\" >nul
echo     OK: %OUT%
exit /b 0

:fail
echo [ERROR] build failed
pause
exit /b 1
