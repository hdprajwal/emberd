// Package firecracker provides a Firecracker-backed implementation of
// sandbox.Manager. It boots one microVM per sandbox over the cold-boot path
// (kernel + initramfs + a read-only rootfs drive), runs submitted code in the
// guest over a vsock control plane, and tears the VM down on delete.
package firecracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"

	"github.com/hdprajwal/emberd/pkg/proto"
	"github.com/hdprajwal/emberd/pkg/sandbox"
)

// Pack is a language pack: the rootfs squashfs a sandbox boots and the
// interpreter emberd-init runs submitted code under inside it.
type Pack struct {
	// RootfsPath is the read-only squashfs mounted as the overlay lower layer.
	RootfsPath string
	// Interpreter is the guest-side executable that runs submitted code (e.g.
	// "python3", "/bin/sh"). Passed to emberd-init via the kernel command line.
	Interpreter string
	// WarmupCode is the trivial program the template VM runs once before it is
	// snapshotted, so every restored clone starts on the interpreter's warm
	// (already paged-in) path. Defaulted per pack in withDefaults: a Python
	// interpreter gets "print(1)", anything else "true".
	WarmupCode string
}

// Config describes the host artifacts and per-sandbox machine shape. Zero
// values fall back to the verified development defaults under ~/firecracker.
type Config struct {
	// FirecrackerBin is the path to the firecracker binary. If empty, it is
	// resolved from PATH, then ~/.local/bin/firecracker.
	FirecrackerBin string

	// KernelImagePath and InitrdPath point at the boot artifacts shared by all
	// packs.
	KernelImagePath string
	InitrdPath      string

	// Packs maps a language-pack name to its rootfs + interpreter. If empty,
	// defaults to "python" (python3) and "shell" (/bin/sh), both over the
	// verification rootfs.
	Packs map[string]Pack

	// KernelArgs is the base guest kernel command line; per-sandbox boot adds
	// the selected pack's interpreter.
	KernelArgs string

	// VcpuCount and MemSizeMib size each microVM.
	VcpuCount  int64
	MemSizeMib int64

	// WorkDir holds per-sandbox runtime state (API sockets, VM logs). If
	// empty, defaults to a temp directory under the OS temp dir.
	WorkDir string

	// BootTimeout bounds how long Create waits for the guest agent to come up
	// and start accepting on the vsock control plane before giving up and
	// tearing the microVM down. If zero, defaults to 15s.
	BootTimeout time.Duration

	// SnapshotDir stores per-pack template snapshots, laid out as
	// SnapshotDir/<pack>/<hash12>/{mem,state}. If empty, defaults to
	// WorkDir/snapshots.
	SnapshotDir string

	// PoolSize is the number of pre-warmed sandboxes kept ready per pack.
	// -1 disables the pool (Create restores directly from a snapshot); 0 means
	// "use the default" (3). The -1 sentinel is why 0 cannot double as disable.
	PoolSize int

	// SkipWarmOnStart controls whether New() builds missing snapshots (and, once
	// pools land, fills them) before returning. The zero value is "warm on
	// start"; set true to defer snapshot creation to the first Create() per pack.
	// Unit tests set it so New() needs no KVM when no snapshot exists yet.
	SkipWarmOnStart bool
}

func (c *Config) withDefaults() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	if c.FirecrackerBin == "" {
		if p, err := exec.LookPath("firecracker"); err == nil {
			c.FirecrackerBin = p
		} else {
			c.FirecrackerBin = filepath.Join(home, ".local", "bin", "firecracker")
		}
	}
	if c.KernelImagePath == "" {
		c.KernelImagePath = filepath.Join(home, "firecracker-verify", "vmlinux-6.1.155")
	}
	if c.InitrdPath == "" {
		// Built by rootfs/build.sh; boots straight into the emberd-init agent.
		c.InitrdPath = filepath.Join(home, "firecracker-verify", "emberd-initramfs.cpio")
	}
	if len(c.Packs) == 0 {
		// Both packs share the verification rootfs for now; only the interpreter
		// differs. A purpose-built minimal squashfs per language slots in here
		// later without touching the rest of the manager.
		rootfs := filepath.Join(home, "firecracker-verify", "ubuntu-24.04.squashfs")
		c.Packs = map[string]Pack{
			"python": {RootfsPath: rootfs, Interpreter: "python3"},
			"shell":  {RootfsPath: rootfs, Interpreter: "/bin/sh"},
		}
	}
	if c.KernelArgs == "" {
		c.KernelArgs = "console=ttyS0 reboot=k panic=1 pci=off"
	}
	if c.VcpuCount == 0 {
		c.VcpuCount = 1
	}
	if c.MemSizeMib == 0 {
		c.MemSizeMib = 256
	}
	if c.WorkDir == "" {
		c.WorkDir = filepath.Join(os.TempDir(), "emberd")
	}
	if c.SnapshotDir == "" {
		c.SnapshotDir = filepath.Join(c.WorkDir, "snapshots")
	}
	// PoolSize == 0 means "unset" → default 3; -1 is the explicit disable
	// sentinel and must survive defaulting untouched.
	if c.PoolSize == 0 {
		c.PoolSize = 3
	}
	if c.BootTimeout == 0 {
		c.BootTimeout = 15 * time.Second
	}
	// Default each pack's warm-up snippet. A Python interpreter runs "print(1)";
	// everything else (shells, etc.) runs "true". Map entries aren't addressable,
	// so reassign the whole Pack value.
	for name, p := range c.Packs {
		if p.WarmupCode == "" {
			if strings.Contains(p.Interpreter, "python") {
				p.WarmupCode = "print(1)"
			} else {
				p.WarmupCode = "true"
			}
			c.Packs[name] = p
		}
	}
	return nil
}

// vm is a live microVM handle.
type vm struct {
	sb        *sandbox.Sandbox
	machine   *fc.Machine
	cancel    context.CancelFunc
	dir       string // per-sandbox work dir
	vsockUDS  string // host Unix socket for the guest vsock control plane
	vsockPort uint32 // guest vsock port emberd-init listens on
}

// firstGuestCID is the lowest assignable guest context ID; 0-2 are reserved
// (hypervisor, local, host).
const firstGuestCID uint32 = 3

// Manager boots and tears down Firecracker microVMs.
type Manager struct {
	cfg    Config
	logger *logrus.Entry

	mu      sync.Mutex
	vms     map[string]*vm
	nextCID uint32

	// snapshots holds the one valid content-addressed template snapshot per pack
	// (pack name → paths). Populated at New() from disk or by createSnapshot, and
	// read on the restore path. Guarded by snapshotMu.
	snapshotMu sync.RWMutex
	snapshots  map[string]snapshotPaths
}

// snapshotPaths locates a pack's template snapshot on disk.
type snapshotPaths struct {
	memFile string // memory image
	vmState string // VM state file
}

// New validates the host artifacts and returns a ready Manager.
func New(cfg Config) (*Manager, error) {
	if err := cfg.withDefaults(); err != nil {
		return nil, err
	}
	artifacts := []string{cfg.FirecrackerBin, cfg.KernelImagePath, cfg.InitrdPath}
	seen := map[string]bool{}
	for _, p := range cfg.Packs {
		if !seen[p.RootfsPath] {
			seen[p.RootfsPath] = true
			artifacts = append(artifacts, p.RootfsPath)
		}
	}
	for _, p := range artifacts {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("required artifact missing: %s: %w", p, err)
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SnapshotDir, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	m := &Manager{
		cfg:       cfg,
		logger:    logrus.NewEntry(logger),
		vms:       make(map[string]*vm),
		nextCID:   firstGuestCID,
		snapshots: make(map[string]snapshotPaths),
	}

	// Register reusable snapshots, clean stale ones, and (unless warm-on-start is
	// skipped) build any that are missing. A build failure fails New — the same
	// contract as the artifact-existence check above.
	if err := m.loadOrBuildSnapshots(context.Background()); err != nil {
		return nil, err
	}

	return m, nil
}

// Create boots a fresh microVM running the named language pack and returns its
// sandbox handle. An unknown pack name returns sandbox.ErrUnknownPack.
func (m *Manager) Create(ctx context.Context, languagePack string) (*sandbox.Sandbox, error) {
	if _, ok := m.cfg.Packs[languagePack]; !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrUnknownPack, languagePack)
	}

	v, err := m.coldBoot(ctx, languagePack)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.vms[v.sb.ID] = v
	m.mu.Unlock()

	return v.sb, nil
}

// coldBoot boots a fresh microVM for packName over the cold-boot path (kernel +
// initramfs + rootfs drive) and returns its live handle. It does everything
// Create does except validating the pack name and registering the VM in m.vms,
// so snapshot creation and the restore-miss path can share the same boot code.
// packName must be a valid pack (callers validate).
//
// The guest vsock device is bound at the relative path "v.sock" and the
// Firecracker process runs with its per-sandbox dir as its working directory
// (cmd.Dir = dir). This is deliberate: Firecracker's vsock API is pre-boot only,
// so a snapshot bakes in whatever UDS path the template used; a relative path +
// per-VM cwd is the only way each restored VM gets its own socket without an
// (impossible) post-restore patch. vm.vsockUDS still records the absolute path
// because the host dials it directly.
func (m *Manager) coldBoot(ctx context.Context, packName string) (*vm, error) {
	pack := m.cfg.Packs[packName]

	id, err := newID()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(m.cfg.WorkDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create sandbox dir: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(dir) }

	logFile, err := os.OpenFile(filepath.Join(dir, "vm.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create vm log: %w", err)
	}

	// The control plane runs over a Firecracker hybrid-vsock device: the host
	// connects through vsockUDS, the guest's emberd-init binds proto.GuestPort.
	// The device Path is the relative "v.sock" so it stays valid inside any
	// restored VM's own cwd; vsockUDS is the absolute join for host-side dialing.
	cid := m.allocCID()
	vsockUDS := filepath.Join(dir, "v.sock")

	// The selected pack picks the rootfs and the guest interpreter; emberd-init
	// reads emberd.interpreter from /proc/cmdline.
	kernelArgs := m.cfg.KernelArgs + " emberd.interpreter=" + pack.Interpreter

	socketPath := filepath.Join(dir, "fc.sock")
	fcCfg := fc.Config{
		SocketPath:      socketPath,
		KernelImagePath: m.cfg.KernelImagePath,
		InitrdPath:      m.cfg.InitrdPath,
		KernelArgs:      kernelArgs,
		Drives: []models.Drive{{
			DriveID:      fc.String("rootfs"),
			PathOnHost:   fc.String(pack.RootfsPath),
			IsRootDevice: fc.Bool(true),
			IsReadOnly:   fc.Bool(true),
		}},
		VsockDevices: []fc.VsockDevice{{
			ID:   "ctrl",
			Path: "v.sock",
			CID:  cid,
		}},
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  fc.Int64(m.cfg.VcpuCount),
			MemSizeMib: fc.Int64(m.cfg.MemSizeMib),
		},
	}

	// The microVM must outlive the create request, so it gets its own context
	// cancelled only on Delete. Firecracker requires this context stay live for
	// the VM's whole lifetime.
	vmCtx, cancel := context.WithCancel(context.Background())

	// Firecracker's instance ID accepts only [A-Za-z0-9-]; the public sandbox
	// ID uses an underscore prefix, so sanitize it for the --id flag.
	fcID := strings.ReplaceAll(id, "_", "-")
	fcCfg.VMID = fcID

	// No stdin is wired: the guest serial console input goes to /dev/null. The
	// microVM stays alive because PID 1 (emberd-init) runs forever in its vsock
	// accept loop, not because a console pipe is held open.
	//
	// cmd.Dir pins the Firecracker process to this sandbox's dir so the relative
	// "v.sock" device path resolves to dir/v.sock — the per-VM host socket that a
	// snapshot restore also binds inside its own dir.
	cmd := fc.VMCommandBuilder{}.
		WithBin(m.cfg.FirecrackerBin).
		WithSocketPath(socketPath).
		WithArgs([]string{"--id", fcID}).
		WithStdout(logFile).
		WithStderr(logFile).
		Build(vmCtx)
	cmd.Dir = dir

	machine, err := fc.NewMachine(vmCtx, fcCfg,
		fc.WithLogger(m.logger.WithField("sandbox", id)),
		fc.WithProcessRunner(cmd),
	)
	if err != nil {
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("new machine: %w", err)
	}

	if err := machine.Start(vmCtx); err != nil {
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("start machine: %w", err)
	}

	// Block until the guest's emberd-init is past bootstrap and accepting on the
	// vsock control plane. Without this, an exec issued right after create races
	// the guest boot; with it, a returned sandbox is immediately usable.
	if err := waitReady(ctx, vsockUDS, proto.GuestPort, m.cfg.BootTimeout); err != nil {
		_ = machine.StopVMM()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = machine.Wait(stopCtx)
		stopCancel()
		cancel()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("sandbox %s: %w", id, err)
	}

	return &vm{
		sb:        &sandbox.Sandbox{ID: id, LanguagePack: packName},
		machine:   machine,
		cancel:    cancel,
		dir:       dir,
		vsockUDS:  vsockUDS,
		vsockPort: proto.GuestPort,
	}, nil
}

// allocCID hands out a fresh guest context ID.
func (m *Manager) allocCID() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid := m.nextCID
	m.nextCID++
	return cid
}

// waitReady polls the guest vsock control plane until it accepts a connection
// or timeout/ctx fires. A successful Firecracker hybrid-vsock handshake means
// emberd-init has finished bootstrap and is listening, so it is a precise
// readiness signal — no fixed sleep, no exec/boot race. Probes fail fast while
// the guest is still booting (Firecracker resets the connection when no guest
// port is listening), so the loop converges within a few interval ticks.
func waitReady(ctx context.Context, udsPath string, port uint32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	const interval = 50 * time.Millisecond
	var lastErr error
	for {
		conn, err := proto.DialGuest(udsPath, port)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return fmt.Errorf("guest control plane not ready after %s: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Exec dials the sandbox's emberd-init agent over vsock, sends the request, and
// returns the result.
func (m *Manager) Exec(ctx context.Context, id string, req proto.ExecRequest) (proto.ExecResult, error) {
	m.mu.Lock()
	v, ok := m.vms[id]
	m.mu.Unlock()
	if !ok {
		return proto.ExecResult{}, sandbox.ErrNotFound
	}
	return m.execVM(ctx, v, req)
}

// execVM runs one request against a specific VM handle over vsock. It is split
// out of Exec so the snapshot warm-up can drive an unregistered template VM
// (one that never enters m.vms). Each call opens and closes its own vsock
// connection, so no connection lingers open — important for the warm-up path,
// since a snapshot taken with an open vsock connection is invalid.
func (m *Manager) execVM(ctx context.Context, v *vm, req proto.ExecRequest) (proto.ExecResult, error) {
	conn, err := proto.DialGuest(v.vsockUDS, v.vsockPort)
	if err != nil {
		return proto.ExecResult{}, fmt.Errorf("connect sandbox %s: %w", v.sb.ID, err)
	}
	defer conn.Close()

	// Bound the round trip so a wedged guest can't hang the request: the guest
	// enforces req.TimeoutMs on the code itself, so allow that plus slack.
	wall := 60 * time.Second
	if req.TimeoutMs > 0 {
		wall = time.Duration(req.TimeoutMs)*time.Millisecond + 10*time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(wall))

	if err := proto.WriteMessage(conn, req); err != nil {
		return proto.ExecResult{}, fmt.Errorf("send exec request: %w", err)
	}
	var res proto.ExecResult
	if err := proto.ReadMessage(conn, &res); err != nil {
		return proto.ExecResult{}, fmt.Errorf("read exec result: %w", err)
	}
	return res, nil
}

// Delete tears down the microVM and releases its resources.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	v, ok := m.vms[id]
	if ok {
		delete(m.vms, id)
	}
	m.mu.Unlock()
	if !ok {
		return sandbox.ErrNotFound
	}

	// SIGTERM to firecracker triggers a clean VMM shutdown.
	stopErr := v.machine.StopVMM()

	// Bound the wait so a wedged guest can't hang Delete; cancelling the VM
	// context kills the process if the graceful stop stalled.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = v.machine.Wait(waitCtx)
	waitCancel()

	v.cancel()
	_ = os.RemoveAll(v.dir)

	if stopErr != nil {
		return fmt.Errorf("stop vmm: %w", stopErr)
	}
	return nil
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate sandbox id: %w", err)
	}
	return "sb_" + hex.EncodeToString(b[:]), nil
}
