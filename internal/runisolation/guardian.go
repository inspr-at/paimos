// SPDX-License-Identifier: AGPL-3.0-only

package runisolation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Request comes from the authorized local controller, never from a remote
// prompt. The command and its children must stay foreground in their group.
// Credentials and command arguments are never written into the registry.
type Request struct {
	Root      string   `json:"root"`
	Owner     Owner    `json:"owner"`
	Directory string   `json:"directory"`
	Command   []string `json:"command"`
}

func (r Request) validate() error {
	if _, err := r.Owner.id(); err != nil {
		return err
	}
	if len(r.Root) > 4096 || len(r.Directory) > 4096 || len(r.Command) < 1 || len(r.Command) > 64 || r.Command[0] == "" {
		return errors.New("invalid bounded isolation request")
	}
	n := 0
	for _, arg := range r.Command {
		n += len(arg)
		if strings.ContainsRune(arg, 0) {
			return errors.New("invalid command argument")
		}
	}
	if n > 16384 {
		return errors.New("isolation command exceeds argument bound")
	}
	return nil
}

// Control starts a separate guardian, keeping its stdin as a lifetime pipe.
// Closing that pipe (also on a controller crash) cancels the guardian's owned
// command group. The guardian is deliberately outside the controller's group.
// helperArgs selects the executable's internal Guard entry point.
func Control(ctx context.Context, executable string, helperArgs []string, request Request, stdout, stderr io.Writer) error {
	if err := request.validate(); err != nil {
		return err
	}
	if !ownedprocess.TrackingSupported() {
		return errors.New("fixture ownership unsupported")
	}
	b, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(b) >= 32768 {
		return errors.New("encoded guardian request exceeds size bound")
	}
	cmd := exec.Command(executable, helperArgs...)
	if !ownedprocess.Configure(cmd) {
		return errors.New("guardian process isolation unsupported")
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer in.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	// There is exactly one waiter. EOF remains the only guardian stop signal;
	// neither cancellation nor recovery sends a signal to a saved guardian PID.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if _, err = in.Write(append(b, '\n')); err != nil {
		in.Close()
		return errors.Join(err, <-done)
	}
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		in.Close()
		return errors.Join(ctx.Err(), <-done)
	}
}

// Guard reads one bounded request followed by an open lifetime pipe. It owns
// the lease and a newly spawned process group through observed exit and final
// cleanup. A crashed guardian leaves reportable unknown evidence; Check never
// attempts to reconstruct signaling authority from it.
func Guard(ctx context.Context, control io.Reader, stdout, stderr io.Writer) error {
	reader := bufio.NewReaderSize(control, 32768)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return errors.New("missing or oversized guardian request")
	}
	var request Request
	if json.Unmarshal(line, &request) != nil {
		return errors.New("invalid guardian request")
	}
	if err = request.validate(); err != nil {
		return err
	}
	if !ownedprocess.TrackingSupported() {
		return errors.New("fixture ownership unsupported")
	}
	lease, err := Acquire(ctx, request.Root, request.Owner)
	if err != nil {
		return err
	}
	confirmed := true // before Start, no fixture has been launched
	defer func() {
		if !lease.closed {
			_ = lease.Finish(confirmed)
		}
	}()
	ownedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// No unbounded reads or accumulation. Any further byte or EOF ends the
		// controller lease; only the initial request is part of this protocol.
		_, _ = reader.ReadByte()
		cancel()
	}()
	cmd := exec.Command(request.Command[0], request.Command[1:]...)
	cmd.Dir = request.Directory
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = resourceEnvironment(os.Environ(), lease.Environment())
	lease.HandoffPorts()
	process, err := ownedprocess.Start(ownedCtx, cmd)
	if err != nil {
		return errors.Join(err, lease.Finish(true))
	}
	confirmed = false
	if err = lease.Running(cmd.Process.Pid); err != nil {
		cancel()
		waitErr := process.Wait()
		confirmed = !errors.Is(waitErr, ownedprocess.ErrCleanupUnconfirmed)
		return errors.Join(err, waitErr, lease.Finish(confirmed))
	}
	err = process.Wait()
	confirmed = !errors.Is(err, ownedprocess.ErrCleanupUnconfirmed)
	return errors.Join(err, lease.Finish(confirmed))
}

func resourceEnvironment(inherited, resources []string) []string {
	owned := map[string]bool{}
	for _, entry := range resources {
		owned[strings.SplitN(entry, "=", 2)[0]] = true
	}
	result := make([]string, 0, len(inherited)+len(resources))
	for _, entry := range inherited {
		if !owned[strings.SplitN(entry, "=", 2)[0]] {
			result = append(result, entry)
		}
	}
	return append(result, resources...)
}
