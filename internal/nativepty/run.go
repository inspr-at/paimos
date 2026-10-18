// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/nativeattach"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

const ResizeDebounce = 150 * time.Millisecond

type RunOptions struct{ Resizes <-chan os.Signal }
type terminalRead struct {
	data []byte
	err  error
}
type terminalEvent struct {
	event nativecontract.StreamEvent
	err   error
}

// Run forwards Esc and normal keys to the official TUI. Ctrl+] d detaches;
// Ctrl+] r explicitly hands back. EOF, signals and lost control only detach.
// Every path restores local terminal modes before bounded viewer cleanup.
func Run(ctx context.Context, c *TakeClient, terminal nativeattach.Terminal, options RunOptions) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	var readsDone, eventsDone chan struct{}
	defer func() {
		cancel()
		if readsDone != nil {
			<-readsDone
		}
		if eventsDone != nil {
			<-eventsDone
		}
		restored := terminal.Restore()
		if terminal.Interactive() {
			_, err := io.WriteString(terminal, "\x1b[?1049l\x1b[?25h\x1b[0m")
			restored = errors.Join(restored, err)
		}
		cleanup, finish := context.WithTimeout(context.Background(), 3*time.Second)
		detached := c.Detach(cleanup)
		finish()
		result = errors.Join(result, restored, detached)
	}()
	if !terminal.Interactive() {
		return errors.New("native takeover requires an interactive terminal")
	}
	cols, rows, err := terminal.Size()
	if err != nil {
		return err
	}
	cols, rows = boundedSize(cols, rows)
	if err = terminal.Enter(); err != nil {
		return err
	}
	if err = c.Begin(ctx, cols, rows); err != nil {
		return err
	}
	reads := make(chan terminalRead, 1)
	events := make(chan terminalEvent, 1)
	readsDone, eventsDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readsDone)
		data := make([]byte, 1024)
		defer clear(data)
		for {
			n, err := terminal.Read(ctx, data)
			item := terminalRead{data: append([]byte(nil), data[:n]...), err: err}
			select {
			case reads <- item:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	view, viewer := c.view, c.viewerID
	go func() {
		defer close(eventsDone)
		err := c.wire.Watch(ctx, view, viewer, func(event nativecontract.StreamEvent) error {
			select {
			case events <- terminalEvent{event: event}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		select {
		case events <- terminalEvent{err: err}:
		case <-ctx.Done():
		}
	}()
	renew := c.clock.After(nativecontract.ControllerRenewSeconds * time.Second)
	var resize <-chan time.Time
	prefix := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case read := <-reads:
			pending := make([]byte, 0, len(read.data)+1)
			for _, b := range read.data {
				if prefix {
					prefix = false
					if b == 'd' || b == 'r' {
						if len(pending) > 0 {
							if err = c.Write(ctx, pending); err != nil {
								return err
							}
						}
						if b == 'r' {
							return c.HandBack(ctx)
						}
						return nil
					}
					pending = append(pending, 0x1d)
					if b == 0x1d {
						continue
					}
				} else if b == 0x1d {
					prefix = true
					continue
				}
				pending = append(pending, b)
			}
			if len(pending) > 0 {
				if err = c.Write(ctx, pending); err != nil {
					return err
				}
			}
			clear(read.data)
			clear(pending)
			if read.err != nil {
				if errors.Is(read.err, io.EOF) || ctx.Err() != nil {
					return nil
				}
				return read.err
			}
		case item := <-events:
			if item.err != nil {
				c.lose()
				return item.err
			}
			if err = c.observe(item.event, terminal); err != nil {
				c.lose()
				return err
			}
		case <-renew:
			if err = c.Renew(ctx); err != nil {
				return err
			}
			renew = c.clock.After(nativecontract.ControllerRenewSeconds * time.Second)
		case _, ok := <-options.Resizes:
			if !ok {
				options.Resizes = nil
				continue
			}
			resize = c.clock.After(ResizeDebounce)
		case <-resize:
			resize = nil
			cols, rows, err = terminal.Size()
			if err != nil {
				return err
			}
			cols, rows = boundedSize(cols, rows)
			if err = c.Resize(ctx, cols, rows, false); err != nil {
				return err
			}
		}
	}
}
func boundedSize(cols, rows int) (int, int) {
	return max(MinCols, min(MaxCols, cols)), max(MinRows, min(MaxRows, rows))
}
func (c *TakeClient) observe(event nativecontract.StreamEvent, output io.Writer) error {
	if !event.Binding.Equal(c.binding) || event.Generation != c.view.Generation {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	switch event.Kind {
	case "terminal":
		var frame nativecontract.TerminalFrame
		if json.Unmarshal(event.Payload, &frame) != nil || frame.TerminalEpoch != c.binding.RuntimeEpoch || len(frame.DataBase64) > base64.StdEncoding.EncodedLen(nativecontract.ExportedFrameBytes) {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		data, err := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
		if err != nil {
			return refuse(nativecontract.ReasonInvalidInput)
		}
		defer clear(data)
		n, err := output.Write(data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
	case "controller":
		var controller nativecontract.ControllerView
		if json.Unmarshal(event.Payload, &controller) != nil || controller.ClientID != c.clientID || controller.Epoch != c.epoch {
			return refuse(nativecontract.ReasonControllerExpired)
		}
	case "snapshot":
		var view nativecontract.SessionView
		if json.Unmarshal(event.Payload, &view) != nil || view.Mode != "terminal" || view.State != "taken" || view.Controller == nil || view.Controller.ClientID != c.clientID || view.Controller.Epoch != c.epoch {
			return refuse(nativecontract.ReasonControllerExpired)
		}
	case "control.lease_expired", "control.ended_by", "end", "gap":
		return refuse(nativecontract.ReasonControllerExpired)
	}
	return nil
}
