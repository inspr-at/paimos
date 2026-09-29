// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCacheCreationAndConfigDir(t *testing.T) {
	dir := t.TempDir()
	var calls []hbCall
	srv := hbServer(t, &calls, nil)
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)
	path := claudeUsagePath(t, dir, "cache.jsonl")
	line := `{"uuid":"11111111-1111-4111-8111-111111111112","type":"assistant","message":{"model":"claude-sonnet-4","usage":{"input_tokens":12,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":28}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatTestOptions(dir)
	opts.Transcript = path
	opts.BillingMode = "api"
	session := openUsageSession(t, rt, opts)
	if err := rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted := usagePosts(calls)
	if len(posted) != 1 || numField(posted[0], "input_tokens") != 42 || numField(posted[0], "cached_input_tokens") != 2 || numField(posted[0], "output_tokens") != 4 {
		t.Fatalf("cache creation omitted: %#v", posted)
	}

	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	configured := heartbeatOptions{}
	configured.normalize()
	if configured.ClaudeProjects != filepath.Join(home, "projects") {
		t.Fatalf("config dir projects %s", configured.ClaudeProjects)
	}
	explicit := heartbeatOptions{ClaudeProjects: filepath.Join(home, "explicit")}
	explicit.normalize()
	if explicit.ClaudeProjects != filepath.Join(home, "explicit") {
		t.Fatalf("explicit projects %s", explicit.ClaudeProjects)
	}
}

func TestHarnessUsageSourcesReportMonotonicTotals(t *testing.T) {
	dir := t.TempDir()
	var calls []hbCall
	srv := hbServer(t, &calls, nil)
	defer srv.Close()
	rt, _, _ := heartbeatRuntime(t, srv)

	claudePath := claudeUsagePath(t, dir, "claude.jsonl")
	raw, err := os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "claude_assistant.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(claudePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatTestOptions(dir)
	opts.Transcript = claudePath
	opts.BillingMode = "api"
	session := openUsageSession(t, rt, opts)
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted := usagePosts(calls)
	if len(posted) != 1 || posted[0]["model"] != "claude-sonnet-4" || numField(posted[0], "input_tokens") != 21 || numField(posted[0], "cached_input_tokens") != 2 || numField(posted[0], "output_tokens") != 4 || posted[0]["billing_mode"] != "api" || posted[0]["subscription_label"] != nil {
		t.Fatalf("claude transcript: %#v", posted)
	}
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	if len(usagePosts(calls)) != 1 {
		t.Fatal("claude transcript was reported twice")
	}

	codexDir := t.TempDir()
	codexPath := filepath.Join(codexDir, "sessions", "rollout-test.jsonl")
	if err = os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "codex_token_count.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(codexPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts = heartbeatTestOptions(codexDir)
	opts.Harness = "codex"
	opts.UsageSource = "codex"
	opts.UsageFile = codexPath
	opts.Model = "gpt-5"
	session = openUsageSession(t, rt, opts)
	before := len(usagePosts(calls))
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)[before:]
	if len(posted) != 1 || posted[0]["model"] != "gpt-5" || numField(posted[0], "input_tokens") != 150 || numField(posted[0], "output_tokens") != 35 || numField(posted[0], "cached_input_tokens") != 80 {
		t.Fatalf("codex cumulative: %#v", posted)
	}
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	if len(usagePosts(calls)) != before+1 {
		t.Fatal("unchanged codex rollout was added again")
	}
	extra := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":80,"output_tokens":40,"total_tokens":240}}}}` + "\n"
	f, err := os.OpenFile(codexPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(extra); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)
	last := posted[len(posted)-1]
	if len(posted) != before+2 || numField(last, "input_tokens") != 200 || numField(last, "output_tokens") != 40 || numField(last, "sequence") != 2 {
		t.Fatalf("codex increase: %#v", posted[before:])
	}

	cursorPath := filepath.Join(dir, "cursor.jsonl")
	raw, err = os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "cursor_result.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cursorPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	opts = heartbeatTestOptions(t.TempDir())
	opts.Harness = "cursor"
	opts.UsageFile = cursorPath
	session = openUsageSession(t, rt, opts)
	before = len(usagePosts(calls))
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)[before:]
	if len(posted) != 1 || posted[0]["model"] != "composer-2.5" || numField(posted[0], "input_tokens") != 42 || numField(posted[0], "cached_input_tokens") != 10 || numField(posted[0], "output_tokens") != 8 || posted[0]["billing_mode"] != "unknown" {
		t.Fatalf("cursor delta: %#v", posted)
	}
	if strings.Contains(string(raw), "MUST_NOT_LEAK") {
		for _, call := range usagePosts(calls) {
			for _, value := range call {
				if text, ok := value.(string); ok && strings.Contains(text, "MUST_NOT_LEAK") {
					t.Fatal("usage post included fixture text")
				}
			}
		}
	}

	grokPath := filepath.Join(dir, "sessions", "work", "usage.json")
	if err = os.MkdirAll(filepath.Dir(grokPath), 0o700); err != nil {
		t.Fatal(err)
	}
	first := `{"session":{"inputTokens":10,"outputTokens":4,"cachedReadTokens":2,"cacheCreationTokens":1,"primaryModelId":"grok-4","costUsdTicks":11}}`
	second := `{"session":{"inputTokens":20,"outputTokens":4,"cachedReadTokens":2,"cacheCreationTokens":1,"primaryModelId":"grok-4","costUsdTicks":11}}`
	if len(first) != len(second) {
		t.Fatalf("rewrite lengths %d %d", len(first), len(second))
	}
	if err = os.WriteFile(grokPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	opts = heartbeatTestOptions(t.TempDir())
	opts.Harness = "grok"
	opts.UsageFile = grokPath
	opts.BillingMode = "subscription"
	opts.SubscriptionLabel = "Grok Pro"
	session = openUsageSession(t, rt, opts)
	before = len(usagePosts(calls))
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)[before:]
	if len(posted) != 1 || posted[0]["model"] != "grok-4" || numField(posted[0], "input_tokens") != 13 || numField(posted[0], "cached_input_tokens") != 2 || numField(posted[0], "output_tokens") != 4 || posted[0]["billing_mode"] != "subscription" || posted[0]["subscription_label"] != "Grok Pro" {
		t.Fatalf("grok snapshot: %#v", posted)
	}
	if err = os.WriteFile(grokPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	if len(usagePosts(calls)) != before+1 {
		t.Fatal("unchanged grok snapshot was reported again")
	}
	if err = os.WriteFile(grokPath, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)
	last = posted[len(posted)-1]
	if len(posted) != before+2 || numField(last, "input_tokens") != 23 || numField(last, "sequence") != 2 {
		t.Fatalf("grok rewrite: %#v", posted[before:])
	}

	fixture, err := os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "grok_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(grokPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	opts = heartbeatTestOptions(t.TempDir())
	opts.Harness = "grok"
	opts.UsageFile = grokPath
	opts.BillingMode = "unknown"
	session = openUsageSession(t, rt, opts)
	before = len(usagePosts(calls))
	if err = rt.reportHeartbeatUsage(context.Background(), transcriptProjectID, opts, session); err != nil {
		t.Fatal(err)
	}
	posted = usagePosts(calls)[before:]
	if len(posted) != 2 || posted[0]["model"] != "grok-4" || numField(posted[0], "input_tokens") != 88 || posted[1]["model"] != "grok-4-fast" || numField(posted[1], "input_tokens") != 25 {
		t.Fatalf("grok models: %#v", posted)
	}
}

func TestUsageLocatorsSkipVendorAuth(t *testing.T) {
	dir := t.TempDir()
	id := "thread_01ab"
	home := filepath.Join(dir, "codex-home")
	newestDir := filepath.Join(home, "sessions", "2026", "09", "29")
	olderDir := filepath.Join(home, "sessions", "2026", "09", "28")
	if err := os.MkdirAll(newestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(olderDir, 0o700); err != nil {
		t.Fatal(err)
	}
	newest := filepath.Join(newestDir, "rollout-2026-09-29T00-00-00-"+id+".jsonl")
	older := filepath.Join(olderDir, "rollout-old-"+id+".jsonl")
	if err := os.WriteFile(newest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(older, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newest, filepath.Join(newestDir, "rollout-zzz-"+id+".jsonl")); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatTestOptions(dir)
	opts.Harness = "codex"
	opts.CodexHome = home
	opts.UsageID = id
	target, err := resolveHeartbeatUsage(opts)
	if err != nil || target.Path != newest || target.Source != "codex" || target.Snapshot {
		t.Fatalf("codex locate %+v %v", target, err)
	}
	opts.UsageID = ""
	target, err = resolveHeartbeatUsage(opts)
	if err != nil || target.Path != "" {
		t.Fatalf("codex scanned without an id: %+v %v", target, err)
	}

	work := "/tmp/work space"
	encoded := encodeURIComponent(work)
	if encoded != "%2Ftmp%2Fwork%20space" {
		t.Fatalf("encode %s", encoded)
	}
	gid := "0123456789abcdef"
	grokHome := filepath.Join(dir, "grok-home")
	primary := filepath.Join(grokHome, "sessions", encoded, gid, "usage.json")
	fallback := filepath.Join(grokHome, "sessions", "other", gid, "usage.json")
	if err = os.MkdirAll(filepath.Dir(primary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(fallback), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(primary, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fallback, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Harness = "grok"
	opts.GrokHome = grokHome
	opts.UsageID = gid
	opts.Worktree = work
	target, err = resolveHeartbeatUsage(opts)
	if err != nil || target.Path != primary || !target.Snapshot {
		t.Fatalf("grok locate %+v %v", target, err)
	}

	opts = heartbeatTestOptions(dir)
	opts.Harness = "cursor"
	cursor := filepath.Join(opts.StateDir, "cursor.jsonl")
	if err = os.MkdirAll(opts.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cursor, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err = resolveHeartbeatUsage(opts)
	if err != nil || target.Path != cursor || target.Source != "cursor" {
		t.Fatalf("cursor locate %+v %v", target, err)
	}

	for _, name := range []string{"auth.json", "credentials.json", "id_ed25519", ".env", "token.key", "seal.age"} {
		opts.UsageFile = filepath.Join(dir, name)
		if _, err = resolveHeartbeatUsage(opts); err == nil {
			t.Fatalf("%s accepted", name)
		}
		opts.UsageFile = ""
		if err = os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		prepared := opts
		prepared.UsageFile = filepath.Join(dir, name)
		if err = prepared.prepare(); err == nil {
			t.Fatalf("prepare accepted %s", name)
		}
	}
	if usageID("abc-def/../x") || usageID("-abcdefgh") || usageID("short") || !usageID(id) {
		t.Fatal("usage id fence")
	}
	opts.UsageFile = ""
	opts.BillingMode = "api"
	opts.SubscriptionLabel = "pro"
	if err = opts.prepare(); err == nil {
		t.Fatal("subscription label accepted with api billing")
	}
}

func TestCodexTurnContextModelBeatsFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "rollout-model.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-5.4\"}}\n" +
		"{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":150,\"cached_input_tokens\":80,\"output_tokens\":35,\"reasoning_output_tokens\":4,\"total_tokens\":185}}}}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sums, _, _, _, err := scanUsageWindowSource(context.Background(), path, "gpt-5", 0, 1<<20, nil, false, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums["gpt-5.4"].input != 150 || sums["gpt-5.4"].output != 35 || sums["gpt-5.4"].cached != 80 {
		t.Fatalf("context model lost: %#v", sums)
	}
	bare := filepath.Join(dir, "sessions", "rollout-bare.jsonl")
	if err := os.WriteFile(bare, []byte(body[strings.Index(body, "\n")+1:]), 0o600); err != nil {
		t.Fatal(err)
	}
	sums, _, _, _, err = scanUsageWindowSource(context.Background(), bare, "", 0, 1<<20, nil, false, "codex")
	if err != nil || len(sums) != 0 {
		t.Fatalf("model-less rollout reported %#v %v", sums, err)
	}
}

func TestUsageFilePostsOnEveryBeat(t *testing.T) {
	var calls []hbCall
	srv := hbServer(t, &calls, nil)
	defer srv.Close()
	rt, _, stderr := heartbeatRuntime(t, srv)
	cases := []struct {
		name    string
		harness string
		write   func(dir string) string
		model   string
		input   float64
	}{
		{
			name: "grok", harness: "grok", model: "grok-4", input: 13,
			write: func(dir string) string {
				path := filepath.Join(dir, "sessions", "work", "usage.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				body := `{"session":{"inputTokens":10,"outputTokens":4,"cachedReadTokens":2,"cacheCreationTokens":1,"primaryModelId":"grok-4"}}`
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "codex", harness: "codex", model: "gpt-5", input: 150,
			write: func(dir string) string {
				path := filepath.Join(dir, "sessions", "rollout-beat.jsonl")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "codex_token_count.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "cursor", harness: "cursor", model: "composer-2.5", input: 42,
			write: func(dir string) string {
				path := filepath.Join(dir, "cursor.jsonl")
				raw, err := os.ReadFile(filepath.Join("..", "sessionusage", "testdata", "cursor_result.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := heartbeatTestOptions(dir)
			opts.Harness = tc.harness
			opts.UsageSource = tc.harness
			opts.UsageFile = tc.write(dir)
			opts.Transcript = ""
			if tc.harness == "codex" {
				opts.Model = "gpt-5"
			}
			session := openUsageSession(t, rt, opts)
			before := len(usagePosts(calls))
			if err := rt.heartbeatBeat(context.Background(), opts, heartbeatDeps{}, session); err != nil {
				t.Fatalf("beat: %v stderr %s", err, stderr.String())
			}
			posted := usagePosts(calls)[before:]
			if len(posted) != 1 || posted[0]["model"] != tc.model || numField(posted[0], "input_tokens") != tc.input {
				t.Fatalf("beat usage %#v stderr %s", posted, stderr.String())
			}
		})
	}
}

func TestUsageAllowlistRefusesSecretFilesAndSymlinkParents(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "sessions", "work", "sid")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := filepath.Join(real, "usage.json")
	body := []byte(`{"session":{"inputTokens":1,"outputTokens":1,"cachedReadTokens":0,"cacheCreationTokens":0,"primaryModelId":"grok-4"}}`)
	if err := os.WriteFile(usage, body, 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := readGrokUsage(usage, "grok-4")
	if err != nil || len(lines) != 1 || lines[0].Input != 1 {
		t.Fatalf("direct usage read: %+v %v", lines, err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "link", "sessions", "work", "sid", "usage.json")
	if _, err := readGrokUsage(linked, "grok-4"); err == nil {
		t.Fatal("opened grok usage through a symlinked parent")
	}
	cursorReal := filepath.Join(dir, "real", "cursor.jsonl")
	if err := os.WriteFile(cursorReal, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanUsageWindowSource(context.Background(), filepath.Join(dir, "link", "cursor.jsonl"), "", 0, 1<<20, nil, false, "cursor"); err == nil {
		t.Fatal("opened cursor.jsonl through a symlinked parent")
	}

	names := []string{
		"cli-config.json", "auth.json", ".credentials.json", ".credentials",
		"cookies", "cookies.db", "cookies.binarycookies",
		"keychain", "login.keychain", "login.keychain-db",
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		projects := filepath.Join(dir, "projects", name)
		if err := os.MkdirAll(filepath.Dir(projects), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(projects, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := scanUsageWindowSource(context.Background(), projects, "", 0, 1<<20, nil, false, "claude"); err == nil {
			t.Fatalf("claude scan opened %s", name)
		}
		sessions := filepath.Join(dir, "sessions", name)
		if err := os.WriteFile(sessions, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGrokUsage(sessions, "grok-4"); err == nil {
			t.Fatalf("grok read opened %s", name)
		}
		opts := heartbeatTestOptions(dir)
		opts.Harness = "cursor"
		opts.UsageSource = "cursor"
		opts.UsageFile = projects
		if _, err := resolveHeartbeatUsage(opts); err == nil {
			t.Fatalf("resolve accepted %s", name)
		}
		if err := opts.prepare(); err == nil {
			t.Fatalf("prepare accepted %s", name)
		}
	}
	keydir := filepath.Join(dir, "projects", "Keychains")
	if err := os.MkdirAll(keydir, 0o700); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(keydir, "session.jsonl")
	if err := os.WriteFile(hidden, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanUsageWindowSource(context.Background(), hidden, "", 0, 1<<20, nil, false, "claude"); err == nil {
		t.Fatal("opened a transcript inside Keychains")
	}
	cookiedir := filepath.Join(dir, "cookie-home", "sessions", "Cookies")
	if err := os.MkdirAll(cookiedir, 0o700); err != nil {
		t.Fatal(err)
	}
	cookieUsage := filepath.Join(cookiedir, "usage.json")
	if err := os.WriteFile(cookieUsage, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGrokUsage(cookieUsage, "grok-4"); err == nil {
		t.Fatal("opened usage.json inside Cookies")
	}
	rollout := filepath.Join(dir, "sessions", "rollout-cli-config.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := heartbeatTestOptions(dir)
	opts.Harness = "codex"
	opts.UsageSource = "codex"
	opts.UsageFile = filepath.Join(dir, "sessions", "cli-config.json")
	if err := os.WriteFile(opts.UsageFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHeartbeatUsage(opts); err == nil {
		t.Fatal("codex resolve accepted cli-config.json")
	}
	opts.UsageFile = rollout
	target, err := resolveHeartbeatUsage(opts)
	if err != nil || target.Path != rollout {
		t.Fatalf("codex rollout %+v %v", target, err)
	}
	if _, _, _, _, err := scanUsageWindowSource(context.Background(), rollout, "gpt-5", 0, 1<<20, nil, false, "codex"); err != nil {
		t.Fatalf("codex rollout scan: %v", err)
	}
}

func openUsageSession(t *testing.T, rt *runtime, opts heartbeatOptions) *heartbeatSession {
	t.Helper()
	session, _, err := rt.openHeartbeatSession(context.Background(), opts, heartbeatDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.hold.release() })
	return &session
}

func usagePosts(calls []hbCall) []map[string]any {
	var out []map[string]any
	for _, call := range hbWhere(calls, http.MethodPost, "/usage") {
		out = append(out, call.body)
	}
	return out
}

func numField(body map[string]any, key string) float64 {
	value, _ := body[key].(float64)
	return value
}
