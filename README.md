# emberd

emberd is a Firecracker-based sandboxing runtime for executing AI-agent tool calls inside isolated microVMs. It is a local-first, open-source take on E2B / Modal-style sandboxes: create a sandbox, run code inside it, collect output, then destroy it.

## Status

Pre-alpha, but the full loop works: `POST /sandboxes` boots a real Firecracker microVM and blocks until the guest is actually serving (so an `exec` issued immediately after won't race the boot), `POST /sandboxes/{id}/exec` runs code inside it over a vsock control plane and returns stdout/stderr/exit, and `DELETE /sandboxes/{id}` tears it down. The guest boots a custom `emberd-init` initramfs (built by `rootfs/build.sh`) that, as PID 1, mounts the rootfs squashfs as an overlayfs lower layer with a tmpfs upper, `switch_root`s into the writable merged view, reaps orphaned child processes, and serves the control plane.

`language_pack` selects the rootfs and interpreter — `python` (runs `python3`) and `shell` (runs `/bin/sh`) ship by default. Still ahead: purpose-built minimal pack images (both packs currently share the dev rootfs), jailer/seccomp hardening, and snapshot restore for sub-100ms cold start.

## What emberd is

- A single-machine daemon for short-lived, isolated code execution.
- A KVM / Firecracker microVM runtime, not a container wrapper.
- A simple HTTP API for sandbox create, exec, and destroy operations.
- A foundation for fast sandbox startup through Firecracker snapshot restore.

## What emberd is not

- Not a Docker replacement.
- Not a fleet orchestrator or serverless platform.
- Not tied to Python long-term, though Python is the first planned language pack.
- Not designed for persistent storage or cross-sandbox communication.

## Architecture

- `cmd/emberd`: host-side HTTP daemon. Owns sandbox lifecycle, holds the live VM handles, and dispatches exec requests into guests over vsock.
- `cmd/emberd-init`: guest-side PID 1. Bootstraps the overlay root, reaps orphaned children, and serves the vsock control plane that runs submitted code under the language pack's interpreter.
- `pkg/api`: HTTP request / response types and route registration.
- `pkg/proto`: the host↔guest vsock wire protocol (length-prefixed JSON).
- `pkg/sandbox`: the sandbox lifecycle interface and its Firecracker-backed implementation (`pkg/sandbox/firecracker`).
- `rootfs/build.sh`: builds the guest initramfs (a static `emberd-init`).

The runtime path is a read-only squashfs language pack with a tmpfs overlay. Host-to-guest control traffic uses vsock, so v0.1 sandboxes run with no network device at all.

## Requirements

- Linux with KVM enabled and accessible.
- Go 1.26 or newer.
- Firecracker 1.15.x available on `PATH`.

## Build

```sh
go build -o bin/emberd ./cmd/emberd
go build -o bin/emberd-init ./cmd/emberd-init

# Build the guest initramfs (a static emberd-init that serves the control
# plane). Writes ~/firecracker-verify/emberd-initramfs.cpio by default.
./rootfs/build.sh
```

## Run

```sh
./bin/emberd
```

The daemon listens on `127.0.0.1:7777` by default. Override it with `--addr`:

```sh
./bin/emberd --addr 127.0.0.1:8888
```

The daemon stats the firecracker binary, kernel, initramfs, and rootfs at startup (defaults under `~/firecracker-verify/`) and exits if any are missing — a sandbox runtime that can't boot sandboxes has no useful degraded mode.

### Scripts

`scripts/serve.sh` builds and runs the daemon; `scripts/emberctl.sh` drives a running daemon. In one terminal:

```sh
./scripts/serve.sh                 # or: ADDR=127.0.0.1:7788 ./scripts/serve.sh
```

In another:

```sh
./scripts/emberctl.sh test            # full create -> inspect -> exec -> delete cycle with assertions
./scripts/emberctl.sh create          # boot a microVM, prints the sandbox id
./scripts/emberctl.sh exec <id> <code> # run code inside the sandbox (Python)
./scripts/emberctl.sh inspect <id>    # show the VM process + boot log
./scripts/emberctl.sh ls              # list running emberd microVMs
./scripts/emberctl.sh rm <id>         # destroy a sandbox
```

Both honor the `ADDR` env var (default `127.0.0.1:7777`).

### By hand

```sh
# Create a sandbox (boots a microVM):
curl -X POST http://127.0.0.1:7777/sandboxes
# {"id":"sb_..."}  (201)

# Run code inside it:
curl -X POST http://127.0.0.1:7777/sandboxes/sb_.../exec -d '{"code":"print(6*7)"}'
# {"stdout":"42\n","stderr":"","exit_code":0,"duration_ms":13}

# Destroy it:
curl -X DELETE http://127.0.0.1:7777/sandboxes/sb_...
# 204 No Content
```

## API

The HTTP surface is intentionally small:

- `POST /sandboxes`: create a sandbox (boots a microVM).
- `POST /sandboxes/{id}/exec`: execute code inside a sandbox.
- `DELETE /sandboxes/{id}`: destroy a sandbox.

Request and response shapes are shown in the "By hand" section above.

## Roadmap

- Purpose-built minimal language-pack squashfs images (both packs currently share the dev rootfs).
- Hardening: run under the Firecracker jailer, add seccomp filters, and enforce per-sandbox resource limits (CPU, memory, wall-clock, pids).
- Snapshot restore for sub-100ms sandbox startup: pre-warm a paused VM per pack, then restore-on-create with a fresh tmpfs overlay.
- Additional language packs (Node, etc.) and opt-in per-sandbox egress.

## License

MIT. See `LICENSE`.
