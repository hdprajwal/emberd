#!/bin/sh
#
# install.sh — one-command install for emberd.
#
#   curl -fsSL https://emberd.hdprajwal.dev/install.sh | sh
#
# Clones the repo and hands off to scripts/setup.sh, which installs
# Firecracker, fetches the kernel + rootfs, and builds the daemon, guest
# agent, and initramfs. Idempotent: re-running fast-forwards the checkout
# and re-runs setup, which skips work that is already done.
#
# Nothing here needs root. Everything lands under your home directory:
# the checkout in EMBERD_DIR, the firecracker binary in ~/.local/bin, and
# the kernel/rootfs/initramfs in ~/firecracker-verify.
#
# Env overrides:
#   EMBERD_DIR   where to clone (default: ~/emberd)
#   EMBERD_REF   branch or tag to check out (default: main)
#
# Arguments are forwarded to setup.sh, so its flags work through the pipe:
#   curl -fsSL https://emberd.hdprajwal.dev/install.sh | sh -s -- --skip-firecracker

set -eu

REPO="${EMBERD_REPO:-https://github.com/hdprajwal/emberd.git}"
EMBERD_DIR="${EMBERD_DIR:-$HOME/emberd}"
EMBERD_REF="${EMBERD_REF:-main}"

log() { printf '\033[1;34m[emberd]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[emberd:error]\033[0m %s\n' "$*" >&2; exit 1; }

# --- preflight ---------------------------------------------------------------
# Only what has to pass before we are allowed to clone. setup.sh does the
# deeper checks (KVM access, Go version, curl/tar/cpio) once we are in the
# repo — no point duplicating them here and letting the two drift apart.
[ "$(uname -s)" = "Linux" ] || die "emberd only runs on Linux (Firecracker needs KVM)."
[ "$(uname -m)" = "x86_64" ] || die "arch is $(uname -m); emberd needs x86_64 (the Firecracker artifacts are x86_64)."
command -v git >/dev/null 2>&1 || die "'git' not found on PATH. Install it and re-run."

# --- fetch the source --------------------------------------------------------
if [ -d "$EMBERD_DIR/.git" ]; then
	log "Updating the checkout at ${EMBERD_DIR}…"
	git -C "$EMBERD_DIR" fetch --quiet origin "$EMBERD_REF" \
		|| die "could not fetch ${EMBERD_REF} from origin."
	git -C "$EMBERD_DIR" checkout --quiet "$EMBERD_REF" \
		|| die "could not check out ${EMBERD_REF}. Sort out the working tree in ${EMBERD_DIR} and re-run."
	# --ff-only so a dirty or diverged checkout stops here instead of being
	# silently rewritten under someone who has local work in it.
	git -C "$EMBERD_DIR" merge --ff-only --quiet "origin/${EMBERD_REF}" \
		|| die "${EMBERD_DIR} has diverged from origin/${EMBERD_REF}. Resolve it, or set EMBERD_DIR to install elsewhere."
elif [ -e "$EMBERD_DIR" ]; then
	die "${EMBERD_DIR} already exists and is not a git checkout. Move it aside, or set EMBERD_DIR to install elsewhere."
else
	log "Cloning emberd into ${EMBERD_DIR}…"
	git clone --quiet --branch "$EMBERD_REF" "$REPO" "$EMBERD_DIR" \
		|| die "clone failed."
fi

# --- hand off to setup -------------------------------------------------------
# cd into the checkout first. rootfs/build.sh passes an absolute path to
# `go build`, which Go resolves against the module of the *current* directory —
# so running setup from wherever the user happened to invoke curl fails with
# "outside main module". setup.sh documents itself as `./scripts/setup.sh`
# from the repo root, so run it the way it expects.
[ -x "$EMBERD_DIR/scripts/setup.sh" ] || die "scripts/setup.sh missing or not executable in ${EMBERD_DIR}."
log "Running setup…"
cd "$EMBERD_DIR"
./scripts/setup.sh "$@"

# --- done --------------------------------------------------------------------
echo
log "emberd is installed in ${EMBERD_DIR}."
echo
echo "  Start the daemon:   cd ${EMBERD_DIR} && ./scripts/serve.sh"
echo "  Smoke test it:      ./scripts/emberctl.sh test   (from another terminal)"
echo
