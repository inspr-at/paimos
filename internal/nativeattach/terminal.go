// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Terminal reads must return on context cancellation; callers own the files.
type Terminal interface {
	io.Writer
	Enter() error
	Restore() error
	Read(context.Context, []byte) (int, error)
	Size() (cols, rows int, err error)
	Interactive() bool
}

type FileTerminal struct {
	input       *os.File
	output      io.Writer
	state       *term.State
	interactive bool
}

func NewFileTerminal(input *os.File, output io.Writer) *FileTerminal {
	interactive := term.IsTerminal(int(input.Fd()))
	if f, ok := output.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		interactive = false
	}
	return &FileTerminal{input: input, output: output, interactive: interactive}
}

func (t *FileTerminal) Interactive() bool { return t.interactive }
func (t *FileTerminal) Enter() error {
	if !t.interactive {
		return nil
	}
	var err error
	t.state, err = term.MakeRaw(int(t.input.Fd()))
	if err == nil {
		_, err = io.WriteString(t.output, "\x1b[?2004h\x1b[>1u")
	}
	return err
}
func (t *FileTerminal) Restore() error {
	if t.state == nil {
		return nil
	}
	state := t.state
	t.state = nil
	_, outputErr := io.WriteString(t.output, "\x1b[?2004l\x1b[<u\r\n")
	return errors.Join(term.Restore(int(t.input.Fd()), state), outputErr)
}
func (t *FileTerminal) Write(b []byte) (int, error) { return t.output.Write(b) }
func (t *FileTerminal) Size() (int, int, error) {
	if !t.interactive {
		return 80, 24, nil
	}
	return term.GetSize(int(t.input.Fd()))
}

// Poll avoids an uninterruptible stdin goroutine. The short poll interval only
// bounds cancellation latency; correctness tests use barriers, never sleeps.
func (t *FileTerminal) Read(ctx context.Context, b []byte) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(t.input.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, errors.New("terminal input unavailable")
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return 0, errors.New("terminal input unavailable")
		}
		return t.input.Read(b)
	}
}
