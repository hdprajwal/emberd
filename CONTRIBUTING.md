# Contributing

## Setup

Follow the [development guide](docs/development.md) to get a working build:

1. Linux host with KVM enabled
2. Go 1.26+
3. Firecracker 1.15.x on your `PATH`

Build both binaries:

```sh
go build -o bin/emberd      ./cmd/emberd
go build -o bin/emberd-init ./cmd/emberd-init
./rootfs/build.sh
```

## Running tests

Unit tests have no KVM dependency and run anywhere:

```sh
go test ./...
```

## Opening issues

Use the issue templates — they keep bug reports and feature requests easy to triage. Check existing issues before opening a new one.

## Submitting a PR

- Keep changes focused. One thing per PR.
- Run `go vet ./...` and `go test ./...` before pushing.
- Write a clear PR description: what changed and why.

Small fixes can be sent directly. For larger changes, open an issue first to discuss the approach.
