#!/bin/sh
# Remove RKCAN. Options: --prefix DIR (as used for install), --purge (also
# delete /etc/rkcan).
PREFIX=/usr/local
PURGE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) PREFIX=$2; shift 2 ;;
        --purge) PURGE=1; shift ;;
        *) echo "unknown option: $1" >&2; exit 1 ;;
    esac
done
[ "$(id -u)" = "0" ] || { echo "please run as root" >&2; exit 1; }

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now rkcan.service rkcan-can.service >/dev/null 2>&1
    rm -f /etc/systemd/system/rkcan.service /etc/systemd/system/rkcan-can.service
    systemctl daemon-reload
fi
for f in /etc/init.d/rkcan /etc/init.d/S99rkcan; do
    if [ -f "$f" ]; then
        "$f" stop >/dev/null 2>&1
        command -v update-rc.d >/dev/null 2>&1 && update-rc.d -f rkcan remove >/dev/null 2>&1
        command -v chkconfig >/dev/null 2>&1 && chkconfig --del rkcan >/dev/null 2>&1
        rm -f "$f"
    fi
done
rm -f "$PREFIX/bin/rkcan" "$PREFIX/bin/rkcan-ctl" "$PREFIX/bin/rkcan-can-setup"
if [ $PURGE -eq 1 ]; then
    rm -rf /etc/rkcan
    echo "RKCAN removed (configuration deleted)."
else
    echo "RKCAN removed (configuration kept in /etc/rkcan; use --purge to delete)."
fi
