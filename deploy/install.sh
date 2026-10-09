#!/bin/sh
# RKCAN installer. Run as root on the target board from the unpacked package:
#
#     tar xzf rkcan-*-linux-arm64.tar.gz && cd rkcan-*-linux-arm64 && sh install.sh
#
# Options:
#   --prefix DIR     install binaries to DIR/bin (default /usr/local)
#   --binary FILE    rkcan binary to install (default: from this package)
#   --init TYPE      systemd | sysv | none (default: auto-detect)
#   --no-start       install but do not start the service
#   -y               do not ask questions
set -e

PREFIX=/usr/local
CONFDIR=/etc/rkcan
BINARY=""
INIT=""
START=1

die() { echo "ERROR: $*" >&2; exit 1; }
info() { echo "==> $*"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --prefix) PREFIX=$2; shift 2 ;;
        --binary) BINARY=$2; shift 2 ;;
        --init) INIT=$2; shift 2 ;;
        --no-start) START=0; shift ;;
        -y) shift ;;
        -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
        *) die "unknown option: $1" ;;
    esac
done

[ "$(id -u)" = "0" ] || die "please run as root (sudo sh install.sh)"

SRC=$(cd "$(dirname "$0")" && pwd)
BINDIR=$PREFIX/bin

# ---- pick the binary for this machine
if [ -z "$BINARY" ]; then
    if [ -f "$SRC/rkcan" ]; then
        BINARY=$SRC/rkcan
    else
        case "$(uname -m)" in
            aarch64|arm64) arch=arm64 ;;
            armv7*|armv8l|armhf|arm) arch=arm32 ;;
            x86_64|amd64) arch=amd64 ;;
            *) die "unsupported CPU $(uname -m); pass --binary" ;;
        esac
        BINARY=$SRC/rkcan-linux-$arch
    fi
fi
[ -f "$BINARY" ] || die "binary not found: $BINARY"
chmod +x "$BINARY"
VERSION=$("$BINARY" -version 2>/dev/null) || die "$BINARY does not run on this machine (wrong CPU architecture?)"
info "Installing $VERSION"

# ---- init system
if [ -z "$INIT" ]; then
    if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
        INIT=systemd
    elif [ -d /etc/init.d ]; then
        INIT=sysv
    else
        INIT=none
    fi
fi
info "Init system: $INIT"

# ---- runtime dependency check (warn only)
command -v ip >/dev/null 2>&1 || echo "WARNING: 'ip' (iproute2) not found; CAN configuration will not work"
if command -v ip >/dev/null 2>&1 && ! ip -V 2>&1 | grep -qi iproute2; then
    echo "WARNING: 'ip' is not iproute2 (BusyBox ip cannot configure CAN bitrates)"
fi

# ---- stop a running instance
if [ "$INIT" = "systemd" ] && systemctl is-active --quiet rkcan 2>/dev/null; then
    info "Stopping running service"
    systemctl stop rkcan
elif [ "$INIT" = "sysv" ]; then
    for f in /etc/init.d/rkcan /etc/init.d/S99rkcan; do
        if [ -x "$f" ]; then "$f" stop >/dev/null 2>&1 || true; fi
    done
fi

subst() {
    sed -e "s#@BINDIR@#$BINDIR#g" -e "s#@CONFDIR@#$CONFDIR#g" "$1" >"$2.tmp" && mv "$2.tmp" "$2"
}

# ---- files
mkdir -p "$BINDIR" "$CONFDIR" || die "cannot create $BINDIR (read-only filesystem? try --prefix /userdata/rkcan)"
cp "$BINARY" "$BINDIR/rkcan.new" && chmod 755 "$BINDIR/rkcan.new" && mv "$BINDIR/rkcan.new" "$BINDIR/rkcan"
subst "$SRC/rkcan-can-setup" "$BINDIR/rkcan-can-setup" && chmod 755 "$BINDIR/rkcan-can-setup"
subst "$SRC/rkcan-ctl" "$BINDIR/rkcan-ctl" && chmod 755 "$BINDIR/rkcan-ctl"
info "Installed $BINDIR/rkcan, rkcan-ctl, rkcan-can-setup"

if [ -f "$CONFDIR/rkcan.conf" ]; then
    cp "$SRC/rkcan.conf" "$CONFDIR/rkcan.conf.new"
    info "Kept existing $CONFDIR/rkcan.conf (new defaults: rkcan.conf.new)"
else
    cp "$SRC/rkcan.conf" "$CONFDIR/rkcan.conf"
    info "Created $CONFDIR/rkcan.conf"
fi

case "$INIT" in
    systemd)
        subst "$SRC/rkcan.service" /etc/systemd/system/rkcan.service
        subst "$SRC/rkcan-can.service" /etc/systemd/system/rkcan-can.service
        systemctl daemon-reload
        systemctl enable rkcan-can.service rkcan.service >/dev/null 2>&1
        info "Enabled systemd services rkcan-can and rkcan"
        if [ $START -eq 1 ]; then
            systemctl restart rkcan-can.service rkcan.service
        fi
        ;;
    sysv)
        if command -v update-rc.d >/dev/null 2>&1; then
            SCRIPT=/etc/init.d/rkcan
            subst "$SRC/rkcan.init" "$SCRIPT" && chmod 755 "$SCRIPT"
            update-rc.d rkcan defaults >/dev/null 2>&1 || true
        elif command -v chkconfig >/dev/null 2>&1; then
            SCRIPT=/etc/init.d/rkcan
            subst "$SRC/rkcan.init" "$SCRIPT" && chmod 755 "$SCRIPT"
            chkconfig --add rkcan >/dev/null 2>&1 || true
        else
            # BusyBox/Buildroot: /etc/init.d/rcS runs S??* scripts in order
            SCRIPT=/etc/init.d/S99rkcan
            subst "$SRC/rkcan.init" "$SCRIPT" && chmod 755 "$SCRIPT"
        fi
        info "Installed init script $SCRIPT (starts at boot)"
        if [ $START -eq 1 ]; then
            "$SCRIPT" start
        fi
        ;;
    none)
        info "No init system detected: start manually with $BINDIR/rkcan"
        ;;
    *) die "unknown init type: $INIT" ;;
esac

PORT=$(sed -n 's/^RKCAN_PORT=//p' "$CONFDIR/rkcan.conf" | tail -n 1)
PORT=${PORT:-80}
IP=$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n 1)

echo
echo "------------------------------------------------------------"
echo " RKCAN installed."
echo "   Dashboard : http://${IP:-<board-ip>}$( [ "$PORT" = 80 ] || echo ":$PORT" )/"
echo "   Settings  : $CONFDIR/rkcan.conf   (rkcan-ctl config)"
echo "   Control   : rkcan-ctl start|stop|restart|status|log"
echo "   Uninstall : sh $SRC/uninstall.sh"
echo "------------------------------------------------------------"
