// Package firecracker provides a Firecracker-backed implementation of
// sandbox.Manager. It boots one microVM per sandbox over the cold-boot path:
// kernel + initramfs + a read-only rootfs drive. Exec is not wired yet; that
// arrives with the vsock control plane in a later milestone.
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
	return nil
}

// vm is a live microVM handle.
type vm struct {
	sb        *sandbox.Sandbox
	machine   *fc.Machine
	cancel    context.CancelFunc
	stdinW    *os.File // write end of the console pipe; held open to keep init alive
	dir       string   // per-sandbox work dir
	vsockUDS  string   // host Unix socket for the guest vsock control plane
	vsockPort uint32   // guest vsock port emberd-init listens on
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
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	return &Manager{
		cfg:     cfg,
		logger:  logrus.NewEntry(logger),
		vms:     make(map[string]*vm),
		nextCID: firstGuestCID,
	}, nil
}

// Create boots a fresh microVM running the named language pack and returns its
// sandbox handle. An unknown pack name returns sandbox.ErrUnknownPack.
func (m *Manager) Create(ctx context.Context, languagePack string) (*sandbox.Sandbox, error) {
	pack, ok := m.cfg.Packs[languagePack]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrUnknownPack, languagePack)
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(m.cfg.WorkDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox dir: %w", err)
	}

	cleanup := func() { _ = os.RemoveAll(dir) }

	logFile, err := os.Create(filepath.Join(dir, "vm.log"))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create vm log: %w", err)
	}

	// A pipe whose write end we keep open feeds the guest serial console.
	// Firecracker reads from the read end; with no EOF, the guest shell blocks
	// instead of exiting, so the microVM stays alive until we tear it down.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("create console pipe: %w", err)
	}

	// The control plane runs over a Firecracker hybrid-vsock device: the host
	// connects through vsockUDS, the guest's emberd-init binds proto.GuestPort.
	cid := m.allocCID()
	vsockUDS := filepath.Join(dir, "vsock.sock")

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
			Path: vsockUDS,
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

	cmd := fc.VMCommandBuilder{}.
		WithBin(m.cfg.FirecrackerBin).
		WithSocketPath(socketPath).
		WithArgs([]string{"--id", fcID}).
		WithStdin(stdinR).
		WithStdout(logFile).
		WithStderr(logFile).
		Build(vmCtx)

	machine, err := fc.NewMachine(vmCtx, fcCfg,
		fc.WithLogger(m.logger.WithField("sandbox", id)),
		fc.WithProcessRunner(cmd),
	)
	if err != nil {
		cancel()
		stdinR.Close()
		stdinW.Close()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("new machine: %w", err)
	}

	if err := machine.Start(vmCtx); err != nil {
		cancel()
		stdinR.Close()
		stdinW.Close()
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("start machine: %w", err)
	}
	// Firecracker dup'd the read end into its own process; we no longer need it.
	stdinR.Close()

	sb := &sandbox.Sandbox{ID: id, LanguagePack: languagePack}

	m.mu.Lock()
	m.vms[id] = &vm{
		sb:        sb,
		machine:   machine,
		cancel:    cancel,
		stdinW:    stdinW,
		dir:       dir,
		vsockUDS:  vsockUDS,
		vsockPort: proto.GuestPort,
	}
	m.mu.Unlock()

	return sb, nil
}

// allocCID hands out a fresh guest context ID.
func (m *Manager) allocCID() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid := m.nextCID
	m.nextCID++
	return cid
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

	conn, err := proto.DialGuest(v.vsockUDS, v.vsockPort)
	if err != nil {
		return proto.ExecResult{}, fmt.Errorf("connect sandbox %s: %w", id, err)
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
	v.stdinW.Close()
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
