// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

const usageWalkLimit = 4000

type usageTarget struct {
	Source   string
	Path     string
	Snapshot bool
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
	source := o.UsageSource
	if source == "" {
		if o.Transcript != "" {
			source = "claude"
		} else if o.Harness == "claude" || o.Harness == "codex" || o.Harness == "cursor" || o.Harness == "grok" {
			source = o.Harness
		}
	}
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
	if forbiddenUsagePath(path) {
		return usageTarget{}, usagef("usage file is not a session log")
	}
	return usageTarget{Source: source, Path: path, Snapshot: source == "grok"}, nil
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
		if regularUsageFile(path) {
			return path
		}
		return ""
	case "codex":
		if id == "" || o.CodexHome == "" {
			return ""
		}
		return findCodexRollout(o.CodexHome, id)
	case "grok":
		if id == "" || o.GrokHome == "" {
			return ""
		}
		return findGrokUsage(o.GrokHome, o.Worktree, id)
	default:
		return ""
	}
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

func forbiddenUsagePath(path string) bool {
	cleaned := filepath.Clean(path)
	if unsafeHeartbeatPath(cleaned) {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if forbiddenUsageName(part) {
			return true
		}
	}
	return false
}

func forbiddenUsageName(name string) bool {
	base := strings.ToLower(name)
	switch base {
	case "auth.json", "credentials", "credentials.json", "secrets", ".ssh":
		return true
	}
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".age") || strings.Contains(base, "credential") || strings.HasPrefix(base, "id_") {
		return true
	}
	return false
}

func regularUsageFile(path string) bool {
	if path == "" || forbiddenUsagePath(path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
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
	var found string
	n := 0
	stop := errors.New("usage walk limit")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		n++
		if n > usageWalkLimit {
			return stop
		}
		if err != nil || d == nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !d.IsDir() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, suffix) && path > found {
			found = path
		}
		return nil
	})
	if !regularUsageFile(found) || !pathInsideRoot(root, found) {
		return ""
	}
	return found
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
		if regularUsageFile(primary) && pathInsideRoot(root, primary) {
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
		if regularUsageFile(candidate) && pathInsideRoot(root, candidate) && candidate > found {
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

func noteHarnessLine(source string, line []byte, fallback string, sums map[string]usageSum, poisoned, seen map[string]bool, ring *[]string) error {
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
		sums[parsed.Model] = usageSum{input: parsed.Input, output: parsed.Output, cached: parsed.Cached, absolute: true}
		return nil
	}
	nextIn, ok1 := addTokens(cur.input, parsed.Input)
	nextOut, ok2 := addTokens(cur.output, parsed.Output)
	nextCached, ok3 := addTokens(cur.cached, parsed.Cached)
	if !ok1 || !ok2 || !ok3 {
		poisoned[parsed.Model] = true
		delete(sums, parsed.Model)
		return nil
	}
	sums[parsed.Model] = usageSum{input: nextIn, output: nextOut, cached: nextCached}
	return nil
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
		sums[line.Model] = usageSum{input: line.Input, output: line.Output, cached: line.Cached, absolute: true}
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
		if prev != nil && sum.input == prev.Input && sum.output == prev.Output && sum.cached == prev.Cached {
			continue
		}
		if sum.input == 0 && sum.output == 0 && sum.cached == 0 {
			continue
		}
		seq := int64(1)
		if prev != nil {
			seq = prev.Sequence + 1
		}
		created = append(created, heartbeatPendingUsage{
			Model: model, Sequence: seq, Input: sum.input, Output: sum.output, Cached: sum.cached,
			ReportID:    usageReportID(session.id, model, seq, sum.input, sum.output, sum.cached),
			BillingMode: mode, SubscriptionLabel: label,
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
	if forbiddenUsagePath(path) {
		return nil, usagef("usage file is not a session log")
	}
	f, err := openNoFollow(path)
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
		if line.Input == 0 && line.Output == 0 && line.Cached == 0 {
			continue
		}
		prev := usageByModel(session.disk.Usage, line.Model)
		if prev == nil || line.Input > prev.Input || line.Output > prev.Output || line.Cached > prev.Cached {
			return false
		}
	}
	return true
}

func usageCaughtUp(o heartbeatOptions, session *heartbeatSession) bool {
	target, err := resolveHeartbeatUsage(o)
	if err != nil || target.Path == "" {
		return err == nil
	}
	if target.Snapshot {
		return snapshotCaughtUp(target.Path, heartbeatText(o.Model, 128), session)
	}
	return heartbeatTranscriptCaughtUp(target.Path, session.disk.UsageOffset)
}

func usageOutstanding(o heartbeatOptions, session *heartbeatSession) bool {
	target, err := resolveHeartbeatUsage(o)
	if err != nil || session == nil || target.Path == "" {
		return false
	}
	if target.Snapshot {
		return !snapshotCaughtUp(target.Path, heartbeatText(o.Model, 128), session)
	}
	return heartbeatTranscriptOutstanding(target.Path, session.disk.UsageOffset, session.disk.UsageDiscard)
}
