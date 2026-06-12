# emberd — Agent Contributor Guide

Guidance for AI coding agents working in this repository. For the human-facing overview, read [README.md](README.md) first; this file covers the conventions and guardrails a README leaves out.

emberd is a Firecracker-based sandboxing runtime that executes AI-agent tool calls inside isolated KVM microVMs: create a sandbox, run code in it, collect stdout/stderr/exit, destroy it. It is a local-first, single-machine take on E2B / Modal-style sandboxes — **not** a container wrapper, fleet orchestrator, or Docker replacement.

This file governs the Go runtime at the repo root. The docs site under `website/` has its own `website/AGENTS.md`; read the closest file to where you are working.

## Task completion requirements

Before considering any change complete, all of these must pass:

- `go test ./...` — hermetic unit suite (no KVM/Firecracker needed).
- `go vet ./...`
- `gofmt -l .` — must print nothing.

If you touched `emberd-init` or `pkg/proto`, also run `./scripts/emberctl.sh test`
in an environment with KVM + Firecracker to confirm a real boot → exec → destroy
cycle still works.

## Core priorities

1. **Isolation first.** Never trade away the sandbox boundary.
2. **Correctness and robustness.** Keep behavior predictable during failures —
   boot races, guest crashes, partial vsock frames, restarts — over short-term
   convenience.
3. **Fast cold start.** The project's reason for existing, but never ahead of 1
   or 2.

If a tradeoff is required, choose isolation and correctness over convenience.

## Maintainability

emberd is pre-alpha; sweeping changes that improve long-term maintainability are
encouraged. Before adding functionality, check whether shared logic can be
extracted into a module — duplicated logic across files is a code smell. Don't be
afraid to change existing code, and don't bolt on a local workaround to dodge a
refactor.

## Naming

- The project is **emberd** (lowercase), the host daemon binary is `emberd`, the guest PID 1 is `emberd-init`. Use those exact names.
- A unit of isolated execution is a **sandbox** (id prefix `sb_`), never a "container", "VM instance", or "box".
- The read-only guest image selected by `language_pack` is a **language pack** (`python`, `shell`), not a "runtime" or "image".

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/emberd` | Host-side HTTP daemon. Owns sandbox lifecycle, holds live VM handles, dispatches exec over vsock. |
| `cmd/emberd-init` | Guest-side PID 1. Bootstraps the overlayfs root, reaps orphans, serves the vsock control plane. |
| `pkg/api` | HTTP request/response types and route registration. |
| `pkg/proto` | Host↔guest vsock wire protocol (length-prefixed JSON). |
| `pkg/sandbox` | `Manager` lifecycle interface. |
| `pkg/sandbox/firecracker` | Firecracker-backed `Manager` implementation. |
| `rootfs/build.sh` | Builds the guest initramfs (a static `emberd-init`). |
| `scripts/` | `serve.sh` builds+runs the daemon; `emberctl.sh` drives a running one. |

## Environment setup

Requirements — verify these before assuming a failure is a code bug:

- Linux with KVM enabled and accessible (`/dev/kvm`).
- Go 1.26 or newer.
- Firecracker 1.15.x on `PATH`.

The daemon stats the firecracker binary, kernel, initramfs, and rootfs at startup (defaults under `~/firecracker-verify/`) and exits if any are missing. Run `./rootfs/build.sh` to produce the initramfs before booting sandboxes.

## Commands

Build:

```sh
go build -o bin/emberd ./cmd/emberd
go build -o bin/emberd-init ./cmd/emberd-init
./rootfs/build.sh          # guest initramfs -> ~/firecracker-verify/emberd-initramfs.cpio
```

Run:

```sh
./scripts/serve.sh                          # build + run daemon (ADDR overridable)
./bin/emberd --addr 127.0.0.1:7777          # default address

# From another terminal, drive a running daemon:
./scripts/emberctl.sh test                  # full create -> inspect -> exec -> delete cycle
./scripts/emberctl.sh exec <id> '<code>'
```

Test and lint (always run both before finishing a change):

```sh
go test ./...                               # unit suite; does NOT need KVM/Firecracker
go vet ./...
gofmt -l .                                  # must print nothing
```

There is no Makefile — drive everything with `go` and the scripts. The `go test ./...` suite is hermetic. `scripts/emberctl.sh test` is the end-to-end smoke test and **does** require KVM, Firecracker, and a built initramfs; only run it in an environment that has them.

## General guidance

- Match the surrounding code. Standard Go, `gofmt` (tabs), short lowercase package names.
- **Always** give exported identifiers doc comments.
- **Always** model new error cases as `Err*` sentinel vars (see `ErrNotFound`, `ErrUnknownPack` in `pkg/sandbox`) and compare with `errors.Is`.
- **Always** thread `context.Context` through lifecycle methods — follow the `Manager` interface (`Create`/`Exec`/`Delete`).
- **Prefer** extending an existing package over adding a new one; reach for a new `pkg/` only when the responsibility genuinely doesn't fit an existing one.
- **Never** widen the public HTTP surface (`pkg/api`) without a corresponding test and a note in the README's API section.

## Architecture boundaries

- `emberd-init` runs as **PID 1** inside the guest. It must reap orphaned children and must not assume a normal init, libc niceties, or dynamic linking are present. **Always** keep it static and dependency-light.
- The host↔guest contract lives entirely in `pkg/proto` and is length-prefixed JSON. **Never** change the wire format on one side only — the daemon and `emberd-init` decode each other's frames.
- v0.1 sandboxes boot a read-only squashfs language pack with a tmpfs overlay and **no network device**. **Never** add guest code that assumes networking, persistent storage, or cross-sandbox communication.
- A sandbox runtime that can't boot has no useful degraded mode. **Prefer** failing loudly at startup over limping along.

## Tests

- Co-locate tests as `*_test.go` next to the code (see `reaper_test.go`, `exec_test.go`, `proto_test.go`, `manager_test.go`).
- Cover the protocol and PID-1 logic with table-driven unit tests that run without a VM. Keep VM-dependent behavior behind the `emberctl.sh` smoke test.
- A bug fix should come with a regression test that fails before the fix.

## Commits and pull requests

- Write imperative, present-tense subject lines (e.g. "Reap orphaned children in emberd-init"), matching the existing history.
- **Never** add `Co-Authored-By:` trailers.
- Branch off `main`; keep changes scoped to one concern.
- Before opening a PR, ensure `go test ./...`, `go vet ./...`, and `gofmt -l .` are all clean.

## Boundaries

Ask first:

- Adding a new language pack, a new HTTP endpoint, or a third top-level binary.
- Introducing a network device, persistent volume, or any cross-sandbox state.
- Adding a third-party dependency to `emberd-init` (the static PID 1).

Never:

- Weaken sandbox isolation (drop the overlay, share the host filesystem, run guest code outside the microVM) to make something "work".
- Change `pkg/proto` framing on only one side of the host/guest boundary.
- Commit built artifacts — `bin/`, `*.cpio`, `*.ext4`, `vmlinux*` are gitignored; keep them that way.

## Review checklist

Before declaring a change done, confirm:

- [ ] `go test ./...`, `go vet ./...`, and `gofmt -l .` are clean.
- [ ] No change to `pkg/proto` framing without the matching host **and** guest update.
- [ ] `emberd-init` stays static and PID-1-safe (children reaped, no assumed init).
- [ ] No new guest networking, persistence, or cross-sandbox coupling.
- [ ] New errors are `Err*` sentinels; new exported symbols are documented.
- [ ] README/API docs updated if the HTTP surface changed.

## References

- [README.md](README.md) — overview, build, run, API examples.
- [website/AGENTS.md](website/AGENTS.md) — docs-site contributor guide.
- Firecracker docs: https://github.com/firecracker-microvm/firecracker
