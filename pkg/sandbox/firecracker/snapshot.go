package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// artifactHash returns the first 12 hex chars of a sha256 over everything that
// makes a pack's template snapshot valid: the kernel image, the initramfs
// (which carries emberd-init), the pack's rootfs squashfs, the pack interpreter
// string, and the kernel args template. Any change to those must invalidate an
// existing snapshot, so all of them feed the hash. The digest is streamed file
// by file so hashing a ~400 MB squashfs never loads it fully into memory.
//
// The 12-char prefix is the directory name under SnapshotDir/<pack>/; a torn or
// stale snapshot lives under a different name and is cleaned at startup.
func (m *Manager) artifactHash(packName string) (string, error) {
	pack := m.cfg.Packs[packName]

	h := sha256.New()
	for _, p := range []string{m.cfg.KernelImagePath, m.cfg.InitrdPath, pack.RootfsPath} {
		if err := hashFile(h, p); err != nil {
			return "", err
		}
	}
	// String inputs are length-delimited so no concatenation of one input's
	// tail with the next can collide (e.g. interpreter "a"+args "b" vs "ab").
	fmt.Fprintf(h, "\x00interp=%d:%s", len(pack.Interpreter), pack.Interpreter)
	fmt.Fprintf(h, "\x00kargs=%d:%s", len(m.cfg.KernelArgs), m.cfg.KernelArgs)

	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// hashFile streams a file's contents into h.
func hashFile(h io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("hash artifact %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash artifact %s: %w", path, err)
	}
	return nil
}
