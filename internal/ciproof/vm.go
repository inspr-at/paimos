// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

var frameMagic = []byte("aeonci01")

func WriteFrame(out io.Writer, raw []byte) error {
	if len(raw) == 0 || len(raw) > maxGitOutput {
		return fmt.Errorf("VM input size limit")
	}
	if _, err := out.Write(frameMagic); err != nil {
		return err
	}
	if err := binary.Write(out, binary.BigEndian, uint64(len(raw))); err != nil {
		return err
	}
	if _, err := out.Write(raw); err != nil {
		return err
	}
	padding := (512 - (16+len(raw))%512) % 512
	_, err := out.Write(make([]byte, padding))
	return err
}

func ReadFrame(in io.Reader, limit int) ([]byte, error) {
	header := make([]byte, 16)
	if _, err := io.ReadFull(in, header); err != nil || !bytes.Equal(header[:8], frameMagic) {
		return nil, fmt.Errorf("invalid VM input frame")
	}
	size := binary.BigEndian.Uint64(header[8:])
	if size == 0 || size > uint64(limit) {
		return nil, fmt.Errorf("VM input frame limit")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(in, raw); err != nil {
		return nil, fmt.Errorf("truncated VM input frame")
	}
	return raw, nil
}

// Only controller-owned FD numbers appear here. Never add host mounts, writable
// disks, a socket, virtiofs/9p, networking, arbitrary args or candidate paths.
func vmArguments(p VMProfile) []string {
	return []string{
		"-no-user-config", "-nodefaults", "-display", "none", "-monitor", "none", "-serial", "stdio", "-nic", "none", "-no-reboot",
		"-machine", "q35,accel=kvm,dump-guest-core=off", "-cpu", "host", "-m", strconv.Itoa(p.MemoryMiB), "-smp", strconv.Itoa(p.CPUs),
		"-kernel", "/proc/self/fd/3", "-initrd", "/proc/self/fd/4", "-bios", "/proc/self/fd/8",
		"-append", "console=ttyS0 quiet loglevel=0 panic=-1 init=/aeon-init",
		"-drive", "file=/proc/self/fd/5,format=raw,if=virtio,readonly=on",
		"-drive", "file=/proc/self/fd/6,format=raw,if=virtio,readonly=on",
		"-drive", "file=/proc/self/fd/7,format=raw,if=virtio,readonly=on",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
	}
}
