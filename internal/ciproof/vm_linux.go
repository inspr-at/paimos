// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package ciproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// checkInstallation checks every directory, not just the final filename. This
// refuses an otherwise read-only file in a candidate-writable parent or symlink.
func checkInstallation(p string) error {
	if !safeAbsolute(p) {
		return fmt.Errorf("invalid installation path")
	}
	for current := p; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("immutable installation unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || (current != p && !info.IsDir()) {
			return fmt.Errorf("installation must be root-owned and immutable to executor UID")
		}
		if current == "/" {
			break
		}
	}
	return nil
}

func openPinned(pin FilePin) (*os.File, error) {
	if err := checkInstallation(pin.Path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(pin.Path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("pinned installation unavailable")
	}
	f := os.NewFile(uintptr(fd), pin.Path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<30 {
		f.Close()
		return nil, fmt.Errorf("invalid pinned installation file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil || "sha256:"+hex.EncodeToString(h.Sum(nil)) != pin.Digest {
		f.Close()
		return nil, fmt.Errorf("immutable installation digest mismatch")
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("pinned installation read failed")
	}
	return f, nil
}

type vmOutput struct{ raw []byte }

// OpenAuthorityRepository is the installed CLI's stricter entrypoint. Policy,
// mirror and Git installation must be inaccessible to the executor UID.
func OpenAuthorityRepository(ctx context.Context, configPath, mirror string, git FilePin) (*Repository, error) {
	for _, name := range []string{configPath, mirror} {
		if err := checkInstallation(name); err != nil {
			return nil, err
		}
	}
	f, err := openPinned(git)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return OpenRepository(ctx, mirror, git.Path)
}

func (b *vmOutput) Write(p []byte) (int, error) {
	if len(b.raw)+len(p) > maxGuestOutput {
		return 0, fmt.Errorf("VM output size limit")
	}
	b.raw = append(b.raw, p...)
	return len(p), nil
}

func runVM(ctx context.Context, p VMProfile, task VMTask, source []byte) ([]byte, error) {
	if os.Geteuid() != 0 || p.UID == 0 || p.GID == 0 || os.Getenv("GITHUB_ACTIONS") != "" {
		return nil, fmt.Errorf("external Linux supervisor with separate executor UID required; Actions execution refused")
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	files := []*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	qemu, err := openPinned(p.QEMU)
	if err != nil {
		return nil, err
	}
	files = append(files, qemu)
	for _, pin := range []FilePin{p.Kernel, p.Initrd, p.RootFS} {
		f, err := openPinned(pin)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	// Private files are unlinked after opening. The child inherits read-only FDs,
	// with no host directory name or writable installation/control mount.
	inputFile := func(raw []byte) (*os.File, error) {
		f, err := os.CreateTemp("", "aeon-vm-input-")
		if err != nil {
			return nil, fmt.Errorf("private VM input unavailable")
		}
		name := f.Name()
		defer os.Remove(name)
		if err := WriteFrame(f, raw); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Chmod(0444); err != nil {
			f.Close()
			return nil, fmt.Errorf("readonly VM input unavailable")
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("VM input write failed")
		}
		return os.Open(name)
	}
	raw, _ := json.Marshal(task)
	for _, input := range [][]byte{raw, source} {
		f, err := inputFile(input)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	f, err := openPinned(p.Firmware)
	if err != nil {
		return nil, err
	}
	files = append(files, f)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, p.QEMU.Path, vmArguments(p)...)
	c.Dir = "/"
	c.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	c.ExtraFiles = files[1:]
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: p.UID, Gid: p.GID, Groups: []uint32{}}}
	c.WaitDelay = 2 * time.Second
	c.Cancel = func() error {
		if c.Process != nil {
			return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	var out vmOutput
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil || ctx.Err() != nil {
		return nil, fmt.Errorf("disposable VM failed, cancelled or timed out")
	}
	return out.raw, nil
}
