// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

const usageWalkLimit = 4000

type usageTarget struct {
	Source   string
	Path     string
	Snapshot bool
}

// Capacity reporting already carries the selected account; reuse that identity
// so launchers need no second account flag solely for billing.
func usageAccount(o heartbeatOptions) string {
	if o.AccountID != "" {
		return o.AccountID
	}
	return o.Capacity.Account
}

func usageBilling(o heartbeatOptions) (mode, label string) {
	switch o.BillingMode {
	case "api":
		return "api", ""
	case "subscription":
		return "subscription", heartbeatText(o.SubscriptionLabel, 120)
	default:
		return "", ""
	}
}

func pendingBilling(pending heartbeatPendingUsage) string {
	if pending.BillingMode == "" {
		return "unknown"
	}
	return pending.BillingMode
}

func resolveHeartbeatUsage(o heartbeatOptions) (usageTarget, error) {
	switch o.BillingMode {
	case "", "unknown", "api", "subscription":
	default:
		return usageTarget{}, usagef("invalid --billing-mode")
	}
	if o.SubscriptionLabel != "" && o.BillingMode != "subscription" {
		return usageTarget{}, usagef("--subscription-label requires subscription billing")
	}
	source := usageSourceOf(o)
	if source == "" {
		return usageTarget{}, nil
	}
	switch source {
	case "claude", "codex", "cursor", "grok":
	default:
		return usageTarget{}, usagef("invalid --usage-source")
	}
	path := o.UsageFile
	if path == "" && source == "claude" {
		path = o.Transcript
	}
	if path == "" {
		path = locateUsageFile(o, source)
	}
	if path == "" {
		return usageTarget{Source: source, Snapshot: source == "grok"}, nil
	}
	if !allowedUsagePath(source, path) {
		return usageTarget{}, usagef("usage file is not a session log")
	}
	return usageTarget{Source: source, Path: path, Snapshot: source == "grok"}, nil
}

func usageSourceOf(o heartbeatOptions) string {
	if o.UsageSource != "" {
		return o.UsageSource
	}
	if o.Transcript != "" {
		return "claude"
	}
	switch o.Harness {
	case "claude", "codex", "cursor", "grok":
		return o.Harness
	default:
		return ""
	}
}

func locateUsageFile(o heartbeatOptions, source string) string {
	id := o.UsageID
	if id == "" && validUUID(o.SourceSession) {
		id = o.SourceSession
	}
	switch source {
	case "claude":
		if id == "" || o.ClaudeProjects == "" {
			return ""
		}
		return findClaudeTranscript(o.ClaudeProjects, id)
	case "cursor":
		if o.StateDir == "" {
			return ""
		}
		path := filepath.Join(o.StateDir, "cursor.jsonl")
		if regularUsageFile("cursor", path) {
			return path
		}
		return ""
	case "codex":
		if o.CodexHome == "" {
			return ""
		}
		if id == "" {
			return discoverCodexRollout(o.CodexHome, o.Worktree, o.UsageStartedAt)
		}
		return findCodexRollout(o.CodexHome, id)
	case "grok":
		if o.GrokHome == "" {
			return ""
		}
		if id == "" {
			return discoverGrokUsage(o.GrokHome, o.Worktree, o.UsageStartedAt)
		}
		return findGrokUsage(o.GrokHome, o.Worktree, id)
	default:
		return ""
	}
}

// Discovery belongs to the registered generation, including after a helper
// restart. Pin its first match so another session cannot inherit its cursor.
func resolveSessionHeartbeatUsage(o heartbeatOptions, session *heartbeatSession) (usageTarget, error) {
	if session == nil || o.UsageFile != "" || o.Transcript != "" || o.UsageID != "" || validUUID(o.SourceSession) {
		return resolveHeartbeatUsage(o)
	}
	o.Worktree = session.disk.BoundWorktree
	o.UsageStartedAt = session.disk.RegisteredAt
	if o.UsageStartedAt.IsZero() && session.disk.StartedUnix > 0 {
		o.UsageStartedAt = time.Unix(session.disk.StartedUnix, 0)
	}
	source := usageSourceOf(o)
	if source != "codex" && source != "grok" {
		return resolveHeartbeatUsage(o)
	}
	if session.disk.UsagePath != "" {
		if session.disk.UsageSource != source {
			return usageTarget{}, usagef("usage source differs from the registered log")
		}
		o.UsageFile = session.disk.UsagePath
		return resolveHeartbeatUsage(o)
	}
	target, err := resolveHeartbeatUsage(o)
	if err == nil && target.Path != "" {
		session.disk.UsageSource, session.disk.UsagePath = source, target.Path
		if err = saveHeartbeatSession(session); err != nil {
			return usageTarget{}, err
		}
	}
	return target, err
}

func discoverCodexRollout(home, worktree string, started time.Time) string {
	if worktree == "" || started.IsZero() {
		return ""
	}
	root := filepath.Join(home, "sessions")
	var found string
	var newest time.Time
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		n++
		if n > usageWalkLimit {
			return errors.New("usage walk limit")
		}
		if err != nil || d == nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || credentialUsageName(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		f, err := openHarnessFile(harnessCodexRollout, path)
		if err != nil {
			return nil
		}
		defer f.Close()
		reader := bufio.NewReader(io.LimitReader(f, heartbeatUsageLineMax+1))
		line, err := reader.ReadBytes('\n')
		if err != nil || len(line) > heartbeatUsageLineMax {
			return nil
		}
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				CWD       string    `json:"cwd"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &meta) != nil || meta.Type != "session_meta" || meta.Payload.CWD != worktree || !meta.Payload.Timestamp.After(started) {
			return nil
		}
		if meta.Payload.Timestamp.After(newest) || meta.Payload.Timestamp.Equal(newest) && path > found {
			found, newest = path, meta.Payload.Timestamp
		}
		return nil
	})
	if err != nil {
		// A bounded walk may not have reached the latest matching rollout.
		// Leave discovery unpinned so a partial result cannot own its cursor.
		return ""
	}
	return found
}

func discoverGrokUsage(home, worktree string, started time.Time) string {
	if worktree == "" || started.IsZero() {
		return ""
	}
	// Grok encodes the exact cwd in the parent name. updatedAt in usage.json
	// proves activity, not session creation: use summary.json's created_at.
	root := filepath.Join(home, "sessions", encodeURIComponent(worktree))
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > usageWalkLimit {
		// A partial listing cannot prove which session is newest. Leave the
		// generation unpinned, as with an incomplete Codex discovery walk.
		return ""
	}
	var found string
	var newest time.Time
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !usageID(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name(), "usage.json")
		if !regularUsageFile("grok", path) {
			continue
		}
		created := grokSessionCreated(filepath.Join(root, entry.Name(), "summary.json"))
		if !created.After(started) {
			continue
		}
		if created.After(newest) || created.Equal(newest) && path > found {
			found, newest = path, created
		}
	}
	return found
}

func grokSessionCreated(path string) time.Time {
	f, err := openHarnessFile(harnessGrokSummary, path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, heartbeatTitleScanMax+1))
	var meta struct {
		Created time.Time `json:"created_at"`
	}
	if err != nil || len(raw) > int(heartbeatTitleScanMax) || json.Unmarshal(raw, &meta) != nil {
		return time.Time{}
	}
	return meta.Created
}

func usageID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// allowedUsagePath reports whether path is this source's vendor usage log.
// It is the resolveHarnessPath allowlist, judged on the absolute path.
func allowedUsagePath(source, path string) bool {
	kind, ok := harnessKindForSource(source)
	if !ok {
		return false
	}
	_, ok = resolveHarnessPath(kind, path)
	return ok
}

func usagePathParts(path string) []string {
	raw := strings.Split(filepath.ToSlash(path), "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

// credentialUsageName reports a credential-store name. It judges the name as
// APFS compares names (fsFold), so every spelling APFS treats as the same
// file gets the same verdict, and nothing APFS keeps distinct is folded in.
func credentialUsageName(name string) bool {
	base := fsFold(name)
	switch base {
	case "auth.json", "cli-config.json", "cookies", "cookies.db", "cookies.binarycookies",
		"keychain", "keychains", "login.keychain", "login.keychain-db",
		"credentials", "credentials.json", "secrets", ".ssh", ".gnupg", ".aws", ".netrc", ".docker", ".password-store":
		return true
	}
	if strings.HasPrefix(base, ".credentials") || strings.HasPrefix(base, ".env") || strings.HasPrefix(base, "id_") {
		return true
	}
	if strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".age") {
		return true
	}
	return strings.Contains(base, "credential") || strings.Contains(base, "keychain") || strings.Contains(base, "cookie")
}

func regularUsageFile(source, path string) bool {
	kind, ok := harnessKindForSource(source)
	if !ok {
		return false
	}
	_, err := statHarnessFile(kind, path)
	return err == nil
}

func findCodexRollout(home, thread string) string {
	if !usageID(thread) {
		return ""
	}
	root := filepath.Join(home, "sessions")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	suffix := "-" + thread + ".jsonl"
	// Codex's YYYY/MM/DD directories sort by date. Spend the bounded
	// search on the newest dates first so old history cannot hide a recent
	// explicit id. Stop at the first fenced suffix match.
	n := 1 // Count the root, as in the metadata discovery walk.
	sessionsRoot := root
	var walk func(string) string
	walk = func(root string) string {
		// Each recursion lists a directory root only. File reads still use
		// the harness fence, and matches must remain inside sessionsRoot.
		entries, err := os.ReadDir(root)
		if err != nil {
			return ""
		}
		for i := len(entries) - 1; i >= 0; i-- {
			n++
			if n > usageWalkLimit {
				return ""
			}
			entry := entries[i]
			if entry.Type()&os.ModeSymlink != 0 || credentialUsageName(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if entry.IsDir() {
				if found := walk(path); found != "" {
					return found
				}
				if n > usageWalkLimit {
					return ""
				}
			} else if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), suffix) && regularUsageFile("codex", path) && pathInsideRoot(sessionsRoot, path) {
				return path
			}
		}
		return ""
	}
	return walk(root)
}

func findGrokUsage(home, worktree, id string) string {
	if !usageID(id) {
		return ""
	}
	root := filepath.Join(home, "sessions")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if strings.TrimSpace(worktree) != "" {
		primary := filepath.Join(root, encodeURIComponent(worktree), id, "usage.json")
		if regularUsageFile("grok", primary) && pathInsideRoot(root, primary) {
			return primary
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var found string
	for i, entry := range entries {
		if i >= usageWalkLimit {
			break
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), id, "usage.json")
		if regularUsageFile("grok", candidate) && pathInsideRoot(root, candidate) && candidate > found {
			found = candidate
		}
	}
	return found
}

func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if urlUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func urlUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.ContainsRune("-_.!~*'()", rune(c))
}

func pathInsideRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func noteHarnessLine(source string, line []byte, fallback string, sums map[string]usageSum, poisoned, seen map[string]bool, ring *[]string, codex *heartbeatCodexCursor) error {
	if source == "codex" {
		if codex == nil {
			codex = &heartbeatCodexCursor{}
		}
		if model, ok := sessionusage.CodexContextModel(line); ok {
			codex.Model = model
			return nil
		}
		noteCodexTotals(line, fallback, sums, poisoned, codex)
		return nil
	}
	parsed, ok, err := sessionusage.ParseHeartbeatLine(source, fallback, line)
	if err != nil {
		return errUsageOverflow
	}
	if !ok || parsed.Model == "" || poisoned[parsed.Model] {
		return nil
	}
	if parsed.ID != "" {
		if seen[parsed.ID] || len(seen) >= heartbeatUsageSeenMax {
			return nil
		}
		seen[parsed.ID] = true
		*ring = append(*ring, parsed.ID)
	}
	cur := sums[parsed.Model]
	if parsed.Absolute {
		if cur.absolute && (parsed.Input < cur.input || parsed.Output < cur.output || parsed.Cached < cur.cached) {
			return nil
		}
		sums[parsed.Model] = usageSum{input: parsed.Input, output: parsed.Output, cached: parsed.Cached, reasoning: parsed.Reasoning, reasoningKnown: parsed.ReasoningKnown, absolute: true}
		return nil
	}
	addUsageDelta(sums, poisoned, parsed)
	return nil
}

// noteCodexTotals turns one session-wide cumulative Codex record into a delta
// for the model in context. The baseline always moves to the new totals, so
// tokens that cannot be attributed to a model are dropped, never re-counted
// under the next model. A record below the baseline is stale and ignored.
func noteCodexTotals(line []byte, fallback string, sums map[string]usageSum, poisoned map[string]bool, codex *heartbeatCodexCursor) {
	snap, ok := sessionusage.CodexTotals(line)
	if !ok {
		return
	}
	if snap.Input < codex.Input || snap.Output < codex.Output || snap.Cached < codex.Cached {
		return
	}
	delta := sessionusage.HeartbeatLine{
		Input: snap.Input - codex.Input, Output: snap.Output - codex.Output, Cached: snap.Cached - codex.Cached,
	}
	switch {
	case !snap.ReasoningKnown:
		// Reasoning since the last known total is unknown until a record
		// carries it again.
		codex.ReasoningKnown = false
	case !codex.ReasoningKnown:
		// A total after an unknown stretch only re-establishes the baseline;
		// the increase cannot be attributed to the model in context.
		codex.Reasoning, codex.ReasoningKnown = snap.Reasoning, true
	case snap.Reasoning >= codex.Reasoning:
		delta.Reasoning, delta.ReasoningKnown = snap.Reasoning-codex.Reasoning, true
		codex.Reasoning = snap.Reasoning
	default:
		// Codex can revise reasoning down while input grows; the attributed
		// baseline never moves backwards.
		delta.ReasoningKnown = true
	}
	codex.Input, codex.Output, codex.Cached = snap.Input, snap.Output, snap.Cached
	if delta.Cached > delta.Input {
		delta.Cached = delta.Input
	}
	// Reasoning is a subset of output. An increase that cannot be one is not
	// attributed rather than reported as an impossible figure.
	if delta.ReasoningKnown && delta.Reasoning > delta.Output {
		delta.Reasoning, delta.ReasoningKnown = 0, false
	}
	model := snap.Model
	if model == "" {
		model = codex.Model
	}
	if model == "" {
		model = fallback
	}
	if model == "" || !heartbeatModelRE.MatchString(model) || poisoned[model] {
		return
	}
	if delta.Input == 0 && delta.Output == 0 && delta.Cached == 0 && (!delta.ReasoningKnown || delta.Reasoning == 0) {
		return
	}
	delta.Model = model
	addUsageDelta(sums, poisoned, delta)
}

func addUsageDelta(sums map[string]usageSum, poisoned map[string]bool, parsed sessionusage.HeartbeatLine) {
	cur := sums[parsed.Model]
	nextIn, ok1 := addTokens(cur.input, parsed.Input)
	nextOut, ok2 := addTokens(cur.output, parsed.Output)
	nextCached, ok3 := addTokens(cur.cached, parsed.Cached)
	nextReasoning, reasoningKnown, ok4 := addUsageReasoning(cur, parsed)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		poisoned[parsed.Model] = true
		delete(sums, parsed.Model)
		return
	}
	sums[parsed.Model] = usageSum{input: nextIn, output: nextOut, cached: nextCached, reasoning: nextReasoning, reasoningKnown: reasoningKnown}
}

func addUsageReasoning(cur usageSum, parsed sessionusage.HeartbeatLine) (int64, bool, bool) {
	// Sum only observed reasoning deltas. An unavailable delta must neither
	// erase earlier observations nor hide later ones within the same scan.
	if !parsed.ReasoningKnown {
		return cur.reasoning, cur.reasoningKnown, true
	}
	next, ok := addTokens(cur.reasoning, parsed.Reasoning)
	return next, true, ok
}

func (rt *runtime) reportSnapshotUsage(ctx context.Context, projectID string, o heartbeatOptions, session *heartbeatSession, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lines, err := readGrokUsage(path, heartbeatText(o.Model, 128))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	sums := map[string]usageSum{}
	for _, line := range lines {
		cur := sums[line.Model]
		if cur.absolute && (line.Input < cur.input || line.Output < cur.output || line.Cached < cur.cached) {
			continue
		}
		sums[line.Model] = usageSum{input: line.Input, output: line.Output, cached: line.Cached, reasoning: line.Reasoning, reasoningKnown: line.ReasoningKnown, absolute: true}
	}
	models := make([]string, 0, len(sums))
	for model := range sums {
		models = append(models, model)
	}
	slices.Sort(models)
	mode, label := usageBilling(o)
	created := make([]heartbeatPendingUsage, 0, len(models))
	for _, model := range models {
		sum := sums[model]
		prev := usageByModel(session.disk.Usage, model)
		if prev != nil && (sum.input < prev.Input || sum.output < prev.Output || sum.cached < prev.Cached) {
			continue
		}
		reasoning, reasoningKnown := holdReasoning(prev, sum.reasoning, sum.reasoningKnown)
		var valid bool
		if reasoning, reasoningKnown, valid = fitUsageReport(prev, sum.input, sum.output, sum.cached, reasoning, reasoningKnown); !valid {
			fmt.Fprintf(rt.stderr, "heartbeat: usage for %s is not a valid cumulative report; skipped\n", model)
			continue
		}
		if prev != nil && sum.input == prev.Input && sum.output == prev.Output && sum.cached == prev.Cached && sameReasoning(prev.Reasoning, reasoning, reasoningKnown) {
			continue
		}
		if sum.input == 0 && sum.output == 0 && sum.cached == 0 && (!reasoningKnown || reasoning == 0) {
			continue
		}
		seq := int64(1)
		if prev != nil {
			seq = prev.Sequence + 1
		}
		created = append(created, heartbeatPendingUsage{
			Model: model, Sequence: seq, Input: sum.input, Output: sum.output, Cached: sum.cached,
			Reasoning:   reasoningPointer(reasoning, reasoningKnown),
			ReportID:    usageReportID(session.id, model, seq, sum.input, sum.output, sum.cached, reasoningPointer(reasoning, reasoningKnown)),
			BillingMode: mode, SubscriptionLabel: label, AccountID: usageAccount(o),
		})
	}
	if len(created) == 0 {
		return nil
	}
	session.disk.PendingUsage = append(session.disk.PendingUsage, created...)
	if err := saveHeartbeatSession(session); err != nil {
		session.disk.PendingUsage = session.disk.PendingUsage[:len(session.disk.PendingUsage)-len(created)]
		return err
	}
	return rt.replayPendingUsage(ctx, projectID, session)
}

func readGrokUsage(path, fallback string) ([]sessionusage.HeartbeatLine, error) {
	if !allowedUsagePath("grok", path) {
		return nil, usagef("usage file is not a session log")
	}
	f, err := openHarnessFile(harnessGrokUsage, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, usagef("usage snapshot is too large")
	}
	return sessionusage.ParseGrokUsage(raw, fallback)
}

func snapshotCaughtUp(path, fallback string, session *heartbeatSession) bool {
	lines, err := readGrokUsage(path, fallback)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	for _, line := range lines {
		if line.Input == 0 && line.Output == 0 && line.Cached == 0 && (!line.ReasoningKnown || line.Reasoning == 0) {
			continue
		}
		prev := usageByModel(session.disk.Usage, line.Model)
		if prev == nil || line.Input > prev.Input || line.Output > prev.Output || line.Cached > prev.Cached || (line.ReasoningKnown && (prev.Reasoning == nil || line.Reasoning > *prev.Reasoning)) {
			return false
		}
	}
	return true
}

func usageCaughtUp(o heartbeatOptions, session *heartbeatSession) bool {
	target, err := resolveSessionHeartbeatUsage(o, session)
	if err != nil || target.Path == "" {
		return err == nil
	}
	if target.Snapshot {
		return snapshotCaughtUp(target.Path, heartbeatText(o.Model, 128), session)
	}
	return heartbeatTranscriptCaughtUp(target.Source, target.Path, session.disk.UsageOffset)
}

func usageOutstanding(o heartbeatOptions, session *heartbeatSession) bool {
	target, err := resolveSessionHeartbeatUsage(o, session)
	if err != nil || session == nil || target.Path == "" {
		return false
	}
	if target.Snapshot {
		return !snapshotCaughtUp(target.Path, heartbeatText(o.Model, 128), session)
	}
	return heartbeatTranscriptOutstanding(target.Source, target.Path, session.disk.UsageOffset, session.disk.UsageDiscard)
}
