// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type RunOptions struct {
	Platform  string
	InputMode string
	Resizes   <-chan os.Signal
}

type terminalRead struct {
	bytes []byte
	err   error
}

// Run owns only the terminal/viewer lifetime. Terminal restoration precedes
// bounded detach, including failures entering raw mode and cancelled signals.
// Resize redraws the local view; a structured viewer never resizes the host PTY.
func Run(ctx context.Context, s *Session, terminal Terminal, opts RunOptions) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	var readerDone, streamDone chan struct{}
	defer func() {
		cancel()
		if readerDone != nil {
			<-readerDone
		}
		if streamDone != nil {
			<-streamDone
		}
		restored := terminal.Restore()
		cleanup, finish := context.WithTimeout(context.Background(), 3*time.Second)
		detached := s.Detach(cleanup)
		finish()
		result = errors.Join(result, restored, detached)
	}()
	if err := terminal.Enter(); err != nil {
		return err
	}
	if s.control && !terminal.Interactive() {
		return errors.New("control attach requires an interactive terminal")
	}
	if opts.InputMode == "" {
		opts.InputMode = "now"
	}
	if opts.InputMode != "now" && opts.InputMode != "after_turn" {
		return errors.New("invalid input mode")
	}
	composer := &Composer{platform: opts.Platform}
	tail := &tail{}
	feedback := "Viewing only. Detach leaves the owned session running."
	if s.control {
		feedback = "Exclusive controller lease; accepted input is not delivery."
	}
	if !terminal.Interactive() {
		if _, err := fmt.Fprintln(terminal, "session "+s.binding.OwnedSessionID+" · read-only; EOF detaches the viewer"); err != nil {
			return err
		}
	}
	if err := draw(terminal, s, composer, tail, feedback); err != nil {
		return err
	}
	reads := make(chan terminalRead, 1)
	events := make(chan streamItem, 1)
	readerDone, streamDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 256)
		for {
			n, err := terminal.Read(ctx, buf)
			item := terminalRead{bytes: append([]byte(nil), buf[:n]...), err: err}
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
	// The reader receives an immutable copy before the foreground can renew
	// or observe new state. It never races with controller/snapshot changes.
	streamSession := *s
	go func() {
		defer close(streamDone)
		defer close(events)
		streamSession.readStream(ctx, func(item streamItem) bool {
			select {
			case events <- item:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	renew := s.clock.After(nativecontract.ControllerRenewSeconds * time.Second)
	var escape <-chan time.Time
	handle := func(action keyAction) (bool, error) {
		switch action {
		case keyDetach:
			return true, nil
		case keySubmit:
			r, err := s.Submit(ctx, composer.Text, opts.InputMode)
			if err != nil {
				feedback = err.Error()
				return false, err
			}
			feedback = receiptText(r)
			tail.put("", feedback, false)
			composer.Text = ""
		case keyInterrupt:
			r, err := s.Interrupt(ctx)
			if err != nil {
				feedback = err.Error()
				if errors.Is(err, ErrReadOnly) || errors.Is(err, ErrNoActiveTurn) {
					return false, nil
				}
				return false, err
			}
			feedback = receiptText(r)
			tail.put("", feedback, false)
		}
		return false, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case read := <-reads:
			if terminal.Interactive() {
				for _, b := range read.bytes {
					for _, action := range composer.feed([]byte{b}, s.control && !s.authorityLost) {
						done, err := handle(action)
						if err != nil {
							return err
						}
						if done {
							return nil
						}
					}
					if string(composer.pending) == "\x1b" {
						escape = s.clock.After(50 * time.Millisecond)
					}
				}
			}
			if read.err != nil {
				if errors.Is(read.err, io.EOF) || ctx.Err() != nil {
					return nil
				}
				return read.err
			}
		case <-escape:
			escape = nil
			done, err := handle(composer.flushEscape())
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case item, ok := <-events:
			if !ok {
				return errors.New("native stream ended; attach again explicitly")
			}
			if item.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if errors.Is(item.err, io.EOF) {
					return errors.New("native stream disconnected; delivery not confirmed")
				}
				return item.err
			}
			if err := s.observe(item.event); err != nil {
				return err
			}
			text := tail.event(item.event)
			if !terminal.Interactive() {
				if _, err := fmt.Fprintln(terminal, text); err != nil {
					return err
				}
			}
			if item.event.Kind == "gap" {
				return errors.New("live stream gap; attach again explicitly")
			}
			if item.event.Kind == "end" {
				return errors.New(text)
			}
			if s.authorityLost {
				feedback = "Control ended; detach and attach again explicitly."
			}
		case <-renew:
			if err := s.Renew(ctx); err != nil {
				return err
			}
			renew = s.clock.After(nativecontract.ControllerRenewSeconds * time.Second)
		case <-opts.Resizes:
			// No NativeResize request: this client is a structured viewer.
		}
		if err := draw(terminal, s, composer, tail, feedback); err != nil {
			return err
		}
	}
}
