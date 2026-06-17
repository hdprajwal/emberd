# emberd

Run code inside isolated microVMs with a single HTTP call.

emberd is a local daemon that boots [Firecracker](https://firecracker-microvm.github.io/) microVMs on demand, runs code inside them, and tears them down. Think E2B or Modal, but self-hosted and open source. It is built for giving AI agents a safe place to execute code.

> **Status: pre-alpha.** The full create/exec/delete loop works end to end. Hardening, snapshot restore, and purpose-built language-pack images are still ahead.

```sh
# Boot a sandbox, run code, tear it down.
curl -X POST localhost:7777/sandboxes -d '{"language_pack":"python"}'
# {"id":"sb_a1b2c3d4e5f6"}

curl -X POST localhost:7777/sandboxes/sb_a1b2c3d4e5f6/exec -d '{"code":"print(6*7)"}'
# {"stdout":"42\n","stderr":"","exit_code":0,"duration_ms":13}

curl -X DELETE localhost:7777/sandboxes/sb_a1b2c3d4e5f6
# 204 No Content
```

## Requirements

- **Linux** with KVM enabled (`/dev/kvm` must be readable/writable)
- **Go 1.26+**
- **Firecracker 1.15.x** on your `PATH`

No macOS or Windows support — Firecracker requires KVM.

## Setup

### 1. Install Firecracker

```sh
arch=$(uname -m)
FCVER=$(curl -fsSL https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest \
  | grep tag_name | cut -d'"' -f4)
mkdir -p ~/firecracker && cd ~/firecracker
curl -fsSL -o fc.tgz \
  "https://github.com/firecracker-microvm/firecracker/releases/download/${FCVER}/firecracker-${FCVER}-${arch}.tgz"
tar -xzf fc.tgz
mkdir -p ~/.local/bin
cp release-${FCVER}-${arch}/firecracker-${FCVER}-${arch} ~/.local/bin/firecracker
chmod +x ~/.local/bin/firecracker
```

Make sure `~/.local/bin` is on your `PATH`, then verify: `firecracker --version`.

### 2. Download the kernel and rootfs

emberd expects these under `~/firecracker-verify/` by default:

```sh
mkdir -p ~/firecracker-verify && cd ~/firecracker-verify
base="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64"
curl -fsSL -o vmlinux-6.1.155       "$base/vmlinux-6.1.155"
curl -fsSL -o ubuntu-24.04.squashfs "$base/ubuntu-24.04.squashfs"
```

### 3. Build

```sh
# Build the host daemon and guest agent
go build -o bin/emberd      ./cmd/emberd
go build -o bin/emberd-init ./cmd/emberd-init

# Pack emberd-init into the guest initramfs
# Writes ~/firecracker-verify/emberd-initramfs.cpio
./rootfs/build.sh
```

### 4. Run

```sh
./bin/emberd
# Daemon listening on 127.0.0.1:7777
```

Or use the helper script, which builds and runs in one step:

```sh
./scripts/serve.sh
```

emberd checks that all required files (Firecracker binary, kernel, initramfs, rootfs) exist at startup and exits with a clear error if anything is missing.

## Usage

### Using `emberctl`

The `scripts/emberctl.sh` script wraps the HTTP API for quick use from the terminal. With the daemon running in one terminal:

```sh
./scripts/emberctl.sh test                    # full lifecycle smoke test
./scripts/emberctl.sh create                  # boot a sandbox, print its id
./scripts/emberctl.sh exec <id> "print(6*7)"  # run Python inside it
./scripts/emberctl.sh exec <id> "echo hello"  # or shell
./scripts/emberctl.sh inspect <id>            # show VM process + boot log
./scripts/emberctl.sh ls                      # list running sandboxes
./scripts/emberctl.sh rm <id>                 # destroy a sandbox
```

Set `ADDR` to target a different port: `ADDR=127.0.0.1:8888 ./scripts/emberctl.sh test`

### Using the HTTP API directly

```sh
# Create a sandbox
curl -X POST localhost:7777/sandboxes -d '{"language_pack":"python"}'

# Run Python code
curl -X POST localhost:7777/sandboxes/<id>/exec \
  -d '{"code":"import sys; print(sys.version)"}'

# Run shell commands (use language_pack: "shell" at create time)
curl -X POST localhost:7777/sandboxes/<id>/exec \
  -d '{"code":"uname -r && df -h /"}'

# Destroy the sandbox
curl -X DELETE localhost:7777/sandboxes/<id>
```

## Language packs

A language pack is a rootfs + interpreter pair. Two ship by default:

| Pack | Interpreter | Create with |
|---|---|---|
| `python` | `python3` | `{"language_pack":"python"}` |
| `shell` | `/bin/sh` | `{"language_pack":"shell"}` |

Both currently share the Ubuntu verification squashfs. Purpose-built minimal images are on the roadmap.

## Examples

The `examples/` directory has five self-contained examples:

| Example | What it shows |
|---|---|
| `01-basic-exec` | Create, exec, destroy in Python and shell |
| `02-shell-exec` | Using the shell language pack |
| `03-state-persistence` | Filesystem writes surviving between exec calls |
| `04-error-handling` | Exit codes, stderr, timeouts |
| `05-openai-agent` | GPT-4o tool-calling agent backed by emberd |

See [examples/README.md](./examples/README.md) to get started, or the [full docs](https://emberd.hdprajwal.dev).

## How it works

Each sandbox is one Firecracker microVM. The guest boots emberd's own initramfs, mounts the language-pack squashfs as a read-only lower layer with a per-VM tmpfs overlay on top, and starts listening on a vsock port. The host daemon connects over vsock to send code and receive output. No network device is attached inside the VM.

```
client (HTTP)
    |
emberd daemon (host)
    |-- boots firecracker process
    |-- sends ExecRequest over vsock
    |
emberd-init (guest PID 1)
    |-- runs code under the language pack interpreter
    |-- returns stdout / stderr / exit_code
```

## Repo layout

```
cmd/emberd/          host daemon (HTTP server, sandbox lifecycle)
cmd/emberd-init/     guest PID 1 (rootfs bootstrap, vsock control plane)
pkg/api/             HTTP request/response types
pkg/proto/           host-guest wire protocol (length-prefixed JSON over vsock)
pkg/sandbox/         Manager interface + Firecracker-backed implementation
rootfs/build.sh      builds the guest initramfs
scripts/             serve.sh and emberctl.sh
examples/            runnable usage examples
eval/                safety evaluation harness
```

## Roadmap

- Purpose-built minimal squashfs images per language pack
- Firecracker jailer + seccomp hardening
- Per-sandbox resource limits (CPU, memory, wall-clock, pids)
- Snapshot restore for sub-100ms cold start
- Additional language packs (Node, etc.)
- Opt-in per-sandbox network egress

## License

MIT. See [LICENSE](./LICENSE).
