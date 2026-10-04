// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package ciexecutor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/ciproof"
	"golang.org/x/sys/unix"
)

type outputBuffer struct{ raw []byte }

func (b *outputBuffer) Write(p []byte) (int, error) {
	if len(b.raw)+len(p) > 8<<20 {
		return 0, fmt.Errorf("guest process output limit")
	}
	b.raw = append(b.raw, p...)
	return len(p), nil
}

// Init is installed as /aeon-init in an independently pinned initrd. It is
// never invoked as a host helper or selected from a candidate source tree.
func Init() error {
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return fmt.Errorf("CI executor runs only as disposable guest PID 1")
	}
	for _, dir := range []string{"/proc", "/sys", "/dev", "/image"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("guest mount directory unavailable")
		}
	}
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"); err != nil {
		return fmt.Errorf("guest devices unavailable")
	}
	// Serial output is held by the root init only; candidate receives pipes and
	// sees a minimal chroot /dev without disks, serial or supervisor descriptors.
	console, err := os.OpenFile("/dev/ttyS0", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("guest terminal channel unavailable")
	}
	defer console.Close()
	readDisk := func(name string, limit int) ([]byte, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("readonly guest input unavailable")
		}
		defer f.Close()
		return ciproof.ReadFrame(f, limit)
	}
	raw, err := readDisk("/dev/vdb", 1<<20)
	if err != nil {
		return err
	}
	task, err := ciproof.DecodeVMTask(raw)
	if err != nil {
		return err
	}
	source, err := readDisk("/dev/vdc", maxSource)
	if err != nil {
		return err
	}
	if ciproof.RawDigest(source) != task.SourceDigest {
		return fmt.Errorf("guest source digest mismatch")
	}
	if err := unix.Mount("/dev/vda", "/image", "ext4", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("immutable executor root image unavailable")
	}
	for _, dir := range []string{"workspace", "tmp", "proc", "dev"} {
		// Empty mountpoints must already exist in the approved read-only image.
		info, err := os.Lstat(filepath.Join("/image", dir))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("approved guest mountpoint missing")
		}
	}
	for _, dir := range []string{"workspace", "tmp", "dev"} {
		mode := "mode=1777"
		if dir == "dev" {
			mode = "mode=0755"
		}
		if err := unix.Mount("tmpfs", filepath.Join("/image", dir), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, mode); err != nil {
			return fmt.Errorf("private guest workspace unavailable")
		}
	}
	if err := unix.Mount("proc", "/image/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "hidepid=2"); err != nil {
		return fmt.Errorf("private guest process view unavailable")
	}
	// Device nodes are the exception to MS_NODEV on the minimal /dev tmpfs.
	if err := unix.Mount("tmpfs", "/image/dev", "tmpfs", unix.MS_REMOUNT|unix.MS_NOSUID, "mode=0755"); err != nil {
		return fmt.Errorf("minimal guest devices unavailable")
	}
	for _, dev := range []struct {
		name  string
		minor uint32
	}{{"null", 3}, {"zero", 5}, {"random", 8}, {"urandom", 9}} {
		if err := unix.Mknod("/image/dev/"+dev.name, unix.S_IFCHR|0666, int(unix.Mkdev(1, dev.minor))); err != nil {
			return fmt.Errorf("minimal guest device unavailable")
		}
	}
	if err := unix.Chroot("/image"); err != nil {
		return fmt.Errorf("guest chroot unavailable")
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("guest root unavailable")
	}
	if err := ExtractSource(source, "/workspace/source"); err != nil {
		return err
	}
	for _, dir := range []string{"/workspace/home", "/workspace/go-cache", "/workspace/npm-cache"} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("guest cache directory unavailable")
		}
	}
	if err := filepath.Walk("/workspace", func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(name, 65534, 65534)
	}); err != nil {
		return fmt.Errorf("guest workspace ownership unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, task.Argv[0], task.Argv[1:]...)
	c.Dir = "/workspace/source"
	c.Env = GuestEnvironment()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{}}}
	c.WaitDelay = time.Second
	c.Cancel = func() error {
		if c.Process != nil {
			return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	var output outputBuffer
	c.Stdout = &output
	c.Stderr = &output
	exitCode := 0
	if err := c.Run(); err != nil {
		exitCode = 1
	}
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	manifest, err := Completion(task, output.raw, exitCode)
	if err != nil {
		return fmt.Errorf("approved guest execution did not complete its manifest")
	}
	artifacts, err := collectArtifacts("/workspace/source", task.ArtifactPaths)
	if err != nil {
		return err
	}
	if task.OutputArtifact != "" {
		artifacts = append(artifacts, ciproof.Artifact{Name: task.OutputArtifact, Digest: ciproof.RawDigest(output.raw), Data: output.raw})
	}
	if err := ciproof.ValidateArtifacts(artifacts); err != nil {
		return err
	}
	result := ciproof.GuestResult{Schema: ciproof.GuestSchema, PlanID: task.PlanID, ObligationID: task.ObligationID, ExecutorDigest: task.ExecutorDigest, ExitCode: exitCode, Manifest: manifest, OutputDigest: ciproof.RawDigest(output.raw), Artifacts: artifacts}
	if err := json.NewEncoder(console).Encode(result); err != nil {
		return fmt.Errorf("guest terminal result write failed")
	}
	if err := console.Sync(); err != nil && err != syscall.EINVAL {
		return fmt.Errorf("guest terminal result incomplete")
	}
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

// openat2 resolves every component beneath the private source root without
// symlinks or proc magic links, including during concurrent candidate mutation.
func collectArtifacts(root string, names []string) ([]ciproof.Artifact, error) {
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("guest artifact root unavailable")
	}
	defer unix.Close(fd)
	out := []ciproof.Artifact{}
	for _, name := range names {
		if !ciproof.SafeArtifactPath(name) {
			return nil, fmt.Errorf("unsafe guest artifact path")
		}
		artifactFD, err := unix.Openat2(fd, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return nil, fmt.Errorf("guest artifact must be a regular file beneath source root")
		}
		f := os.NewFile(uintptr(artifactFD), name)
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			f.Close()
			return nil, fmt.Errorf("guest artifact size/type limit")
		}
		raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
		f.Close()
		if err != nil || len(raw) > 4<<20 {
			return nil, fmt.Errorf("guest artifact read limit")
		}
		out = append(out, ciproof.Artifact{Name: filepath.Base(name), Digest: ciproof.RawDigest(raw), Data: raw})
		if err := ciproof.ValidateArtifacts(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
