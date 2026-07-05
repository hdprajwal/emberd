#!/usr/bin/env bash
#
# setup.sh — turn a fresh Linux + KVM box into a ready-to-run emberd, in one
# command. Installs Firecracker, fetches the kernel + rootfs, and builds the
# daemon, guest agent, and initramfs into the exact paths emberd looks for.
#
# Run it on the box itself (emberd is local-first, single-machine):
#
#   ./scripts/setup.sh
#   ./scripts/serve.sh        # then start the daemon
#
# Idempotent — safe to re-run. Skips work that's already done; use --force to
# redo downloads.
#
# Options:
#   --skip-firecracker   don't touch Firecracker (you manage it yourself)
#   --force              re-download kernel/rootfs and reinstall Firecracker
#   -h, --help           show this help
#
# Env overrides:
#   FC_VERSION   Firecracker tag to install (default: latest v1.15.x)
#   VERIFY_DIR   where artifacts live (default: ~/firecracker-verify — the
#                daemon's hardcoded default; change only if you also override
#                the daemon config to match)
#   INSTALL_DIR  where to install the firecracker binary (default: ~/.local/bin)
set -euo pipefail

# --- config (pinned to what the daemon expects) ------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERIFY_DIR="${VERIFY_DIR:-$HOME/firecracker-verify}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
GO_MIN="1.26"
KERNEL="vmlinux-6.1.155"
ROOTFS="ubuntu-24.04.squashfs"
INITRAMFS="emberd-initramfs.cpio"
CI_BASE="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64"
FC_FALLBACK="v1.15.0"   # used only if the GitHub API can't be reached

SKIP_FC=0
FORCE=0

log()  { printf '\033[1;34m[setup]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[setup:warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[setup:error]\033[0m %s\n' "$*" >&2; exit 1; }

# --- args --------------------------------------------------------------------
for arg in "$@"; do
	case "$arg" in
		--skip-firecracker) SKIP_FC=1 ;;
		--force)            FORCE=1 ;;
		-h|--help)          awk 'NR>1 && /^#/{sub(/^# ?/,"");print;next} NR>1{exit}' "$0"; exit 0 ;;
		*)                  die "unknown option: $arg (try --help)" ;;
	esac
done

# --- 0. preflight ------------------------------------------------------------
log "Preflight…"
arch="$(uname -m)"
[[ "$arch" == "x86_64" ]] || die "arch is $arch; emberd needs x86_64 (Firecracker artifacts are x86_64)."
[[ "$(uname -s)" == "Linux" ]] || die "emberd only runs on Linux (Firecracker requires KVM)."
[[ -r /dev/kvm && -w /dev/kvm ]] || die "/dev/kvm not read/writable. Enable KVM and ensure your user can access it (e.g. add yourself to the 'kvm' group)."

for tool in curl tar cpio go; do
	command -v "$tool" >/dev/null || die "'$tool' not found on PATH. Install it and re-run."
done

gov="$(go version | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1 | sed 's/go//')"
[[ -n "$gov" ]] || die "couldn't parse 'go version' output."
if [[ "$(printf '%s\n%s\n' "$GO_MIN" "$gov" | sort -V | head -1)" != "$GO_MIN" ]]; then
	die "Go >= ${GO_MIN} required, found ${gov}."
fi
log "  ok: Linux x86_64, /dev/kvm accessible, go ${gov}"

mkdir -p "$VERIFY_DIR" "$INSTALL_DIR"

# --- 1. install Firecracker --------------------------------------------------
if [[ "$SKIP_FC" == "1" ]]; then
	log "Skipping Firecracker (--skip-firecracker)."
	command -v firecracker >/dev/null || warn "firecracker not on PATH — the daemon will look at ${INSTALL_DIR}/firecracker."
elif [[ "$FORCE" != "1" ]] && command -v firecracker >/dev/null; then
	log "Firecracker already present: $(firecracker --version 2>/dev/null | head -1). Skipping (use --force to reinstall)."
else
	FC_VERSION="${FC_VERSION:-}"
	if [[ -z "$FC_VERSION" ]]; then
		log "Resolving latest Firecracker v1.15.x…"
		FC_VERSION="$(curl -fsSL 'https://api.github.com/repos/firecracker-microvm/firecracker/releases?per_page=100' 2>/dev/null \
			| grep -oE '"tag_name": *"v1\.15\.[0-9]+"' | grep -oE 'v1\.15\.[0-9]+' | head -1 || true)"
		FC_VERSION="${FC_VERSION:-$FC_FALLBACK}"
	fi
	log "Installing Firecracker ${FC_VERSION} -> ${INSTALL_DIR}/firecracker…"
	tmp="$(mktemp -d -t emberd-fc.XXXXXX)"
	trap 'rm -rf "$tmp"' EXIT
	curl -fsSL -o "$tmp/fc.tgz" \
		"https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-${arch}.tgz" \
		|| die "download of Firecracker ${FC_VERSION} failed. Set FC_VERSION to a valid tag and retry."
	tar -xzf "$tmp/fc.tgz" -C "$tmp"
	install -m0755 "$tmp/release-${FC_VERSION}-${arch}/firecracker-${FC_VERSION}-${arch}" "$INSTALL_DIR/firecracker"
	rm -rf "$tmp"; trap - EXIT
	log "  installed: $("$INSTALL_DIR/firecracker" --version | head -1)"
	case ":$PATH:" in
		*":$INSTALL_DIR:"*) : ;;
		*) warn "${INSTALL_DIR} is not on your PATH. The daemon falls back to it, but add it to PATH for 'firecracker' to work in your shell." ;;
	esac
fi

# --- 2. fetch kernel + rootfs ------------------------------------------------
fetch() {
	local name="$1" dest="$VERIFY_DIR/$1"
	if [[ "$FORCE" != "1" && -s "$dest" ]]; then
		log "  ${name} already present. Skipping."
		return
	fi
	log "  downloading ${name}…"
	curl -fsSL -o "$dest" "$CI_BASE/$name" || die "download of ${name} failed."
}
log "Fetching kernel + rootfs into ${VERIFY_DIR}…"
fetch "$KERNEL"
fetch "$ROOTFS"

# --- 3. build daemon, guest agent, initramfs ---------------------------------
log "Building bin/emberd and bin/emberd-init…"
( cd "$REPO_ROOT" && go build -o bin/emberd ./cmd/emberd && go build -o bin/emberd-init ./cmd/emberd-init )

log "Building guest initramfs -> ${VERIFY_DIR}/${INITRAMFS}…"
OUT="$VERIFY_DIR/$INITRAMFS" "$REPO_ROOT/rootfs/build.sh"

# --- 4. done -----------------------------------------------------------------
log "Done. emberd is set up."
echo
echo "  Artifacts in ${VERIFY_DIR}:"
echo "    - ${KERNEL}"
echo "    - ${ROOTFS}"
echo "    - ${INITRAMFS}"
echo "  Firecracker: $(command -v firecracker || echo "${INSTALL_DIR}/firecracker")"
echo
echo "  Start the daemon:   ./scripts/serve.sh"
echo "  Smoke test it:      ./scripts/emberctl.sh test   (from another terminal)"
