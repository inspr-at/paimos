// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin && !aeon_test_unsupported

package nativepty

import (
	"bytes"
	"golang.org/x/sys/unix"
	"os"
	"unsafe"
)

func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	m := os.NewFile(uintptr(fd), "owned-pty")
	for _, operation := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err = unix.IoctlSetInt(fd, operation, 0); err != nil {
			m.Close()
			return nil, nil, err
		}
	}
	// Darwin's TIOCPTYGNAME writes char[128]; x/sys has no string-get helper.
	var name [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	if errno != 0 {
		m.Close()
		return nil, nil, errno
	}
	end := bytes.IndexByte(name[:], 0)
	if end <= 0 || !bytes.HasPrefix(name[:end], []byte("/dev/ttys")) {
		m.Close()
		return nil, nil, unix.EINVAL
	}
	slave, err := unix.Open(string(name[:end]), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, os.NewFile(uintptr(slave), "owned-pty-child"), nil
}
