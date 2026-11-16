#!/bin/sh
# Builds PiPeek for every platform a release ships, and packages each build
# with the files needed to install it. CI runs this on every push and tag;
# running it by hand produces the same files:
#
#   scripts/build-release.sh v0.2.0 dist
#
# Writes one archive per platform and a SHA256SUMS file into the directory.

set -eu

if [ "$#" -ne 2 ]; then
    echo "usage: build-release.sh VERSION OUT_DIR" >&2
    exit 2
fi
version=$1
out=$2

# PiPeek reads /proc and /sys, so Linux only. Every build is static and
# cgo-free: one file runs on any distribution of that architecture.
#   arm64  64-bit Raspberry Pi OS (Pi 3, 4, 5, Zero 2)
#   armv7  32-bit Raspberry Pi OS on Pi 2 and newer
#   armv6  original Pi and Pi Zero / Zero W
targets="
amd64
arm64
arm:7
arm:6
"

go=${GO:-go}
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for target in $targets; do
    arch=${target%%:*}
    goarm=
    label=$arch
    case $target in
        *:*) goarm=${target#*:}; label=armv$goarm ;;
    esac
    # No version in the name: every release has the same file names, so
    # releases/latest/download/<name> is a link that never goes stale.
    # The binary itself says which release it is, with `pipeek -version`.
    name="pipeek_linux_${label}"
    stage="$work/$name"
    mkdir -p "$stage"

    (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch GOARM=$goarm \
        "$go" build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/pipeek" .)
    cp "$root/LICENSE" "$root/README.md" "$stage/"
    cp -r "$root/deploy" "$stage/deploy"

    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
    echo "built $name"
done

(cd "$out" && sha256sum pipeek_* > SHA256SUMS)
echo "wrote $out/SHA256SUMS"
