#!/bin/bash
# RKCAN Deploy Script
# Uploads the ARM64 binary to root@10.0.0.100:/userdata

HOST="root@10.0.0.100"
PASS="yfcommon"
FILE="rkcan-linux-arm64"
DEST="/userdata/rkcan"

set -e

if [ ! -f "$FILE" ]; then
    echo "ERROR: $FILE not found. Please run build.bat first."
    exit 1
fi

if command -v sshpass &> /dev/null; then
    echo "Uploading $FILE to $HOST:$DEST ..."
    sshpass -p "$PASS" scp "$FILE" "$HOST:$DEST"
    echo "Setting executable permission..."
    sshpass -p "$PASS" ssh "$HOST" "chmod +x $DEST"
    echo ""
    echo "========================================"
    echo "  Deploy SUCCESS"
    echo "========================================"
    echo ""
    echo "To start on the target device:"
    echo "  sshpass -p '$PASS' ssh $HOST"
    echo "  $DEST"
    echo ""
else
    echo "========================================"
    echo "  Manual Deploy Commands"
    echo "========================================"
    echo ""
    echo "sshpass not found. Please run the following manually:"
    echo ""
    echo "  scp $FILE $HOST:$DEST"
    echo "  ssh $HOST"
    echo "  chmod +x $DEST"
    echo "  $DEST"
    echo ""
    echo "Password when prompted: $PASS"
    echo ""
fi
