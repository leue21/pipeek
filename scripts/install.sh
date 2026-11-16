#!/bin/sh
# Installs or upgrades PiPeek from the latest GitHub release, as a systemd
# service listening on 127.0.0.1:8080:
#
#   curl -fsSL https://raw.githubusercontent.com/leue21/pipeek/main/scripts/install.sh | sh
#
# Environment:
#   PIPEEK_VERSION=v0.2.0   install that release instead of the latest
#   PIPEEK_NO_SERVICE=1     only install the binary; leave systemd alone
#   PIPEEK_BINDIR=dir       install the binary there (default /usr/local/bin)
#
# Existing systemd drop-ins (sudo systemctl edit pipeek) are kept.

set -eu

repo=leue21/pipeek
version=${PIPEEK_VERSION:-latest}
base=${PIPEEK_BASE_URL:-}   # for testing against a local dist/ directory
bindir=${PIPEEK_BINDIR:-/usr/local/bin}
unitdir=/etc/systemd/system

say() { printf '%s\n' "pipeek-install: $*"; }
die() { say "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "PiPeek runs on Linux only (this is $(uname -s))."

case $(uname -m) in
    aarch64 | arm64) arch=arm64 ;;
    armv7l | armv8l) arch=armv7 ;;
    armv6l) arch=armv6 ;;
    x86_64 | amd64) arch=amd64 ;;
    *) die "no prebuilt binary for $(uname -m); build from source instead: https://github.com/$repo#from-source" ;;
esac
# A 64-bit kernel with a 32-bit userland (older Raspberry Pi OS) reports
# aarch64, but a 32-bit system cannot run the arm64 binary.
if [ "$arch" = arm64 ] && command -v getconf >/dev/null 2>&1 && [ "$(getconf LONG_BIT)" = 32 ]; then
    arch=armv7
fi

if [ -z "$base" ]; then
    if [ "$version" = latest ]; then
        base="https://github.com/$repo/releases/latest/download"
    else
        base="https://github.com/$repo/releases/download/$version"
    fi
fi

if [ "$(id -u)" = 0 ]; then
    sudo=
elif command -v sudo >/dev/null 2>&1; then
    sudo=sudo
else
    die "run as root, or install sudo."
fi

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
else
    die "curl or wget is required."
fi

name="pipeek_linux_$arch"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

say "downloading $name.tar.gz ($version)"
fetch "$base/$name.tar.gz" "$work/$name.tar.gz" || die "download failed: $base/$name.tar.gz"
fetch "$base/SHA256SUMS" "$work/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

expected=$(grep " $name.tar.gz\$" "$work/SHA256SUMS" | cut -d' ' -f1)
actual=$(sha256sum "$work/$name.tar.gz" | cut -d' ' -f1)
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
    die "checksum mismatch for $name.tar.gz"
fi

tar -C "$work" -xzf "$work/$name.tar.gz"
"$work/$name/pipeek" -version >/dev/null 2>&1 || die "the $arch binary does not run on this machine."

# Install through a new file and rename, so a running pipeek is not disturbed
# until it is restarted.
$sudo install -d "$bindir"
$sudo install -m 0755 "$work/$name/pipeek" "$bindir/pipeek.new"
$sudo mv -f "$bindir/pipeek.new" "$bindir/pipeek"
say "installed $("$bindir/pipeek" -version) to $bindir/pipeek"

if [ -n "${PIPEEK_NO_SERVICE:-}" ]; then
    say "skipping systemd (PIPEEK_NO_SERVICE is set). Run: $bindir/pipeek -listen :8080"
    exit 0
fi
if ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
    say "systemd not found; start it yourself: $bindir/pipeek -listen :8080"
    exit 0
fi

$sudo install -m 0644 "$work/$name/deploy/pipeek.service" "$unitdir/pipeek.service"
$sudo systemctl daemon-reload
if systemctl is-active --quiet pipeek; then
    $sudo systemctl restart pipeek
    say "restarted the running service."
else
    $sudo systemctl enable --now pipeek
    say "enabled and started the pipeek service."
fi

# Report the address the service really uses: a drop-in may override -listen.
listen=$(systemctl show pipeek -p ExecStart --value | sed -n 's/.*-listen[ =]\([^ ;]*\).*/\1/p')
say "done. PiPeek listens on ${listen:-127.0.0.1:8080} (see 'systemctl cat pipeek')."
say "To reach it from your network, put it behind nginx or run: sudo systemctl edit pipeek"
