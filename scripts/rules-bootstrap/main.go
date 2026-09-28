// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux || darwin

// rules-bootstrap prepares a local instruction read. It cannot prove that a
// harness read the output, enforce obedience, or reset already loaded rules.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
	"golang.org/x/sys/unix"
)

const maxBinding = 16 * 1024
const maxMetadata = 16 * 1024
const publicDir = "scripts/rules-bootstrap"

var rejected = errors.New("bootstrap input or evidence rejected; material work blocked")
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,95}$`)
var instanceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var cliPathRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// The coordinator delivers this privately for one actual generation, after a
// separate approval of the FULL floor digest. Its independently supplied hash
// authenticates all selectors; no field is inferred from home/current slots.
// ConfigPath and LeaseFile are references only: this helper never opens them.
// CLIPath names the coordinator-selected, independently validated published CLI.
type binding struct {
	Schema      string        `json:"schema"`
	Workspace   string        `json:"workspace"`
	Instance    string        `json:"instance"`
	InstanceURL string        `json:"instance_url"`
	ConfigPath  string        `json:"config_path"`
	CLIPath     string        `json:"cli_path"`
	Session     string        `json:"session_id"`
	Agent       string        `json:"agent_name"`
	Context     rules.Context `json:"context"`
	Request     string        `json:"request_id"`
	Revision    int64         `json:"expected_revision"`
	Floor       string        `json:"floor_path"`
	FloorSHA    string        `json:"floor_sha256"`
	Cache       string        `json:"cache_path"`
	Output      string        `json:"output_path"`
	State       string        `json:"state_path"`
	LeaseFile   string        `json:"worker_lease_file"`
}

type options struct {
	path, sha, session, harness string
	retry                       bool
}

func digest(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

// Enforce exact field spelling, presence and types, not encoding/json's
// case-insensitive fallback. Tokens also reject duplicate keys at every depth.
func strictJSON(raw []byte, dst any) error {
	if !utf8.Valid(raw) {
		return rejected
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := jsonShape(d, reflect.TypeOf(dst).Elem(), 0); err != nil {
		return rejected
	}
	if _, err := d.Token(); err != io.EOF {
		return rejected
	}
	if json.Unmarshal(raw, dst) != nil {
		return rejected
	}
	return nil
}

func jsonShape(d *json.Decoder, typ reflect.Type, depth int) error {
	if depth > 32 {
		return rejected
	}
	tok, err := d.Token()
	if err != nil {
		return rejected
	}
	if typ.Kind() == reflect.Pointer {
		if tok == nil {
			return nil
		}
		typ = typ.Elem()
	}
	if tok == nil { // Only explicitly nullable pointer fields accept null.
		return rejected
	}
	if typ == reflect.TypeOf(time.Time{}) {
		_, ok := tok.(string)
		if !ok {
			return rejected
		}
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		if tok != json.Delim('{') {
			return rejected
		}
		fields := map[string]reflect.StructField{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "-" {
				fields[name] = f
			}
		}
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			name, ok := key.(string)
			f, known := fields[name]
			if e != nil || !ok || !known || seen[name] {
				return rejected
			}
			seen[name] = true
			if jsonShape(d, f.Type, depth+1) != nil {
				return rejected
			}
		}
		for name, f := range fields {
			if !seen[name] && !strings.Contains(f.Tag.Get("json"), ",omitempty") {
				return rejected
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return rejected
		}
	case reflect.Slice:
		if tok != json.Delim('[') {
			return rejected
		}
		for d.More() {
			if jsonShape(d, typ.Elem(), depth+1) != nil {
				return rejected
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return rejected
		}
	default:
		if _, compound := tok.(json.Delim); compound {
			return rejected
		}
	}
	return nil
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\\\x00\r\n") && len(path) <= 4096
}

// Metadata only. Physical parents are required even for references we never
// open. The rules reader repeats its own descriptor-relative, no-follow checks.
func physical(path string) error {
	if !absolute(path) {
		return rejected
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return rejected
		}
		if p != path && !st.IsDir() {
			return rejected
		}
		if st.IsDir() {
			var dir unix.Stat_t
			if unix.Lstat(p, &dir) != nil || (dir.Uid != 0 && dir.Uid != uint32(os.Geteuid())) ||
				(dir.Mode&0022 != 0 && !(dir.Uid == 0 && dir.Mode&unix.S_ISVTX != 0)) {
				return rejected
			}
		}
		if p == "/" {
			break
		}
	}
	return nil
}

func privateFile(path string, max int, optional bool) error {
	if physical(filepath.Dir(path)) != nil {
		return rejected
	}
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	if optional && err == unix.ENOENT {
		return nil
	}
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size < 0 || st.Size > int64(max) {
		return rejected
	}
	return nil
}

func privateDir(path string) error {
	var st unix.Stat_t
	if physical(path) != nil || unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		return rejected
	}
	return nil
}

// Validate metadata only. The coordinator's published-binary validation is
// independent of this helper; a path check does not authenticate CLI bytes.
func executable(path string) error {
	if !absolute(path) || !cliPathRE.MatchString(path) || physical(filepath.Dir(path)) != nil {
		return rejected
	}
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG ||
		(st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Mode&0022 != 0 ||
		st.Mode&06000 != 0 || unix.Access(path, unix.X_OK) != nil {
		return rejected
	}
	return nil
}

func workspace() (string, error) {
	wd, err := os.Getwd()
	if err != nil || physical(wd) != nil {
		return "", rejected
	}
	st, err := os.Lstat(filepath.Join(wd, ".git"))
	if err != nil || (!st.IsDir() && !st.Mode().IsRegular()) {
		return "", rejected
	}
	return wd, nil
}

func loadBinding(root string, o options) (binding, []byte, error) {
	var b binding
	if !uuidRE.MatchString(o.session) || (o.harness != "codex" && o.harness != "cursor" && o.harness != "claude-code") || !shaRE.MatchString(o.sha) {
		return b, nil, rejected
	}
	dir := filepath.Join(root, "tmp", "aeon-rules", o.session)
	for _, p := range []string{filepath.Join(root, "tmp"), filepath.Dir(dir), dir} {
		if privateDir(p) != nil {
			return b, nil, rejected
		}
	}
	if o.path != filepath.Join(dir, "binding.json") || privateFile(o.path, maxBinding, false) != nil {
		return b, nil, rejected
	}
	raw, err := rules.ReadFile(o.path, maxBinding)
	if err != nil || digest(raw) != o.sha || strictJSON(raw, &b) != nil {
		return b, nil, rejected
	}
	if b.Schema != "aeon.rules.bootstrap.v1" || b.Workspace != root || b.Session != o.session || b.Context.Harness != o.harness || rules.ValidateContext(b.Context) != nil || b.Context.AgentID == "" || !nameRE.MatchString(b.Agent) || !instanceRE.MatchString(b.Instance) || !uuidRE.MatchString(b.Request) || b.Revision < 0 || b.Revision == math.MaxInt64 || !shaRE.MatchString(b.FloorSHA) {
		return b, nil, rejected
	}
	u, err := url.Parse(b.InstanceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return b, nil, rejected
	}
	// The explicit config is opened only by paimos, with no inherited instance
	// environment. It must be a distinct private reference, never a rules input.
	if b.ConfigPath == o.path || !absolute(b.ConfigPath) || privateFile(b.ConfigPath, 1<<20, false) != nil {
		return b, nil, rejected
	}
	if executable(b.CLIPath) != nil {
		return b, nil, rejected
	}
	seen := map[string]bool{o.path: true, b.ConfigPath: true}
	for _, a := range []struct {
		path, ext string
		max       int
		optional  bool
	}{
		{b.Floor, ".txt", rules.MaxBytes, false}, {b.Cache, ".json", rules.MaxCacheBytes, true},
		{b.Output, ".txt", rules.MaxBytes, true}, {b.State, ".json", rules.MaxCacheBytes, true},
		{b.LeaseFile, ".txt", 64 * 1024, false},
	} {
		if !absolute(a.path) || filepath.Dir(a.path) != dir || filepath.Ext(a.path) != a.ext || seen[a.path] || privateFile(a.path, a.max, a.optional) != nil {
			return b, nil, rejected
		}
		seen[a.path] = true
	}
	if b.Output != filepath.Join(dir, "received.txt") {
		return b, nil, rejected
	}
	raw, err = rules.ReadFile(b.Floor, rules.MaxBytes)
	if err != nil {
		return b, nil, rejected
	}
	if _, err = rules.VerifyFloor(raw, b.FloorSHA); err != nil {
		return b, nil, rejected
	}
	return b, raw, nil
}

// This is the existing CLI's immutable checkpoint envelope, not a new receive
// protocol. It supplies context/instance binding absent from stdout metadata.
type checkpoint struct {
	Schema   string                    `json:"schema"`
	Instance string                    `json:"instance"`
	Session  string                    `json:"session_id"`
	Agent    string                    `json:"agent_name"`
	Output   string                    `json:"output_path"`
	FloorSHA string                    `json:"floor_sha256"`
	Bundle   rules.Merged              `json:"bundle"`
	Request  harness.RulesReceiptWrite `json:"request"`
	Gap      string                    `json:"gap"`
	SHA256   string                    `json:"sha256"`
}

type metadata struct {
	Session         string `json:"session_id"`
	Request         string `json:"request_id"`
	Revision        int64  `json:"expected_revision"`
	Source          string `json:"source"`
	Stale           bool   `json:"stale"`
	Gap             string `json:"gap"`
	Output          string `json:"output_path"`
	State           string `json:"state_path"`
	SHA256          string `json:"body_sha256"`
	Version         string `json:"version"`
	ByteSize        int    `json:"byte_size"`
	BytesReceived   bool   `json:"bytes_received"`
	ReceiptRecorded bool   `json:"receipt_recorded"`
	ReceiptStatus   string `json:"receipt_status"`
	Provenance      bool   `json:"provenance_recorded"`
	Publication     bool   `json:"publication_verified"`
	Load            bool   `json:"load_verified"`
	Execution       bool   `json:"execution_verified"`
	Authority       bool   `json:"authority_granted"`
	Complete        bool   `json:"complete"`
	ReceiptID       string `json:"receipt_id,omitempty"`
	ReceiptRevision int64  `json:"receipt_revision,omitempty"`
	Replayed        *bool  `json:"replayed,omitempty"`
}

func verifyCheckpoint(b binding, floor []byte) (checkpoint, error) {
	var s checkpoint
	raw, err := rules.ReadFile(b.State, rules.MaxCacheBytes)
	if err != nil || strictJSON(raw, &s) != nil {
		return s, rejected
	}
	pin := s.SHA256
	s.SHA256 = ""
	canonical, _ := json.Marshal(s)
	s.SHA256 = pin
	r := s.Request
	if digest(canonical) != pin || s.Schema != "aeon.rules.receive.v1" || s.Instance != b.InstanceURL || s.Session != b.Session || s.Agent != b.Agent || s.Output != b.Output || s.FloorSHA != b.FloorSHA || r.Context != b.Context || r.RequestID != b.Request || r.ExpectedRevision == nil || *r.ExpectedRevision != b.Revision || s.Bundle.Context != b.Context || s.Bundle.Floor != string(floor) || r.BodySHA256 != s.Bundle.SHA256 || r.Version != s.Bundle.Version || r.ByteSize == nil || *r.ByteSize != s.Bundle.ByteSize {
		return s, rejected
	}
	if r.Source == "floor-only" {
		m, _ := rules.Offline(nil, b.InstanceURL, b.Context, string(floor), time.Now())
		if !reflect.DeepEqual(m, s.Bundle) {
			return s, rejected
		}
	} else if (r.Source != "online" && r.Source != "cache") || rules.ValidateMerged(s.Bundle, b.Context, time.Now()) != nil {
		return s, rejected
	}
	return s, nil
}

func verifyResult(b binding, floor, raw []byte, success bool) (metadata, []byte, error) {
	var m metadata
	if len(raw) > maxMetadata || strictJSON(raw, &m) != nil {
		return m, nil, rejected
	}
	s, err := verifyCheckpoint(b, floor)
	if err != nil {
		return m, nil, rejected
	}
	if m.Session != b.Session || m.Request != b.Request || m.Revision != b.Revision || m.Output != b.Output || m.State != b.State || m.SHA256 != s.Bundle.SHA256 || m.Version != s.Bundle.Version || m.ByteSize != s.Bundle.ByteSize || m.Source != s.Request.Source || m.Stale != (m.Source != "online") || m.Gap != s.Gap || !m.BytesReceived || m.Provenance || m.Publication || m.Load || m.Execution || m.Authority || m.Complete != m.ReceiptRecorded {
		return m, nil, rejected
	}
	if m.ReceiptRecorded {
		if !success || m.Source != "online" || m.ReceiptStatus != "recorded" || !uuidRE.MatchString(m.ReceiptID) || m.ReceiptRevision != b.Revision+1 || m.Replayed == nil {
			return m, nil, rejected
		}
	} else {
		if m.ReceiptID != "" || m.ReceiptRevision != 0 || m.Replayed != nil {
			return m, nil, rejected
		}
		if m.Source == "online" {
			if success || (m.ReceiptStatus != "unconfirmed" && m.ReceiptStatus != "rejected") {
				return m, nil, rejected
			}
		} else if !success || m.ReceiptStatus != "not_submitted_offline" {
			return m, nil, rejected
		}
	}
	body, err := rules.ReadFile(b.Output, rules.MaxBytes)
	if err != nil || len(body) != m.ByteSize || digest(body) != m.SHA256 || !bytes.Equal(body, []byte(s.Bundle.Body)) {
		return m, nil, rejected
	}
	return m, body, nil
}

func receiveCommand(ctx context.Context, b binding, retry bool) *exec.Cmd {
	args := []string{"--config", b.ConfigPath, "--instance", b.Instance, "--json", "session", "start", "--rules-receive", "--project", b.Context.ProjectID, "--agent", b.Agent, "--session", b.Session, "--worker-lease-file", b.LeaseFile, "--rules-request-id", b.Request, "--rules-expected-revision", strconv.FormatInt(b.Revision, 10), "--rules-state", b.State, "--rules-out", b.Output, "--rules-cache", b.Cache, "--rules-floor", b.Floor, "--rules-floor-sha256", b.FloorSHA, "--rules-tenant", b.Context.TenantID, "--rules-person", b.Context.PersonID, "--rules-agent", b.Context.AgentID, "--rules-role", b.Context.Role, "--rules-harness", b.Context.Harness}
	if b.Context.TaskID != "" {
		args = append(args, "--rules-task", b.Context.TaskID)
	}
	if retry {
		args = append(args, "--rules-retry")
	}
	cmd := exec.CommandContext(ctx, b.CLIPath, args...)
	cmd.Dir = b.Workspace
	cmd.Env = []string{} // In particular, AEON/PAIMOS URL/key overrides cannot win.
	cmd.WaitDelay = time.Second
	return cmd
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxMetadata - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type runner func(*exec.Cmd) error

func receive(b binding, floor []byte, o options, out, status io.Writer, run runner) error {
	// Refuse an implicit retry before paimos can refresh its cache. On retry the
	// original checkpoint/cache/output must stay byte-for-byte unchanged.
	saved := map[string][]byte{}
	for _, p := range []string{b.State, b.Output, b.Cache} {
		_, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || (!o.retry && p != b.Cache) {
			return rejected
		}
		if o.retry {
			raw, err := rules.ReadFile(p, rules.MaxCacheBytes)
			if err != nil {
				return rejected
			}
			saved[p] = raw
		}
	}
	if o.retry {
		s, err := verifyCheckpoint(b, floor)
		if err != nil || s.Request.Source != "online" {
			return rejected
		}
		if raw, exists := saved[b.Output]; exists && !bytes.Equal(raw, []byte(s.Bundle.Body)) {
			return rejected
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := receiveCommand(ctx, b, o.retry)
	var captured boundedOutput
	cmd.Stdout, cmd.Stderr = &captured, io.Discard
	err := run(cmd) // Never forward err or arbitrary child stderr to the harness.
	if captured.overflow {
		return rejected
	}
	for p, before := range saved {
		after, e := rules.ReadFile(p, rules.MaxCacheBytes)
		if e != nil || !bytes.Equal(before, after) {
			return rejected
		}
	}
	// Revalidate the binding/floor and permissions after the subprocess, too.
	after, afterFloor, e := loadBinding(b.Workspace, o)
	if e != nil || after != b || !bytes.Equal(afterFloor, floor) {
		return rejected
	}
	m, body, e := verifyResult(b, floor, captured.Bytes(), err == nil)
	if e != nil {
		return rejected
	}
	// Deliberately omit private paths and arbitrary gap/error bodies. These are
	// byte-receipt claims only, including when complete=true in the CLI schema.
	statusFields := map[string]any{
		"source": m.Source, "stale": m.Stale, "bytes_received": m.BytesReceived,
		"receipt_recorded": m.ReceiptRecorded, "receipt_status": m.ReceiptStatus,
		"provenance_recorded": m.Provenance, "publication_verified": m.Publication,
		"load_verified": m.Load, "execution_verified": m.Execution, "authority_granted": m.Authority,
		"complete": m.Complete, "degraded": m.Source != "online", "rollout_verified": false,
	}
	if m.ReceiptRecorded {
		statusFields["receipt_id"], statusFields["receipt_revision"], statusFields["replayed"] = m.ReceiptID, m.ReceiptRevision, *m.Replayed
	}
	if json.NewEncoder(status).Encode(statusFields) != nil {
		return rejected
	}
	if err != nil {
		return errors.New("receipt not confirmed; checkpoint retained; only explicit --retry may resubmit")
	}
	if _, e = out.Write(body); e != nil {
		return rejected
	}
	if m.Source != "online" {
		return errors.New("degraded rules received; material work blocked; no rollout success")
	}
	return nil
}

type target struct {
	Candidate string   `json:"candidate"`
	Target    string   `json:"target"`
	Harnesses []string `json:"harnesses"`
	SHA256    string   `json:"sha256"`
	Bytes     int      `json:"byte_size"`
}
type manifest struct {
	Schema       string   `json:"schema"`
	State        string   `json:"state"`
	AgentsSHA    string   `json:"existing_agents_sha256"`
	ClaudeAbsent bool     `json:"expected_claude_absent"`
	Targets      []target `json:"targets"`
}

func publicFile(path string, max int) ([]byte, error) {
	if physical(path) != nil {
		return nil, rejected
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, rejected
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(max) {
		return nil, rejected
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(raw) > max || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
		return nil, rejected
	}
	return raw, nil
}

func check(root string) error {
	raw, err := publicFile(filepath.Join(root, publicDir, "rollout.json"), maxBinding)
	var m manifest
	if err != nil || strictJSON(raw, &m) != nil || m.Schema != "aeon.rules.rollout.v1" || (m.State != "prepared" && m.State != "active") || !m.ClaudeAbsent || !shaRE.MatchString(m.AgentsSHA) || len(m.Targets) != 2 {
		return rejected
	}
	for i, t := range m.Targets {
		candidate, dest, harnesses := "agents.txt", "AGENTS.md", []string{"codex", "cursor"}
		if i == 1 {
			candidate, dest, harnesses = "claude.txt", "CLAUDE.md", []string{"claude-code"}
		}
		if t.Candidate != publicDir+"/"+candidate || t.Target != dest || !reflect.DeepEqual(t.Harnesses, harnesses) || !shaRE.MatchString(t.SHA256) || t.Bytes <= 0 || t.Bytes > rules.MaxBytes {
			return rejected
		}
		body, e := publicFile(filepath.Join(root, t.Candidate), rules.MaxBytes)
		if e != nil || len(body) != t.Bytes || digest(body) != t.SHA256 {
			return rejected
		}
		if m.State == "active" {
			active, e := publicFile(filepath.Join(root, dest), rules.MaxBytes)
			if e != nil || !bytes.Equal(body, active) {
				return rejected
			}
		}
	}
	if m.State == "prepared" {
		active, e := publicFile(filepath.Join(root, "AGENTS.md"), rules.MaxBytes)
		if e != nil || digest(active) != m.AgentsSHA {
			return rejected
		}
		if _, e = os.Lstat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(e) {
			return rejected
		}
	}
	// Check the index, including force-added ignored artifacts, without opening
	// any private binding. Runtime is confined to this one ignored subtree.
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--", "tmp", publicDir)
	cmd.Env = []string{} // GIT_DIR/GIT_INDEX_FILE must not select another checkout.
	var tracked boundedOutput
	cmd.Stdout, cmd.Stderr = &tracked, io.Discard
	if cmd.Run() != nil || tracked.overflow {
		return rejected
	}
	allowed := map[string]bool{}
	for _, name := range []string{"main.go", "main_test.go", "agents.txt", "claude.txt", "rollout.json"} {
		allowed[publicDir+"/"+name] = true
	}
	for _, path := range strings.Split(strings.TrimSuffix(tracked.String(), "\x00"), "\x00") {
		if path != "" && !allowed[path] {
			return rejected
		}
	}
	cmd = exec.Command("git", "-C", root, "check-ignore", "--no-index", "-q", "--", "tmp/aeon-rules/probe")
	cmd.Env = []string{}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Run() != nil {
		return rejected
	}
	return nil
}

func execute(args []string, out, status io.Writer, run runner) error {
	root, err := workspace()
	if err != nil || len(args) == 0 {
		return rejected
	}
	if args[0] == "check" {
		if len(args) != 1 {
			return rejected
		}
		if err := check(root); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "rules bootstrap drift check passed (no activation or harness-read evidence)")
		return err
	}
	if args[0] != "floor" && args[0] != "receive" {
		return rejected
	}
	var o options
	fs := flag.NewFlagSet("rules-bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.path, "binding", "", "private per-generation binding.json")
	fs.StringVar(&o.sha, "binding-sha256", "", "independently delivered binding SHA256")
	fs.StringVar(&o.session, "session", "", "expected public session UUID")
	fs.StringVar(&o.harness, "harness", "", "expected harness")
	if args[0] == "receive" {
		fs.BoolVar(&o.retry, "retry", false, "explicit identical receipt retry")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return rejected
	}
	b, floor, err := loadBinding(root, o)
	if err != nil {
		return err
	}
	if args[0] == "floor" {
		_, err = out.Write(floor)
		return err
	}
	return receive(b, floor, o, out, status, run)
}

func main() {
	if err := execute(os.Args[1:], os.Stdout, os.Stderr, (*exec.Cmd).Run); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
