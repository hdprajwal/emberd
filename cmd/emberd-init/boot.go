package main

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// rootDevice is the block device Firecracker exposes for the rootfs drive.
const rootDevice = "/dev/vda"

// newRoot is where the language-pack squashfs is mounted before we chroot in.
const newRoot = "/newroot"

// bootstrapPID1 runs the minimal init sequence when emberd-init is PID 1 inside
// the guest: bring up enough of the initramfs to see the root block device,
// mount the read-only language-pack squashfs, chroot into it, and re-create the
// pseudo-filesystems plus a writable /tmp inside the new root. After this the
// process is running with the language pack's interpreter and libraries on the
// usual paths.
//
// This is the cold-boot rootfs path; the tmpfs overlay and a proper switch_root
// are refinements left for later. A read-only squashfs root with tmpfs on /tmp
// is enough to run code: the interpreter and its libs come from the immutable
// base, scratch files go to /tmp.
func bootstrapPID1() error {
	// In the initramfs, mount the kernel filesystems we need. /dev (devtmpfs)
	// is what makes /dev/vda appear so we can mount the real root.
	if err := mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Printf("mount /proc (initramfs): %v", err)
	}
	if err := mount("dev", "/dev", "devtmpfs", 0, ""); err != nil {
		return fmt.Errorf("mount /dev (initramfs): %w", err)
	}

	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", newRoot, err)
	}
	if err := mount(rootDevice, newRoot, "squashfs", syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount rootfs %s -> %s: %w", rootDevice, newRoot, err)
	}

	if err := syscall.Chroot(newRoot); err != nil {
		return fmt.Errorf("chroot %s: %w", newRoot, err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Re-create the pseudo-filesystems inside the new root, plus a writable
	// tmpfs for scratch (the squashfs root is read-only).
	if err := mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Printf("mount /proc (rootfs): %v", err)
	}
	if err := mount("dev", "/dev", "devtmpfs", 0, ""); err != nil {
		log.Printf("mount /dev (rootfs): %v", err)
	}
	if err := mount("sys", "/sys", "sysfs", 0, ""); err != nil {
		log.Printf("mount /sys (rootfs): %v", err)
	}
	if err := mount("tmpfs", "/tmp", "tmpfs", 0, ""); err != nil {
		return fmt.Errorf("mount /tmp: %w", err)
	}

	// PID 1 starts with no environment; give interpreters a usable PATH (and a
	// HOME) so exec.LookPath resolves binaries from the language pack.
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	os.Setenv("HOME", "/root")
	return nil
}

func mount(source, target, fstype string, flags uintptr, data string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return syscall.Mount(source, target, fstype, flags, data)
}
