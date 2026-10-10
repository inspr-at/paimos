// SPDX-License-Identifier: AGPL-3.0-only

// Package runisolation allocates local resources for explicitly opted-in runs.
// All workers on one host/account must use the same private root. Allocation
// is not admission authority, and never changes an existing agentd run.
package runisolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const maxRuns = 1024
const maxRecord = 8192

var ErrBusy = errors.New("isolation allocation busy")
var ErrRetained = errors.New("run resources already retained; use a new attempt ID")

// Owner binds an attempt to the selected tenant, account and host. RunID must
// include the assignment/attempt identity; retries never erase prior evidence.
type Owner struct {
	TenantID  string `json:"tenant_id"`
	AccountID string `json:"account_id"`
	HostID    string `json:"host_id"`
	RunID     string `json:"run_id"`
}

type Record struct {
	Version  int       `json:"version"`
	ID       string    `json:"id"`
	Owner    Owner     `json:"owner"`
	Database string    `json:"database"`
	Ports    []int     `json:"ports"`
	TempDir  string    `json:"temp_dir"`
	State    string    `json:"state"`              // reserved, running, stopped, unknown; incomplete in reports
	RootPID  int       `json:"root_pid,omitempty"` // informational; never signal authority
	Updated  time.Time `json:"updated_at"`
}

type Report struct {
	Record
	Orphan bool `json:"orphan"`
}

// Lease has one owner. Finish is called only after owned fixture cleanup has
// completed; an uncertain cleanup keeps the resource reservation indefinitely.
type Lease struct {
	Record Record
	root   *os.Root
	run    *os.Root
	lock   *os.File
	ports  []net.Listener
	closed bool
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var allocationID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (o Owner) id() (string, error) {
	for _, value := range []string{o.TenantID, o.AccountID, o.HostID, o.RunID} {
		if !identifier.MatchString(value) {
			return "", errors.New("owner identifiers must be 1..128 safe ASCII characters")
		}
	}
	b, _ := json.Marshal(o)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16]), nil
}

func openRoot(path string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("isolation root must be an absolute private directory")
	}
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("isolation root must be a private directory, not a symlink")
	}
	return os.OpenRoot(path)
}

// Acquire serializes allocations across processes, reserving two loopback TCP
// ports (application, Postgres), a unique SQL-safe database name and temp dir.
// Unknown/orphaned reservations remain excluded, even after their sockets close.
func Acquire(ctx context.Context, path string, owner Owner) (_ *Lease, resultErr error) {
	id, err := owner.id()
	if err != nil {
		return nil, err
	}
	root, err := openRoot(path, true)
	if err != nil {
		return nil, err
	}
	l := &Lease{root: root}
	defer func() {
		if resultErr != nil {
			l.closeHandles()
		}
	}()
	global, err := waitLock(ctx, root, "allocator.lock", true)
	if err != nil {
		return nil, err
	}
	defer global.Close()
	records, err := readRecords(root)
	if err != nil {
		return nil, err
	}
	if len(records) >= maxRuns {
		return nil, errors.New("isolation registry full; retained evidence requires operator archival")
	}
	for _, r := range records {
		if r.ID == id {
			return nil, ErrRetained
		}
	}
	if err = root.Mkdir(id, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrRetained
		}
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			// This call created the directory under allocator.lock and has not
			// returned a lease, so no fixture can own anything inside it yet.
			// Keep the global lock through cleanup; never remove retained runs.
			resultErr = errors.Join(resultErr, root.RemoveAll(id))
		}
	}()
	l.run, err = root.OpenRoot(id)
	if err != nil {
		return nil, err
	}
	l.lock, err = lockFile(l.run, "lease.lock", true)
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, r := range records {
		if r.State != "stopped" {
			for _, p := range r.Ports {
				used[p] = true
			}
		}
	}
	l.Record = Record{Version: 1, ID: id, Owner: owner, Database: "aeon_run_" + id, TempDir: filepath.Join(path, id, "tmp"), State: "reserved"}
	for attempts := 0; len(l.ports) < 2 && attempts < 64; attempts++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
		if listenErr != nil {
			return nil, listenErr
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if used[port] {
			listener.Close()
			continue
		}
		used[port] = true
		l.ports = append(l.ports, listener)
		l.Record.Ports = append(l.Record.Ports, port)
	}
	if len(l.ports) != 2 {
		return nil, errors.New("could not reserve distinct run ports")
	}
	if err = l.run.Mkdir("tmp", 0700); err != nil {
		return nil, err
	}
	if err = l.save(); err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return nil, err
	}
	return l, nil
}

// HandoffPorts releases the sockets immediately before an owned fixture binds
// them. The registry lease still excludes these numbers from other runs. Bind
// failures from unrelated software must fail the fixture, never reuse it.
func (l *Lease) HandoffPorts() {
	for _, p := range l.ports {
		p.Close()
	}
	l.ports = nil
}

func (l *Lease) Running(pid int) error {
	if l.closed || pid <= 0 {
		return errors.New("live fixture ownership required")
	}
	l.Record.State, l.Record.RootPID = "running", pid
	return l.save()
}

// Environment returns only value-free resource variables, never credentials.
// Callers replace any inherited entries with these exact values.
func (l *Lease) Environment() []string {
	return []string{"AEON_RUN_ID=" + l.Record.Owner.RunID, "AEON_RUN_DATABASE=" + l.Record.Database,
		"AEON_RUN_APP_PORT=" + strconv.Itoa(l.Record.Ports[0]), "AEON_RUN_POSTGRES_PORT=" + strconv.Itoa(l.Record.Ports[1]),
		"AEON_RUN_TEMP_DIR=" + l.Record.TempDir, "TMPDIR=" + l.Record.TempDir, "TMP=" + l.Record.TempDir, "TEMP=" + l.Record.TempDir}
}

func (l *Lease) Finish(confirmed bool) error {
	if l.closed {
		return errors.New("run lease already closed")
	}
	l.closed = true
	l.HandoffPorts()
	l.Record.State = "unknown"
	if confirmed {
		l.Record.State = "stopped"
	}
	err := l.save()
	l.closeHandles()
	return err
}

func (l *Lease) closeHandles() {
	l.HandoffPorts()
	if l.lock != nil {
		l.lock.Close()
	}
	if l.run != nil {
		l.run.Close()
	}
	if l.root != nil {
		l.root.Close()
	}
}

func (l *Lease) save() error {
	l.Record.Updated = time.Now().UTC()
	b, err := json.Marshal(l.Record)
	if err != nil {
		return err
	}
	f, err := l.run.OpenFile("record.next", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = l.run.Rename("record.next", "record.json"); err != nil {
		return err
	}
	dir, err := l.run.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// Check is report-only: no directory creation, signals, cleanup or PID lookup.
// A nonterminal record without a held lease is an orphan. Unknown fixtures stay
// visible and reserved even if their owner process can no longer be observed.
// Incomplete allocations retain their ID and evidence, without inventing owner
// or resource values that were never durably recorded.
func Check(ctx context.Context, path string) ([]Report, error) {
	root, err := openRoot(path, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	global, err := waitLock(ctx, root, "allocator.lock", false)
	if err != nil {
		return nil, err
	}
	defer global.Close()
	records, err := readRecords(root)
	if err != nil {
		return nil, err
	}
	reports := make([]Report, 0, len(records))
	for _, r := range records {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		run, openErr := root.OpenRoot(r.ID)
		if openErr != nil {
			return nil, openErr
		}
		lock, lockErr := lockFile(run, "lease.lock", false)
		if lock != nil {
			// Completion may have raced the initial scan. Re-read after taking
			// the released lease so a completed run is not a false orphan.
			fresh, readErr := readRecord(run, r.ID, path)
			if r.State == "incomplete" && errors.Is(readErr, os.ErrNotExist) {
				readErr = nil
			} else if readErr == nil {
				r = fresh
			}
			err = readErr
			lock.Close()
		}
		if r.State == "incomplete" && errors.Is(lockErr, os.ErrNotExist) {
			// A crash can happen before lease.lock is created. No held lease
			// exists in that case, but the directory remains operator evidence.
			lockErr = nil
		}
		run.Close()
		if err != nil {
			return nil, err
		}
		if lockErr != nil && !errors.Is(lockErr, ErrBusy) {
			return nil, lockErr
		}
		reports = append(reports, Report{Record: r, Orphan: r.State != "stopped" && lockErr == nil})
	}
	return reports, nil
}

func readRecords(root *os.Root) ([]Record, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxRuns + 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxRuns+1 {
		return nil, errors.New("isolation scan limit exceeded; report incomplete")
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "allocator.lock" {
			continue
		}
		if !entry.IsDir() || !allocationID.MatchString(entry.Name()) {
			return nil, errors.New("unexpected isolation registry entry")
		}
		run, err := root.OpenRoot(entry.Name())
		if err != nil {
			return nil, err
		}
		r, err := readRecord(run, entry.Name(), root.Name())
		run.Close()
		if errors.Is(err, os.ErrNotExist) {
			// A process crash bypasses Acquire's error cleanup. Count this
			// retained attempt toward maxRuns and refuse replay, while allowing
			// other attempts and report-only checks to continue.
			r, err = Record{ID: entry.Name(), State: "incomplete"}, nil
		}
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

func readRecord(run *os.Root, name, path string) (Record, error) {
	var r Record
	f, err := openRecord(run)
	if err != nil {
		return r, fmt.Errorf("incomplete retained allocation %s: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecord {
		return r, errors.New("invalid bounded allocation record file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err != nil {
		return r, err
	}
	if len(b) > maxRecord || json.Unmarshal(b, &r) != nil {
		return r, errors.New("invalid bounded allocation record")
	}
	id, idErr := r.Owner.id()
	if idErr != nil || r.Version != 1 || r.ID != name || r.ID != id || r.Database != "aeon_run_"+id || r.TempDir != filepath.Join(path, id, "tmp") || len(r.Ports) != 2 ||
		r.Ports[0] < 1 || r.Ports[0] > 65535 || r.Ports[1] < 1 || r.Ports[1] > 65535 || r.Ports[0] == r.Ports[1] ||
		(r.State != "reserved" && r.State != "running" && r.State != "stopped" && r.State != "unknown") {
		return Record{}, errors.New("allocation record identity or resource mismatch")
	}
	return r, nil
}
