#!/usr/bin/env bash
# Build the emberd guest initramfs: a minimal cpio archive whose /init is a
# statically-linked emberd-init. At boot it mounts the language-pack squashfs
# (passed to Firecracker as the rootfs drive) read-only, chroots in, and serves
# the vsock control plane. The interpreter and libraries come from the squashfs;
# the initramfs carries only the agent.
#
# Output: $OUT (default ~/firecracker-verify/emberd-initramfs.cpio)
#
# Usage:
#   ./rootfs/build.sh
#   OUT=/path/to/initramfs.cpio ./rootfs/build.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${OUT:-$HOME/firecracker-verify/emberd-initramfs.cpio}"

command -v cpio >/dev/null || { echo "cpio not found on PATH" >&2; exit 1; }

STAGE="$(mktemp -d -t emberd-initramfs.XXXXXX)"
trap 'rm -rf "$STAGE"' EXIT

echo "building static emberd-init..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o "$STAGE/init" "$REPO_ROOT/cmd/emberd-init"

# Mountpoints emberd-init needs before/after chroot.
mkdir -p "$STAGE/proc" "$STAGE/sys" "$STAGE/dev" "$STAGE/newroot" "$STAGE/tmp"

echo "packing initramfs -> $OUT"
mkdir -p "$(dirname "$OUT")"
( cd "$STAGE" && find . -print0 | cpio --null --create --format=newc --quiet ) > "$OUT"

echo "done: $OUT ($(du -h "$OUT" | cut -f1))"
