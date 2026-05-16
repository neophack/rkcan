@echo off
REM Build chrony 4.5 statically for ARM (arm-linux-gnueabihf) using Docker
REM Output will be in chrony_build\out directory

set SCRIPT_DIR=%~dp0
set OUT_DIR=%SCRIPT_DIR%out

if not exist "%OUT_DIR%" mkdir "%OUT_DIR%"

docker run --rm -v "%OUT_DIR%:/out" -v "%SCRIPT_DIR%build.sh:/build.sh" debian:bookworm bash /build.sh

echo.
if %ERRORLEVEL% EQU 0 (
    echo Build successful! Output in: %OUT_DIR%
) else (
    echo Build failed!
)
pause
