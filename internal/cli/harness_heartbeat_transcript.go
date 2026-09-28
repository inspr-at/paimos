// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	heartbeatUsageWindow  int64 = 1 << 20
	heartbeatUsageRecent        = 1024
	heartbeatUsageSeenMax       = 4096
	heartbeatTitleScanMax int64 = 1 << 20
)

func (rt *runtime) reportHeartbeatUsage(ctx context.Context, projectID string, o heartbeatOptions, session *heartbeatSession) error {
	if err := rt.replayPendingUsage(ctx, projectID, session); err != nil {
		return err
	}
	if o.Transcript == "" {
		return nil
	}
	sums, next, recent, err := scanUsageWindow(ctx, o.Transcript, heartbeatText(o.Model, 128), session.disk.UsageOffset, heartbeatUsageWindow, session.disk.UsageRecent)
	if err != nil {
		return err
	}
	models := make([]string, 0, len(sums))
	for model := range sums {
		models = append(models, model)
	}
	slices.Sort(models)
	created := make([]heartbeatPendingUsage, 0, len(models))
	for _, model := range models {
		sum := sums[model]
		prev := usageByModel(session.disk.Usage, model)
		input, output, cached := sum.input, sum.output, sum.cached
		if prev != nil {
			var ok1, ok2, ok3 bool
			input, ok1 = addTokens(prev.Input, sum.input)
			output, ok2 = addTokens(prev.Output, sum.output)
			cached, ok3 = addTokens(prev.Cached, sum.cached)
			if !ok1 || !ok2 || !ok3 {
				continue
			}
			if input == prev.Input && output == prev.Output && cached == prev.Cached {
				continue
			}
			if input < prev.Input || output < prev.Output || cached < prev.Cached {
				continue
			}
		}
		if input == 0 && output == 0 && cached == 0 {
			continue
		}
		seq := int64(1)
		if prev != nil {
			seq = prev.Sequence + 1
		}
		created = append(created, heartbeatPendingUsage{
			Model: model, Sequence: seq, Input: input, Output: output, Cached: cached,
			ReportID: usageReportID(session.id, model, seq, input, output, cached),
			Offset:   next, Recent: recent,
		})
	}
	if len(created) == 0 {
		session.disk.UsageOffset = next
		session.disk.UsageRecent = trimRecent(recent)
		return nil
	}
	session.disk.PendingUsage = append(session.disk.PendingUsage, created...)
	if err := saveHeartbeatSession(session); err != nil {
		session.disk.PendingUsage = session.disk.PendingUsage[:len(session.disk.PendingUsage)-len(created)]
		return err
	}
	return rt.replayPendingUsage(ctx, projectID, session)
}

type usageAck struct {
	Sequence int64
	Input    *int64
	Output   *int64
	Cached   *int64
}

func (rt *runtime) replayPendingUsage(ctx context.Context, projectID string, session *heartbeatSession) error {
	for len(session.disk.PendingUsage) > 0 {
		pending := session.disk.PendingUsage[0]
		if err := rt.postPendingUsage(ctx, projectID, session, pending); err != nil {
			return err
		}
	}
	return nil
}

func (rt *runtime) postPendingUsage(ctx context.Context, projectID string, session *heartbeatSession, pending heartbeatPendingUsage) error {
	provisional := true
	body := map[string]any{
		"report_id":           pending.ReportID,
		"model":               pending.Model,
		"sequence":            pending.Sequence,
		"input_tokens":        pending.Input,
		"output_tokens":       pending.Output,
		"cached_input_tokens": pending.Cached,
		"provisional":         provisional,
		"billing_mode":        "unknown",
	}
	var result struct {
		Usage struct {
			Sequence          int64  `json:"sequence"`
			InputTokens       *int64 `json:"input_tokens"`
			OutputTokens      *int64 `json:"output_tokens"`
			CachedInputTokens *int64 `json:"cached_input_tokens"`
		} `json:"usage"`
	}
	path := harnessPath(projectID, session.id) + "/usage"
	err := rt.harnessDoCtx(ctx, http.MethodPost, path, session.lease, body, &result)
	if heartbeatTerminalStatus(err) {
		return err
	}
	if heartbeatStatus(err) == http.StatusConflict {
		return rt.reconcileUsage(ctx, projectID, session, pending)
	}
	if err != nil {
		return err
	}
	commitUsage(session, pending, usageAck{
		Sequence: result.Usage.Sequence,
		Input:    result.Usage.InputTokens,
		Output:   result.Usage.OutputTokens,
		Cached:   result.Usage.CachedInputTokens,
	})
	return nil
}

func (rt *runtime) reconcileUsage(ctx context.Context, projectID string, session *heartbeatSession, pending heartbeatPendingUsage) error {
	var page struct {
		Items []struct {
			Model             string `json:"model"`
			Sequence          int64  `json:"sequence"`
			InputTokens       *int64 `json:"input_tokens"`
			OutputTokens      *int64 `json:"output_tokens"`
			CachedInputTokens *int64 `json:"cached_input_tokens"`
		} `json:"items"`
	}
	if err := rt.harnessDoCtx(ctx, http.MethodGet, harnessPath(projectID, session.id)+"/usage", "", nil, &page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if item.Model != pending.Model || item.InputTokens == nil || item.OutputTokens == nil || item.CachedInputTokens == nil {
			continue
		}
		if item.Sequence < pending.Sequence || *item.InputTokens < pending.Input || *item.OutputTokens < pending.Output || *item.CachedInputTokens < pending.Cached {
			continue
		}
		commitUsage(session, pending, usageAck{Sequence: item.Sequence, Input: item.InputTokens, Output: item.OutputTokens, Cached: item.CachedInputTokens})
		return nil
	}
	return errors.New("usage report is still unacknowledged")
}

func commitUsage(session *heartbeatSession, pending heartbeatPendingUsage, ack usageAck) {
	seq, input, output, cached := pending.Sequence, pending.Input, pending.Output, pending.Cached
	if ack.Sequence > 0 {
		seq = ack.Sequence
	}
	if ack.Input != nil {
		input = *ack.Input
	}
	if ack.Output != nil {
		output = *ack.Output
	}
	if ack.Cached != nil {
		cached = *ack.Cached
	}
	next := heartbeatUsageDisk{Model: pending.Model, Sequence: seq, Input: input, Output: output, Cached: cached}
	if prev := usageByModel(session.disk.Usage, pending.Model); prev == nil {
		session.disk.Usage = append(session.disk.Usage, next)
	} else {
		*prev = next
	}
	if len(session.disk.PendingUsage) > 0 {
		session.disk.PendingUsage = session.disk.PendingUsage[1:]
	}
	if pending.Offset > session.disk.UsageOffset {
		session.disk.UsageOffset = pending.Offset
	}
	if len(pending.Recent) > 0 {
		session.disk.UsageRecent = trimRecent(pending.Recent)
	}
}

func codexSessionLabel(path, sessionID string) (string, bool) {
	return readCodexSessionLabel(context.Background(), path, sessionID)
}

func readCodexSessionLabel(ctx context.Context, path, sessionID string) (string, bool) {
	if ctx.Err() != nil || path == "" || !validUUID(sessionID) || unsafeHeartbeatPath(path) {
		return "", false
	}
	f, err := openNoFollow(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > heartbeatIndexMax {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, heartbeatIndexMax+1))
	if err != nil || len(raw) > heartbeatIndexMax || ctx.Err() != nil {
		return "", false
	}
	var found string
	var matched bool
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || len(line) > heartbeatTitleLineMax {
			continue
		}
		var rec struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &rec) != nil || !strings.EqualFold(rec.ID, sessionID) {
			continue
		}
		matched = true
		found = heartbeatText(rec.ThreadName, 128)
	}
	if !matched || found == "" {
		return "", false
	}
	return found, true
}

func claudeSessionLabel(o heartbeatOptions, sessionID string) (string, bool) {
	return readClaudeSessionLabel(context.Background(), o, sessionID)
}

func readClaudeSessionLabel(ctx context.Context, o heartbeatOptions, sessionID string) (string, bool) {
	if label, ok := claudeTitleFile(ctx, o.Transcript); ok {
		return label, true
	}
	return claudeTitleFile(ctx, findClaudeTranscript(o.ClaudeProjects, sessionID))
}

func claudeTitleFile(ctx context.Context, path string) (string, bool) {
	if ctx.Err() != nil || path == "" || unsafeHeartbeatPath(path) {
		return "", false
	}
	f, err := openNoFollow(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", false
	}
	reader := bufio.NewReader(f)
	if st.Size() > heartbeatTitleScanMax {
		if _, err := f.Seek(st.Size()-heartbeatTitleScanMax, io.SeekStart); err != nil {
			return "", false
		}
		reader.Reset(f)
		_, _ = reader.ReadSlice('\n')
	}
	var custom, ai string
	var used int64
	for {
		if err := ctx.Err(); err != nil {
			return "", false
		}
		if used > heartbeatTitleScanMax {
			break
		}
		line, overflow, err := readLimitedLine(reader, heartbeatTitleLineMax)
		used += int64(len(line)) + 1
		if !overflow && len(line) > 0 && (bytes.Contains(line, []byte("Title")) || bytes.Contains(line, []byte("title"))) {
			var rec struct {
				Type        string `json:"type"`
				CustomTitle string `json:"customTitle"`
				AITitle     string `json:"aiTitle"`
			}
			if json.Unmarshal(line, &rec) == nil {
				switch rec.Type {
				case "custom-title":
					if label := heartbeatText(rec.CustomTitle, 128); label != "" {
						custom = label
					}
				case "ai-title":
					if label := heartbeatText(rec.AITitle, 128); label != "" {
						ai = label
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !overflow {
			return "", false
		}
	}
	if custom != "" {
		return custom, true
	}
	if ai != "" {
		return ai, true
	}
	return "", false
}

func findClaudeTranscript(root, sessionID string) string {
	if root == "" || !validUUID(sessionID) || unsafeHeartbeatPath(root) {
		return ""
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	name := strings.ToLower(sessionID) + ".jsonl"
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), name)
		st, err := os.Lstat(candidate)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			continue
		}
		return candidate
	}
	return ""
}

type usageSum struct {
	input, output, cached int64
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func scanUsageWindow(ctx context.Context, path, fallback string, offset, maxBytes int64, recent []string) (map[string]usageSum, int64, []string, error) {
	if ctx.Err() != nil {
		return nil, offset, recent, ctx.Err()
	}
	if path == "" || unsafeHeartbeatPath(path) || maxBytes <= 0 {
		return nil, offset, recent, nil
	}
	f, err := openNoFollow(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, recent, nil
		}
		return nil, offset, recent, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, recent, err
	}
	if offset > st.Size() {
		return nil, offset, recent, errors.New("usage transcript shrank")
	}
	if offset == st.Size() {
		return nil, offset, recent, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, recent, err
	}
	counter := &countingReader{r: f}
	reader := bufio.NewReader(counter)
	position := func() int64 { return counter.n - int64(reader.Buffered()) }
	sums := map[string]usageSum{}
	poisoned := map[string]bool{}
	seen := map[string]bool{}
	ring := append([]string{}, recent...)
	for _, id := range ring {
		seen[id] = true
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, offset, recent, err
		}
		at := position()
		if at >= maxBytes {
			return sums, offset + at, trimRecent(ring), nil
		}
		line, overflow, err := readLimitedLine(reader, heartbeatUsageLineMax)
		next := position()
		if !overflow && len(line) > 0 {
			if lineErr := noteUsageLine(line, fallback, sums, poisoned, seen, &ring); lineErr != nil {
				return nil, offset, recent, lineErr
			}
		}
		if errors.Is(err, io.EOF) {
			return sums, offset + next, trimRecent(ring), nil
		}
		if err != nil && !overflow {
			return nil, offset, recent, err
		}
		if len(seen) >= heartbeatUsageSeenMax {
			return sums, offset + next, trimRecent(ring), nil
		}
	}
}

func noteUsageLine(line []byte, fallback string, sums map[string]usageSum, poisoned, seen map[string]bool, ring *[]string) error {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return nil
	}
	var rec struct {
		UUID    string `json:"uuid"`
		Type    string `json:"type"`
		Message struct {
			Model string                     `json:"model"`
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" || len(rec.Message.Usage) == 0 {
		return nil
	}
	if rec.UUID != "" {
		if seen[rec.UUID] {
			return nil
		}
		if len(seen) >= heartbeatUsageSeenMax {
			return nil
		}
		seen[rec.UUID] = true
		*ring = append(*ring, rec.UUID)
	}
	input, okIn := jsonToken(rec.Message.Usage["input_tokens"])
	output, okOut := jsonToken(rec.Message.Usage["output_tokens"])
	if !okIn || !okOut {
		return nil
	}
	cached := int64(0)
	if raw, ok := rec.Message.Usage["cache_read_input_tokens"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(raw) != "null" {
		var okCached bool
		cached, okCached = jsonToken(raw)
		if !okCached {
			return nil
		}
	}
	inclusive, ok := addTokens(input, cached)
	if !ok {
		return errUsageOverflow
	}
	model := heartbeatText(rec.Message.Model, 128)
	if model == "" {
		model = fallback
	}
	if model == "" || !heartbeatModelRE.MatchString(model) || poisoned[model] {
		return nil
	}
	cur := sums[model]
	nextIn, ok1 := addTokens(cur.input, inclusive)
	nextOut, ok2 := addTokens(cur.output, output)
	nextCached, ok3 := addTokens(cur.cached, cached)
	if !ok1 || !ok2 || !ok3 {
		poisoned[model] = true
		delete(sums, model)
		return nil
	}
	sums[model] = usageSum{input: nextIn, output: nextOut, cached: nextCached}
	return nil
}

func readLimitedLine(r *bufio.Reader, maxLine int) ([]byte, bool, error) {
	var line []byte
	overflow := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 && !overflow {
			if len(line)+len(chunk) > maxLine+1 {
				overflow = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if overflow {
			return nil, true, err
		}
		return bytes.TrimRight(line, "\r\n"), false, err
	}
}

func trimRecent(ids []string) []string {
	if len(ids) > heartbeatUsageRecent {
		ids = ids[len(ids)-heartbeatUsageRecent:]
	}
	return ids
}

func usageByModel(items []heartbeatUsageDisk, model string) *heartbeatUsageDisk {
	for i := range items {
		if items[i].Model == model {
			return &items[i]
		}
	}
	return nil
}

func usageReportID(session, model string, seq, input, output, cached int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("aeon-heartbeat-usage-v1\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d", session, model, seq, input, output, cached)))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func unsafeHeartbeatPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "" || strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".key") || strings.Contains(base, "credential")
}
