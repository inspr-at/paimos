// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package nativepty

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"golang.org/x/sys/unix"
)

// CommandPlan is produced by a separately qualified provider integration, not
// decoded from local/public input. Args include its exact official resume and
// session-only settings overlay. Environment is explicit: no ambient daemon
// environment or credential store is copied. The vendor binary is unmodified.
type CommandPlan struct {
	Args, Environment []string
	Provider          string
	Require           func(context.Context, nativecontract.PTYLaunchSpec) error
}
type CommandBuilder interface {
	Prepare(context.Context, nativecontract.PTYLaunchSpec) (CommandPlan, error)
}
type ExecLauncher struct{ Builder CommandBuilder }

var _ nativecontract.PTYLauncher = (*ExecLauncher)(nil)

// OpenPTY allocates a fresh controlling terminal and process session. A nonnil
// runtime returned WITH an error means a possible fork: retain it and occupancy.
// The operation deadline never becomes the runtime lifetime.
func (l *ExecLauncher) OpenPTY(ctx context.Context, spec nativecontract.PTYLaunchSpec) (nativecontract.PTYRuntime, error) {
	if l == nil || l.Builder == nil || !ownedprocess.TrackingSupported() {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	if !ValidSize(int64(spec.Cols), int64(spec.Rows)) || spec.Mode != "terminal" || spec.Lifetime == nil || spec.ResumeRef == "" {
		return nil, refuse(nativecontract.ReasonInvalidInput)
	}
	plan, err := l.Builder.Prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	if plan.Require == nil {
		return nil, refuse(nativecontract.ReasonQualificationMissing)
	}
	if err = boundedCommand(plan, spec); err != nil {
		return nil, err
	}
	if err = verifyBinary(spec.Profile.Binary); err != nil {
		return nil, err
	}
	master, slave, err := openPTY()
	if err != nil {
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	defer slave.Close()
	// File.Fd changes a pollable file to blocking mode. Keep the raw descriptor
	// via SyscallConn so cancellation and slow-PTY bounds remain effective.
	raw, err := master.SyscallConn()
	if err != nil {
		master.Close()
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	var masterFD int
	if err = raw.Control(func(fd uintptr) { masterFD = int(fd) }); err != nil {
		master.Close()
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	if err = unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(spec.Cols), Row: uint16(spec.Rows)}); err != nil {
		master.Close()
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	cmd := exec.Command(spec.Profile.Binary.Path, plan.Args...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.Cwd, plan.Environment, slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err = plan.Require(ctx, spec); err != nil {
		master.Close()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		master.Close()
		return nil, err
	}
	if err = spec.Lifetime.Err(); err != nil {
		master.Close()
		return nil, err
	}
	if !time.Now().Before(spec.Policy.ExpiresAt) {
		master.Close()
		return nil, refuse(nativecontract.ReasonGrantExpired)
	}
	if err = cmd.Start(); err != nil {
		master.Close()
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	now := time.Now()
	digest := sha256.Sum256([]byte(uuid.NewString()))
	r := &execPTY{master: master, masterFD: masterFD, binding: cloneBinding(spec.Binding), life: ownedprocess.Track(cmd), cmd: cmd,
		identity: nativecontract.NativeIdentity{Provider: plan.Provider, RuntimeEpoch: spec.Binding.RuntimeEpoch, NativeSessionRef: spec.ResumeRef, PID: cmd.Process.Pid, StartedAt: now, ProcessIdentityDigest: hex.EncodeToString(digest[:])},
		cols:     spec.Cols, rows: spec.Rows, frames: make(chan nativecontract.TerminalFrame, 8), done: make(chan struct{}), readerDone: make(chan struct{}), readStop: make(chan struct{})}
	verifyErr := r.life.Verify()
	go r.readFrames()
	go r.observe(spec.Lifetime)
	if verifyErr != nil {
		return r, refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return r, nil
}

func boundedCommand(p CommandPlan, s nativecontract.PTYLaunchSpec) error {
	if len(p.Args) > 128 || len(p.Environment) == 0 || len(p.Environment) > 128 || len(p.Provider) == 0 || len(p.Provider) > 32 {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	budget := 65536
	for _, list := range [][]string{p.Args, p.Environment} {
		for _, value := range list {
			if len(value) > 8192 || strings.IndexByte(value, 0) >= 0 {
				return refuse(nativecontract.ReasonInvalidInput)
			}
			budget -= len(value)
		}
	}
	if budget < 0 {
		return refuse(nativecontract.ReasonBufferFull)
	}
	seen := map[string]bool{}
	for _, value := range p.Environment {
		key, _, ok := strings.Cut(value, "=")
		if !ok || key == "" || seen[key] || strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") || strings.Contains(key, "TOKEN") || strings.Contains(key, "API_KEY") {
			return refuse(nativecontract.ReasonInvalidInput)
		}
		seen[key] = true
	}
	if !physicalDirectory(s.Cwd) || !physicalDirectory(s.Profile.AccountHome) || !physicalDirectory(s.Profile.WorkspaceRoot) || !within(s.Profile.WorkspaceRoot, s.Cwd) {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	return nil
}

// Hash and path checks do not execute the pin. Provider Require also performs
// its existing platform signature/OS fence qualification immediately before fork.
func verifyBinary(pin nativecontract.BinaryPin) error {
	if len(pin.Path) > 4096 || len(pin.SHA256) != 64 || pin.Version == "" || pin.Protocol == "" {
		return refuse(nativecontract.ReasonPinInvalid)
	}
	physical, err := filepath.EvalSymlinks(pin.Path)
	if err != nil || !filepath.IsAbs(pin.Path) || physical != pin.Path {
		return refuse(nativecontract.ReasonPinInvalid)
	}
	f, err := os.Open(pin.Path)
	if err != nil {
		return refuse(nativecontract.ReasonPinInvalid)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || info.Size() > 256<<20 {
		return refuse(nativecontract.ReasonPinInvalid)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (256<<20)+1))
	if err != nil || n > 256<<20 || hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return refuse(nativecontract.ReasonPinInvalid)
	}
	return nil
}
func physicalDirectory(path string) bool {
	if path == "" || len(path) > 4096 || !filepath.IsAbs(path) {
		return false
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

type execPTY struct {
	mu                         sync.Mutex
	master                     *os.File
	masterFD                   int
	binding                    nativecontract.Binding
	identity                   nativecontract.NativeIdentity
	life                       *ownedprocess.Lifetime
	cmd                        *exec.Cmd
	cols, rows                 int
	sequence                   uint64
	frames                     chan nativecontract.TerminalFrame
	done, readerDone, readStop chan struct{}
	exit                       nativecontract.ExitEvidence
	exitErr                    error
	closed                     bool
}

var _ nativecontract.PTYRuntime = (*execPTY)(nil)

func (r *execPTY) Identity() nativecontract.NativeIdentity     { return r.identity }
func (r *execPTY) Frames() <-chan nativecontract.TerminalFrame { return r.frames }

func (r *execPTY) check(ctx context.Context, binding nativecontract.Binding, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if !binding.Equal(r.binding) {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	if !time.Now().Before(expires) {
		return refuse(nativecontract.ReasonControllerExpired)
	}
	return nil
}
func (r *execPTY) Write(ctx context.Context, in nativecontract.PTYInput) (nativecontract.NativeEvidence, error) {
	if len(in.DataBase64) > base64.StdEncoding.EncodedLen(nativecontract.PTYInputDecodedBytes) {
		return nativecontract.NativeEvidence{}, refuse(nativecontract.ReasonBufferFull)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(in.DataBase64)
	if err != nil || len(data) == 0 || len(data) > nativecontract.PTYInputDecodedBytes {
		return nativecontract.NativeEvidence{}, refuse(nativecontract.ReasonInvalidInput)
	}
	defer clear(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = r.check(ctx, in.ExpectedBinding, in.ExpiresAt); err != nil {
		return nativecontract.NativeEvidence{}, err
	}
	for len(data) > 0 {
		if err = r.check(ctx, in.ExpectedBinding, in.ExpiresAt); err != nil {
			return nativecontract.NativeEvidence{}, err
		}
		n, writeErr := unix.Write(r.masterFD, data)
		if n > 0 {
			data = data[n:]
		}
		if writeErr == unix.EINTR {
			continue
		}
		if writeErr == unix.EAGAIN {
			if err = poll(ctx, r.masterFD, unix.POLLOUT); err == nil {
				continue
			}
			return nativecontract.NativeEvidence{}, err
		}
		if writeErr != nil || n == 0 {
			return nativecontract.NativeEvidence{}, refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	}
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, ObservedAt: time.Now()}, nil
}
func (r *execPTY) Resize(ctx context.Context, in nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	if !ValidSize(in.Cols, in.Rows) {
		return nativecontract.NativeEvidence{}, refuse(nativecontract.ReasonInvalidInput)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.check(ctx, in.ExpectedBinding, in.ExpiresAt); err != nil {
		return nativecontract.NativeEvidence{}, err
	}
	if err := unix.IoctlSetWinsize(r.masterFD, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(in.Cols), Row: uint16(in.Rows)}); err != nil {
		return nativecontract.NativeEvidence{}, refuse(nativecontract.ReasonNativeFailure)
	}
	r.cols, r.rows = int(in.Cols), int(in.Rows)
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, ObservedAt: time.Now()}, nil
}

func poll(ctx context.Context, fd int, events int16) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		n, err := unix.Poll(fds, 50) // bounds cancellation only; tests use barriers
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return refuse(nativecontract.ReasonNativeFailure)
		}
		if n > 0 {
			if fds[0].Revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
				return refuse(nativecontract.ReasonNativeFailure)
			}
			return nil
		}
	}
}

// This bounded channel is only the local taker's private terminal. It has no
// replay or browser export. Backpressure stops reads without dropping bytes or
// accumulating a transcript; the stream integration owns explicit viewer gaps.
func (r *execPTY) readFrames() {
	defer close(r.readerDone)
	defer close(r.frames)
	buffer := make([]byte, 4096) // base64 plus envelope remain below export bound
	defer clear(buffer)
	for {
		select {
		case <-r.readStop:
			return
		default:
		}
		r.mu.Lock()
		n, err := unix.Read(r.masterFD, buffer)
		cols, rows := r.cols, r.rows
		r.mu.Unlock()
		if n > 0 {
			r.sequence++
			frame := nativecontract.TerminalFrame{TerminalEpoch: r.binding.RuntimeEpoch, Sequence: strconv.FormatUint(r.sequence, 10), DataBase64: base64.StdEncoding.EncodeToString(buffer[:n]), Rows: int64(rows), Cols: int64(cols), Reset: r.sequence == 1}
			select {
			case r.frames <- frame:
			case <-r.readStop:
				return
			}
		}
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN {
			fds := []unix.PollFd{{Fd: int32(r.masterFD), Events: unix.POLLIN}}
			_, _ = unix.Poll(fds, 50)
			continue
		}
		if err != nil || n == 0 {
			return
		}
	}
}
func (r *execPTY) observe(lifetime context.Context) {
	wait := make(chan error, 1)
	go func() { wait <- r.life.WaitGroup() }()
	var err error
	select {
	case err = <-wait:
	case <-lifetime.Done():
		_ = r.life.Signal(true)
		err = <-wait
	}
	rootExited := r.cmd.ProcessState != nil && !errors.Is(err, ownedprocess.ErrCleanupUnconfirmed)
	// No signal after reaping. A reused/nonempty group is conservatively held.
	groupAbsent := rootExited && unix.Kill(-r.identity.PID, 0) == unix.ESRCH
	r.mu.Lock()
	r.closed = true
	r.exit = nativecontract.ExitEvidence{ProcessIdentityDigest: r.identity.ProcessIdentityDigest, RootExited: rootExited, GroupAbsent: groupAbsent, ObservedAt: time.Now(), Reason: "owned_pty_exit"}
	if !rootExited || !groupAbsent {
		r.exitErr = refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	r.mu.Unlock()
	close(r.readStop)
	<-r.readerDone
	_ = r.master.Close()
	close(r.done)
}
func (r *execPTY) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	select {
	case <-ctx.Done():
		return nativecontract.ExitEvidence{}, ctx.Err()
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.exit, r.exitErr
	}
}
func (r *execPTY) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	select {
	case <-r.done:
		return r.Wait(ctx)
	default:
	}
	if err := ctx.Err(); err != nil {
		return nativecontract.ExitEvidence{}, err
	}
	// Lifetime revokes signaling before the PID is reaped. No foreign PID or
	// saved checkpoint is used, and force is reserved for lifetime cancellation.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(nativecontract.LocalOperationSeconds * time.Second)
	}
	if err := r.life.SignalBefore(ctx, false, deadline); err != nil {
		// An exit racing the signal is resolved by the owned wait, never by
		// retrying a numeric PID after the lifetime fence was revoked.
		return r.Wait(ctx)
	}
	return r.Wait(ctx)
}
