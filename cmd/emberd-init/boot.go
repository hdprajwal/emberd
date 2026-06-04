package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
)

// Mount layout used while bringing up the guest root.
const (
	// rootDevice is the block device Firecracker exposes for the rootfs drive.
	rootDevice = "/dev/vda"
	// lowerDir is the read-only language-pack squashfs (overlay lower layer).
	lowerDir = "/lower"
	// overlayDir holds the tmpfs that backs the overlay upper + work dirs.
	overlayDir = "/overlay"
	// newRoot is the merged overlay mount we switch_root into.
	newRoot = "/newroot"
)

// bootstrapPID1 runs the init sequence when emberd-init is PID 1 inside the
// guest. It builds the writable root the architecture calls for: the language
// pack squashfs as a read-only lower layer, a tmpfs upper for writes, an
// overlayfs merge of the two, then a switch_root into the merged view. After
// this the whole filesystem is writable scratch (discarded with the VM) while
// the base image stays immutable and shareable.
//
// pivot_root(2) is rejected on an initramfs, so the switch uses the busybox
// switch_root technique: move the merged root onto / with MS_MOVE, then chroot.
func bootstrapPID1() error {
	// In the initramfs, mount the kernel filesystems we need. devtmpfs on /dev
	// is what makes /dev/vda appear so we can mount the real root.
	if err := mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Printf("mount /proc (initramfs): %v", err)
	}
	if err := mount("dev", "/dev", "devtmpfs", 0, ""); err != nil {
		return fmt.Errorf("mount /dev (initramfs): %w", err)
	}
	if err := mount("sys", "/sys", "sysfs", 0, ""); err != nil {
		log.Printf("mount /sys (initramfs): %v", err)
	}

	// Lower layer: the immutable language-pack squashfs.
	if err := mount(rootDevice, lowerDir, "squashfs", syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount rootfs %s -> %s: %w", rootDevice, lowerDir, err)
	}

	// Upper + work dirs live on a fresh tmpfs (must share one filesystem).
	if err := mount("tmpfs", overlayDir, "tmpfs", 0, ""); err != nil {
		return fmt.Errorf("mount overlay tmpfs: %w", err)
	}
	upper := overlayDir + "/upper"
	work := overlayDir + "/work"
	for _, d := range []string{upper, work, newRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Merge lower (ro) + upper (rw) into the new root.
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upper, work)
	if err := syscall.Mount("overlay", newRoot, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay (%s): %w", opts, err)
	}

	// Carry the pseudo-filesystems into the new root before switching. MS_MOVE
	// relocates each mount (and its subtree) so we don't lose /dev etc.
	for _, d := range []string{"/proc", "/dev", "/sys"} {
		target := newRoot + d
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		if err := syscall.Mount(d, target, "", syscall.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move %s -> %s: %w", d, target, err)
		}
	}

	// switch_root: make newRoot the real root, then chroot into it.
	if err := syscall.Chdir(newRoot); err != nil {
		return fmt.Errorf("chdir %s: %w", newRoot, err)
	}
	if err := syscall.Mount(".", "/", "", syscall.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move new root onto /: %w", err)
	}
	if err := syscall.Chroot("."); err != nil {
		return fmt.Errorf("chroot: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// PID 1 starts with no environment; give interpreters a usable PATH (and a
	// HOME) so exec.LookPath resolves binaries from the language pack.
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	os.Setenv("HOME", "/root")
	return nil
}

// kernelParam returns the value of a key=value token on the kernel command
// line (/proc/cmdline), or "" if absent. /proc must already be mounted.
func kernelParam(key string) string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, tok := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(tok, prefix); ok {
			return v
		}
	}
	return ""
}

func mount(source, target, fstype string, flags uintptr, data string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return syscall.Mount(source, target, fstype, flags, data)
}
