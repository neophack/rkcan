@echo off
chcp 65001 >nul
echo ========================================
echo  RKCAN - Dual CAN-FD to UDP Bridge
echo  Build Script
echo ========================================
echo.

REM Check Go installation
where go >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] Go not found. Please install Go first.
    echo Download: https://golang.org/dl/
    pause
    exit /b 1
)

echo [1/5] Checking Go version...
go version
echo.

REM Initialize go mod if missing
if not exist "go.mod" (
    echo [2/5] Initializing Go module...
    go mod init github.com/penghongxia/rkcan
    echo.
) else (
    echo [2/5] Go module already exists, skipping
echo.
)

echo [3/5] Tidying dependencies...
go mod tidy
if %errorlevel% neq 0 (
    echo [ERROR] go mod tidy failed!
    pause
    exit /b 1
)
echo.

echo [4/5] Building for Linux...
echo.

REM Linux ARM32 (older RK boards)
echo   - Building linux/arm ...
set GOOS=linux
set GOARCH=arm
set GOARM=7
set CGO_ENABLED=0
go build -ldflags="-s -w" -o rkcan-linux-arm32 .
if %errorlevel% neq 0 (
    echo [ERROR] linux/arm build failed!
    pause
    exit /b 1
)
echo     OK: rkcan-linux-arm32


echo.
echo ========================================
echo  Build SUCCESS
echo ========================================
echo.
dir rkcan-linux-* /b
echo.

echo ========================================
echo  Deployment Guide
echo ========================================
echo.
echo 1. Choose the correct binary for your target device:
echo    - RK3288 / older boards : rkcan-linux-arm32
echo.
echo 2. Upload to target (10.0.0.100) via SCP:
echo    scp rkcan-linux-arm32 root@10.0.0.100:/userdata/rkcan
echo    Password: yfcommon
echo.
echo    If you have sshpass installed:
echo    sshpass -p "yfcommon" scp rkcan-linux-arm32 root@10.0.0.100:/userdata/rkcan
echo.
echo 3. On the target device, run:
echo    chmod +x /userdata/rkcan
echo    cd /userdata
echo.
echo    # Run with defaults (can0+can1 -> UDP broadcast port 6000)
echo    ./rkcan
echo.
echo    # Run with custom target (e.g. local receiver)
echo    ./rkcan -addr 127.0.0.1:6000
echo.
echo    # Run with different CAN interfaces
echo    ./rkcan -can0 can0 -can1 can1 -addr 10.0.0.100:6000
echo.
echo 4. Receiver (UdpCanFdReceiver) usage on PC:
echo    udp_canfd_recv 6000
echo.
echo ========================================
echo  CAN Interface Setup (run on target)
echo ========================================
echo.
echo    # CAN-FD mode (recommended)
echo    sudo ip link set can0 type can bitrate 500000 dbitrate 2000000 fd on
echo    sudo ip link set up can0
echo    sudo ip link set can1 type can bitrate 500000 dbitrate 2000000 fd on
echo    sudo ip link set up can1
echo.
echo    # Classic CAN mode
echo    sudo ip link set can0 type can bitrate 500000
echo    sudo ip link set up can0
echo    sudo ip link set can1 type can bitrate 500000
echo    sudo ip link set up can1
echo.
echo ========================================
pause
