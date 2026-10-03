// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CommandProvider is a bounded protocol adapter to the instance's existing,
// explicitly provisioned backup tool. No shell, default provider, credentials or
// production host is selected here. The tool must implement the Provider
// contract (durable keys/catalog, expiries, quotas, isolated restore and pins).
type CommandProvider struct{ Executable string }
type commandRequest struct {
	Action    string           `json:"action"`
	Operation *Operation       `json:"operation,omitempty"`
	Request   *ProviderRequest `json:"request,omitempty"`
	Instance  string           `json:"instance,omitempty"`
	Cursor    string           `json:"cursor,omitempty"`
	Limit     int              `json:"limit,omitempty"`
}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("provider response limit")
	}
	return b.Buffer.Write(p)
}
func (p CommandProvider) call(ctx context.Context, in commandRequest, out any) error {
	if !filepath.IsAbs(p.Executable) {
		return ErrPrerequisite
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("provider request exceeds bound")
	}
	cmd := exec.CommandContext(ctx, p.Executable, "aeon-adoption-v1")
	// A provider's descendants share our new group. Cancellation terminates
	// that group; WaitDelay also bounds inherited output descriptors when the
	// leader exits before its children. Retire remaining children on return.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	killGroup := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	defer func() {
		if cmd.Process != nil {
			_ = killGroup()
		}
	}()
	cmd.Stdin = bytes.NewReader(raw)
	response := &boundedBuffer{limit: 1 << 20}
	cmd.Stdout = response
	// No provider stderr becomes diagnostics, a transcript, or a job payload.
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return errors.New("backup provider command failed")
	}
	if err = strictJSON(response.Bytes(), out); err != nil {
		return errors.New("backup provider response failed validation")
	}
	return nil
}
func (p CommandProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	var out Capabilities
	err := p.call(ctx, commandRequest{Action: "capabilities"}, &out)
	return out, err
}
func (p CommandProvider) Lookup(ctx context.Context, op Operation) (ProviderResult, error) {
	var out ProviderResult
	err := p.call(ctx, commandRequest{Action: "lookup", Operation: &op}, &out)
	return out, err
}
func (p CommandProvider) Execute(ctx context.Context, in ProviderRequest) (ProviderResult, error) {
	var out ProviderResult
	err := p.call(ctx, commandRequest{Action: "execute", Request: &in}, &out)
	return out, err
}
func (p CommandProvider) List(ctx context.Context, instance, cursor string, limit int) (CatalogPage, error) {
	var out CatalogPage
	if limit < 1 || limit > 100 || len(cursor) > 512 {
		return out, errors.New("invalid provider catalog bounds")
	}
	err := p.call(ctx, commandRequest{Action: "list", Instance: instance, Cursor: cursor, Limit: limit}, &out)
	return out, err
}

func strictJSON(raw []byte, out any) error {
	// Reject duplicate keys before decode; DisallowUnknownFields alone silently
	// accepts a duplicate key and would weaken immutable evidence identities.
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting exceeds bound")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] || len(seen) >= 64 {
					return errors.New("invalid or duplicate JSON key")
				}
				seen[s] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	return nil
}

// FileReports keeps exactly a current and a latest-failed payload per project.
// A per-project OS lock and monotone lease generation fence multiple processes
// and stale workers. Files are private, bounded and replaced atomically; refs
// name only owned report files, never arbitrary paths or backup downloads.
type FileReports struct{ Root string }
type reportManifest struct {
	Generation int64  `json:"generation"`
	Current    string `json:"current"`
	Failed     string `json:"failed"`
}

var reportName = regexp.MustCompile(`^[1-9][0-9]*_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.report$`)

func (f FileReports) directory(identity Identity) (string, error) {
	if !filepath.IsAbs(f.Root) || !uuidRE.MatchString(identity.Tenant) || !uuidRE.MatchString(identity.Project) {
		return "", errors.New("invalid report storage identity")
	}
	dir := filepath.Join(f.Root, identity.Tenant, identity.Project)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New("private report storage unavailable")
	}
	for _, path := range []string{f.Root, filepath.Join(f.Root, identity.Tenant), dir} {
		st, err := os.Lstat(path)
		if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
			return "", errors.New("report storage permissions are not private")
		}
	}
	return dir, nil
}
func fileLock(ctx context.Context, dir string) (*os.File, error) {
	path := filepath.Join(dir, ".lock")
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return nil, errors.New("invalid report storage lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func readPrivate(path string, limit int) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > int64(limit) {
		return nil, errors.New("private report file failed bounds or ownership checks")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(raw) > limit {
		return nil, errors.New("report payload exceeds bound")
	}
	return raw, err
}
func manifest(dir string) (reportManifest, error) {
	var m reportManifest
	raw, err := readPrivate(filepath.Join(dir, "manifest.json"), 2048)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	err = strictJSON(raw, &m)
	return m, err
}
func writePrivate(path string, raw []byte) error {
	tmp := path + ".tmp"
	if st, err := os.Lstat(tmp); err == nil && !st.Mode().IsRegular() {
		return errors.New("unexpected report storage item")
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (f FileReports) Put(ctx context.Context, id Identity, raw []byte) (string, error) {
	if len(raw) > MaxReportBytes || id.Generation < 1 || !uuidRE.MatchString(id.Attempt) {
		return "", errors.New("report payload or generation exceeds bound")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	dir, err := f.directory(id)
	if err != nil {
		return "", err
	}
	lock, err := fileLock(ctx, dir)
	if err != nil {
		return "", err
	}
	defer unlock(lock)
	m, err := manifest(dir)
	if err != nil {
		return "", err
	}
	if id.Generation < m.Generation {
		return "", ErrLease
	}
	name := strconv.FormatInt(id.Generation, 10) + "_" + id.Attempt + ".report"
	if id.Generation == m.Generation && m.Current != "" && m.Current != name {
		return "", ErrLease
	}
	// Preflight before allocating: a leftover unexpected item cannot cause
	// every generation to write another payload before maintenance fails.
	if err = pruneReports(dir, m); err != nil {
		return "", err
	}
	if err = writePrivate(filepath.Join(dir, name), raw); err != nil {
		return "", err
	}
	m.Generation = id.Generation
	m.Current = name
	encoded, _ := json.Marshal(m)
	if err = writePrivate(filepath.Join(dir, "manifest.json"), encoded); err != nil {
		return "", err
	}
	if err = pruneReports(dir, m); err != nil {
		return "", err
	}
	return id.Tenant + "/" + id.Project + "/" + name, ctx.Err()
}

func pruneReports(dir string, m reportManifest) error {
	// Only recognized files created by this store are eligible for pruning.
	// Unexpected items stop maintenance; they are never deleted or renamed.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(128)
	d.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) >= 128 {
		return errors.New("report storage directory exceeds bound")
	}
	prune := []string{}
	for _, e := range entries {
		n := e.Name()
		if n == ".lock" || n == "manifest.json" || n == m.Current || n == m.Failed {
			continue
		}
		// Atomic writes may stop at write/sync/before rename. These are owned,
		// uncommitted artifacts; the committed manifest remains authoritative.
		temporary := n == "manifest.json.tmp" || strings.HasSuffix(n, ".tmp") && reportName.MatchString(strings.TrimSuffix(n, ".tmp"))
		if !reportName.MatchString(n) && !temporary {
			return errors.New("unexpected report storage item")
		}
		limit := MaxReportBytes
		if n == "manifest.json.tmp" {
			limit = 2048
		}
		if _, err = readPrivate(filepath.Join(dir, n), limit); err != nil {
			return err
		}
		prune = append(prune, n)
	}
	// Validate the whole bounded directory before removing any owned residue.
	for _, name := range prune {
		if err = os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
func (f FileReports) resolve(ref string) (Identity, string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || !uuidRE.MatchString(parts[0]) || !uuidRE.MatchString(parts[1]) || !reportName.MatchString(parts[2]) {
		return Identity{}, "", errors.New("invalid private report reference")
	}
	return Identity{Tenant: parts[0], Project: parts[1]}, parts[2], nil
}
func (f FileReports) Get(ctx context.Context, ref string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	id, name, err := f.resolve(ref)
	if err != nil {
		return nil, err
	}
	dir, err := f.directory(id)
	if err != nil {
		return nil, err
	}
	lock, err := fileLock(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	m, err := manifest(dir)
	if err != nil {
		return nil, err
	}
	if name != m.Current && name != m.Failed {
		return nil, ErrLease
	}
	return readPrivate(filepath.Join(dir, name), MaxReportBytes)
}
func (f FileReports) RetainFailed(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	id, name, err := f.resolve(ref)
	if err != nil {
		return "", err
	}
	dir, err := f.directory(id)
	if err != nil {
		return "", err
	}
	lock, err := fileLock(ctx, dir)
	if err != nil {
		return "", err
	}
	defer unlock(lock)
	m, err := manifest(dir)
	if err != nil {
		return "", err
	}
	if err = pruneReports(dir, m); err != nil {
		return "", err
	}
	if name == m.Failed {
		return ref, nil
	}
	if name != m.Current {
		return "", ErrLease
	}
	old := m.Failed
	m.Failed = name
	raw, _ := json.Marshal(m)
	if err = writePrivate(filepath.Join(dir, "manifest.json"), raw); err != nil {
		return "", err
	}
	if old != "" && old != name && old != m.Current {
		if !reportName.MatchString(old) {
			return "", errors.New("invalid retained report reference")
		}
		if err = os.Remove(filepath.Join(dir, old)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return ref, nil
}
