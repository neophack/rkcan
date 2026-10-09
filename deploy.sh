#!/bin/sh
# Build (if needed) and install RKCAN on a board over SSH.
#
#   ./deploy.sh [host] [arch]        e.g. ./deploy.sh root@10.0.0.100 arm64
#
# Environment: RKCAN_HOST (default root@10.0.0.100), RKCAN_ARCH (arm64),
# RKCAN_PASS (password for sshpass; omit to use SSH keys / prompt).
set -e

HOST=${1:-${RKCAN_HOST:-root@10.0.0.100}}
ARCH=${2:-${RKCAN_ARCH:-arm64}}
PASS=${RKCAN_PASS:-yfcommon}

cd "$(dirname "$0")"
make package >/dev/null
# shellcheck disable=SC2012
PKG=$(ls -t dist/rkcan-*-linux-"$ARCH".tar.gz | head -n 1)
[ -n "$PKG" ] || { echo "no package for $ARCH"; exit 1; }
NAME=$(basename "$PKG" .tar.gz)

if [ -n "$PASS" ] && command -v sshpass >/dev/null 2>&1; then
    SSH="sshpass -p $PASS ssh -o StrictHostKeyChecking=accept-new"
    SCP="sshpass -p $PASS scp -o StrictHostKeyChecking=accept-new"
else
    SSH="ssh"
    SCP="scp"
fi

echo "==> Uploading $PKG to $HOST"
$SCP "$PKG" "$HOST:/tmp/"
echo "==> Installing"
$SSH "$HOST" "cd /tmp && tar xzf $NAME.tar.gz && cd $NAME && sh install.sh && cd /tmp && rm -rf $NAME $NAME.tar.gz"
