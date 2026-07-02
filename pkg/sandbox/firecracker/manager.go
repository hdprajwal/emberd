// Package firecracker provides a Firecracker-backed implementation of
// sandbox.Manager. It boots one microVM per sandbox over the cold-boot path
// (kernel + initramfs + a read-only rootfs drive), runs submitted code in the
// guest over a vsock control plane, and tears the VM down on delete.
package firecracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// Boot-path labels reported by Info. They name the fast-boot strategy a Create
// takes given the manager's state at query time. The set is intentionally small
// and stable so downstream consumers (the bench env block) can rely on it.
const (
	// bootPathWarmPool: a warm pool is enabled, so Create pops a pre-warmed VM.
	bootPathWarmPool = "warm-pool"
	// bootPathSnapshotRestore: the pool is disabled but a template snapshot is
	// registered, so Create restores from it instead of a full cold boot.
	bootPathSnapshotRestore = "snapshot-restore"
	// bootPathColdBoot: the pool is disabled and no template snapshot is
	// registered (e.g. --skip-warm pointed at an empty snapshot dir), so a
	// Create boots a fresh microVM.
	bootPathColdBoot = "cold-boot"
)

// bootPath derives the fast-boot label from the state that actually decides a
// Create's path: the pool config and the registered-snapshot set (acquire's
// order of preference). It reports the state at query time, not a per-create
// trace — a cold-boot manager kicks off a lazy background snapshot build after
// its first Create, after which this honestly flips to "snapshot-restore".
// With the pool enabled the label is "warm-pool" even while an initial
// snapshot build or refill is still in flight: the pool is what serves creates
// in steady state under that config.
func (m *Manager) bootPath() string {
	if m.cfg.PoolSize > 0 {
		return bootPathWarmPool
	}
	m.snapshotMu.RLock()
	registered := len(m.snapshots) > 0
	m.snapshotMu.RUnlock()
	if registered {
		return bootPathSnapshotRestore
	}
	return bootPathColdBoot
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

// ErrManagerClosed is returned by Create after the Manager has been closed. A
// closed manager's refillCh is closed, so a create that tried to signal the
// refiller would panic; Create checks for closure first and returns this
// instead.
var ErrManagerClosed = errors.New("firecracker manager is closed")

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

	// pools holds pre-warmed VMs per pack (pack name → ready VMs). It is created
	// in New() and, per the concurrency story, is IMMUTABLE thereafter: every
	// pack's channel is allocated before any goroutine exists and the map is
	// never written again — only the channels inside it are pushed to / popped
	// from. That lets acquire read m.pools[pack] lock-free. When the pool is
	// disabled (PoolSize < 1) the map stays nil, so acquire's pool pop is a
	// guarded no-op (a receive on a nil channel would block forever).
	pools map[string]chan *vm

	// refillCh carries pack names that want their pool topped back up. Create()
	// does a non-blocking send after each acquire; the single runRefiller()
	// goroutine consumes it. Buffered (len(Packs)*4) so unconsumed signals never
	// block Create. Close() closes it to stop the refiller; the send is guarded
	// by closeMu so it can never race the close into a send-on-closed panic.
	refillCh chan string

	// closeMu guards closed and serialises Create's refillCh send against
	// Close()'s close of the same channel. closed flips true exactly once, in
	// Close(); Create checks it (and skips the send) under a read lock.
	closeMu sync.RWMutex
	closed  bool

	// refillerDone is closed by runRefiller when it returns. Close() waits on it
	// after closing refillCh so the refiller can never push a VM into a pool that
	// Close already drained (which would leak that VM). Nil when the pool is
	// disabled (no refiller runs). Set in New() before the refiller starts, so
	// Close()'s later read of it needs no lock.
	refillerDone chan struct{}

	// buildOnce guards the lazy background snapshot build: acquire's no-snapshot
	// path kicks off createSnapshot exactly once per pack so concurrent cold
	// boots never race to build the same snapshot. Guarded by buildMu.
	buildMu   sync.Mutex
	buildOnce map[string]*sync.Once
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
		refillCh:  make(chan string, len(cfg.Packs)*4),
		buildOnce: make(map[string]*sync.Once),
	}

	// Register reusable snapshots, clean stale ones, and (unless warm-on-start is
	// skipped) build any that are missing. A build failure fails New — the same
	// contract as the artifact-existence check above.
	if err := m.loadOrBuildSnapshots(context.Background()); err != nil {
		return nil, err
	}

	// Warm pool bring-up. Allocate every pack's channel now, before any goroutine
	// exists, and never touch m.pools again (see the field comment: immutable map,
	// mutable channels). PoolSize < 1 (the -1 sentinel) leaves m.pools nil and
	// disables the pool entirely.
	if cfg.PoolSize > 0 {
		m.pools = make(map[string]chan *vm, len(cfg.Packs))
		for name := range cfg.Packs {
			m.pools[name] = make(chan *vm, cfg.PoolSize)
		}
		// Warm-on-start: fill each pool synchronously so the first Create per pack
		// is a pool hit. Done before the refiller starts, so nothing else pushes to
		// these channels concurrently. Skipped under SkipWarmOnStart, where no
		// snapshot exists yet to restore from — the refiller fills the pool lazily
		// once the first cold boot builds the snapshot.
		if !cfg.SkipWarmOnStart {
			for name := range m.pools {
				m.refillPool(name)
			}
		}
		m.refillerDone = make(chan struct{})
		go m.runRefiller()
	}

	return m, nil
}

// Create boots a fresh microVM running the named language pack and returns its
// sandbox handle. An unknown pack name returns sandbox.ErrUnknownPack.
func (m *Manager) Create(ctx context.Context, languagePack string) (*sandbox.Sandbox, error) {
	if _, ok := m.cfg.Packs[languagePack]; !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrUnknownPack, languagePack)
	}

	m.closeMu.RLock()
	closed := m.closed
	m.closeMu.RUnlock()
	if closed {
		return nil, ErrManagerClosed
	}

	v, err := m.acquire(ctx, languagePack)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.vms[v.sb.ID] = v
	m.mu.Unlock()

	// Signal the refiller to top this pack back up. Held under closeMu's read lock
	// and re-checking closed so the non-blocking send can never race Close()'s
	// close(refillCh) into a send-on-closed-channel panic. Non-blocking so a full
	// (or, when the pool is disabled, unconsumed) refillCh never stalls Create.
	m.closeMu.RLock()
	if !m.closed {
		select {
		case m.refillCh <- languagePack:
		default:
		}
	}
	m.closeMu.RUnlock()

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

	return m.launchVM(ctx, packName, func() (fc.Config, []fc.Opt) {
		// The selected pack picks the rootfs and the guest interpreter;
		// emberd-init reads emberd.interpreter from /proc/cmdline.
		kernelArgs := m.cfg.KernelArgs + " emberd.interpreter=" + pack.Interpreter

		return fc.Config{
			KernelImagePath: m.cfg.KernelImagePath,
			InitrdPath:      m.cfg.InitrdPath,
			KernelArgs:      kernelArgs,
			Drives: []models.Drive{{
				DriveID:      fc.String("rootfs"),
				PathOnHost:   fc.String(pack.RootfsPath),
				IsRootDevice: fc.Bool(true),
				IsReadOnly:   fc.Bool(true),
			}},
			// The control plane runs over a Firecracker hybrid-vsock device: the
			// host connects through vm.vsockUDS, the guest's emberd-init binds
			// proto.GuestPort. The device Path is the relative "v.sock" so it
			// stays valid inside any restored VM's own cwd.
			VsockDevices: []fc.VsockDevice{{
				ID:   "ctrl",
				Path: "v.sock",
				CID:  m.allocCID(),
			}},
			MachineCfg: models.MachineConfiguration{
				VcpuCount:  fc.Int64(m.cfg.VcpuCount),
				MemSizeMib: fc.Int64(m.cfg.MemSizeMib),
			},
		}, nil
	})
}

// launchVM is the boot spine shared by coldBoot and restoreFromSnapshot: fresh
// id + per-sandbox dir, vm.log, Firecracker process pinned to the dir via
// cmd.Dir, machine creation and start, then waitReady on the guest control
// plane — with full unwinding (StopVMM → bounded Wait → cancel → close log →
// remove dir) on every failure so no process or work dir outlives an error.
//
// makeCfg supplies the caller-specific fc.Config body plus any extra machine
// options (the restore path passes fc.WithSnapshot); launchVM fills in the
// per-VM SocketPath and VMID afterwards so callers cannot get them wrong. The
// restore path therefore returns a zero fc.Config — per the fast-boot spec the
// restore config carries only SocketPath and VMID, with drives, vsock, and
// machine shape all coming from snapshot state.
//
// The vsock UDS the host dials is always dir/v.sock: the device path is the
// relative "v.sock" (baked into snapshot state by the template) and cmd.Dir
// pins every Firecracker process — cold-booted or restored — to its own dir,
// giving each VM a distinct host socket without any post-boot vsock patching
// (Firecracker's vsock API is pre-boot only, so patching is impossible).
func (m *Manager) launchVM(ctx context.Context, packName string, makeCfg func() (fc.Config, []fc.Opt)) (*vm, error) {
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

	// Absolute join for host-side dialing of the guest's relative "v.sock".
	vsockUDS := filepath.Join(dir, "v.sock")
	socketPath := filepath.Join(dir, "fc.sock")

	fcCfg, extraOpts := makeCfg()
	fcCfg.SocketPath = socketPath

	// Firecracker's instance ID accepts only [A-Za-z0-9-]; the public sandbox
	// ID uses an underscore prefix, so sanitize it for the --id flag.
	fcID := strings.ReplaceAll(id, "_", "-")
	fcCfg.VMID = fcID

	// The microVM must outlive the create request, so it gets its own context
	// cancelled only on Delete. Firecracker requires this context stay live for
	// the VM's whole lifetime.
	vmCtx, cancel := context.WithCancel(context.Background())

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

	opts := append([]fc.Opt{
		fc.WithLogger(m.logger.WithField("sandbox", id)),
		fc.WithProcessRunner(cmd),
	}, extraOpts...)

	machine, err := fc.NewMachine(vmCtx, fcCfg, opts...)
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
	// the guest boot; with it, a returned sandbox is immediately usable. On the
	// restore path the guest listener survived the snapshot, so this converges
	// in a probe or two.
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
	// Stamp the host wall clock so a restored guest (resumed with the
	// snapshot's stale clock) can self-heal its time before running the code.
	req.HostTimeUnixNano = time.Now().UnixNano()

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

// Info reports the resolved sandbox configuration: the guest machine shape, the
// fast-boot strategy a Create takes, the registered language packs, and the host
// work directory. Static values come from the Config after New applied defaults;
// the boot-path label additionally consults the registered-snapshot state, so it
// matches what a Create actually does right now (see bootPath). The pack list is
// sorted for a stable response.
func (m *Manager) Info() sandbox.Info {
	packs := make([]string, 0, len(m.cfg.Packs))
	for name := range m.cfg.Packs {
		packs = append(packs, name)
	}
	sort.Strings(packs)
	return sandbox.Info{
		GuestRAMMiB: int(m.cfg.MemSizeMib),
		GuestVCPUs:  int(m.cfg.VcpuCount),
		BootPath:    m.bootPath(),
		Packs:       packs,
		WorkDir:     m.cfg.WorkDir,
	}
}

// runRefiller is the single goroutine that keeps pools topped up. It blocks on
// refillCh and, per signal, tops the named pack's pool back to PoolSize. It
// exits when Close() closes refillCh, so it never outlives the Manager.
func (m *Manager) runRefiller() {
	defer close(m.refillerDone)
	for packName := range m.refillCh {
		m.refillPool(packName)
	}
}

// refillPool restores VMs into packName's pool until it holds PoolSize of them.
// Each restore is pushed with a non-blocking send. The default branch that tears
// down a surplus VM is a defensive guard: it is unreachable under the current
// design, where refillPool's only callers are New (pre-refiller, warm-on-start)
// and the single refiller goroutine — never concurrently, so this goroutine is
// the pool's sole pusher and the length check above guarantees room for the
// send. It is kept cheap in case a future concurrent pusher appears, so a
// surplus VM is torn down rather than leaked. A restore error logs and abandons
// this round — the next claim re-signals refillCh, so the pool heals on the
// following Create without this goroutine spinning on a bad snapshot.
func (m *Manager) refillPool(packName string) {
	pool := m.pools[packName]
	if pool == nil {
		return
	}
	for len(pool) < m.cfg.PoolSize {
		v, err := m.restoreFromSnapshot(context.Background(), packName)
		if err != nil {
			m.logger.WithField("pack", packName).WithError(err).
				Warn("pool refill restore failed; abandoning round")
			return
		}
		select {
		case pool <- v:
		default:
			m.teardownVM(v)
			return
		}
	}
}

// teardownVM stops an unregistered VM's hypervisor and removes its work dir:
// StopVMM, a bounded Wait, cancel the VM context, then RemoveAll. It is the
// teardown for VMs that never enter m.vms — idle pooled VMs (drained by Close or
// dropped as refill surplus) and the snapshot template. Registered VMs go
// through Delete, which additionally unregisters them from m.vms.
func (m *Manager) teardownVM(v *vm) {
	_ = v.machine.StopVMM()
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = v.machine.Wait(waitCtx)
	cancel()
	v.cancel()
	_ = os.RemoveAll(v.dir)
}

// Close shuts the Manager down: it stops the refiller (by closing refillCh),
// drains every pool tearing each idle VM down, then deletes every live VM. After
// Close, Create returns ErrManagerClosed. Close is idempotent — a second call is
// a no-op — so both an explicit shutdown and a test's t.Cleanup can call it.
//
// NOTE (plan deviation): the spec sequences full Close() in step 7; the
// pool-drain half is pulled forward into this task so the pool tests never leak
// Firecracker processes or work dirs.
func (m *Manager) Close() error {
	m.closeMu.Lock()
	if m.closed {
		m.closeMu.Unlock()
		return nil
	}
	m.closed = true
	// Closing refillCh both stops the refiller (its range returns) and, together
	// with the closed flag checked under closeMu, guarantees no Create send can
	// still be in flight to a closed channel.
	close(m.refillCh)
	m.closeMu.Unlock()

	// Wait for the refiller to fully exit before draining, so it can't push a VM
	// into a pool after drainPool has emptied it (which would leak that VM). Nil
	// when the pool is disabled.
	if m.refillerDone != nil {
		<-m.refillerDone
	}

	// Drain idle pooled VMs. The refiller has stopped and Create's guarded send
	// can no longer fire, so nothing else touches these channels now.
	for _, pool := range m.pools {
		m.drainPool(pool)
	}

	// Delete every still-live VM. Snapshot ids under m.mu, then Delete outside the
	// lock (Delete takes m.mu itself).
	m.mu.Lock()
	ids := make([]string, 0, len(m.vms))
	for id := range m.vms {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var firstErr error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := m.Delete(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
		cancel()
	}
	return firstErr
}

// drainPool tears down every VM currently idle in pool, returning once the
// channel is empty. The non-blocking receive is the loop's exit condition; Close
// guarantees no concurrent pusher, so an empty read means fully drained.
func (m *Manager) drainPool(pool chan *vm) {
	for {
		select {
		case v := <-pool:
			m.teardownVM(v)
		default:
			return
		}
	}
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate sandbox id: %w", err)
	}
	return "sb_" + hex.EncodeToString(b[:]), nil
}
