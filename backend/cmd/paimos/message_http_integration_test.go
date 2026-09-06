package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
)

// Exercise the actual CLI serialization, HTTP authorization, handler, and
// modernc SQLite constraint together. A transport fixture accepting any body
// cannot catch message files being rejected by a scalar-only database guard.
func TestTellRealHTTPMultilineFileAndStdin(t *testing.T) {
	oldDB := db.DB
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "multiline-cli-fixture")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DB.Close(); db.DB = oldDB })

	exec := func(query string, args ...any) int64 {
		t.Helper()
		result, err := db.DB.Exec(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	user := exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('multiline-cli','disabled','admin','super_admin','active',1)`)
	project := exec(`INSERT INTO projects(name,key) VALUES('Multiline CLI','MLC')`)
	sender := exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, project)
	receiver := exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, project)
	exec(`INSERT INTO agent_message_allowlist(receiver_agent_id,sender_agent_id) VALUES(?,?)`, receiver, sender)
	key := brand.Default.APIKeyPrefix + "multiline-cli-fixture"
	digest := sha256.Sum256([]byte(key))
	exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture',?,'fixture','*')`, user, hex.EncodeToString(digest[:]))

	router := chi.NewRouter()
	router.Use(auth.Middleware)
	router.Route("/api", func(r chi.Router) {
		r.Get("/projects", handlers.ListProjects)
		handlers.RegisterAgentMessageRoutes(r)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, key)
	t.Setenv(envAPIKeyFile, "")
	t.Setenv(envPPMURL, "")
	t.Setenv(envPPMAPIKey, "")
	oldAgent, oldSession := flagAgentName, flagSessionID
	t.Cleanup(func() { flagAgentName, flagSessionID = oldAgent, oldSession })

	tell := func(t *testing.T, body, source string) (string, string, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "message.txt")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if source == "stdin" {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdin
			os.Stdin = file
			t.Cleanup(func() { os.Stdin = previous; _ = file.Close() })
			path = "-"
		}
		return executeCLIForTest(t, "--json", "--agent-name", "sender", "tell", "claude:receiver", "--project", "MLC", "--level", "simple", "--message-file", path)
	}
	count := func(t *testing.T, query string) int {
		t.Helper()
		var n int
		if err := db.DB.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// These go first so zero means no message, delivery, or idempotency row
	// exists at all, rather than merely a failed lookup of a returned ID.
	for _, tc := range []struct{ name, source, body string }{
		{"split_bearer_file", "file", "Observation\nBearer\nfixturevalue123456789\n"},
		{"split_assignment_stdin", "stdin", "Observation\r\napi_key\r\n=\r\nfixturevalue123456789\r\n"},
		{"nul_file", "file", "Observation\x00Second line\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := tell(t, tc.body, tc.source)
			var problem struct {
				Code      int    `json:"code"`
				ErrorCode string `json:"error_code"`
			}
			if err == nil || out != "" || json.Unmarshal([]byte(errOut), &problem) != nil || problem.Code != 400 || problem.ErrorCode != "agent_message_secret_rejected" {
				t.Fatalf("unsafe body refusal: error=%v status=%d code=%q stdout_empty=%v", err != nil, problem.Code, problem.ErrorCode, out == "")
			}
			for _, query := range []string{"SELECT COUNT(*) FROM agent_messages", "SELECT COUNT(*) FROM agent_message_deliveries", "SELECT COUNT(*) FROM agent_message_idempotency"} {
				if count(t, query) != 0 {
					t.Fatal("rejected input left a durable ledger effect")
				}
			}
		})
	}

	for _, tc := range []struct{ name, source, body string }{
		{"file_trailing_lf", "file", "Ordinary observation.\n"},
		{"file_lf_blank_lines", "file", "First observation.\n\nSecond observation.\n"},
		{"file_crlf_blank_lines", "file", "First observation.\r\n\r\nSecond observation.\r\n"},
		{"stdin_lf_blank_lines", "stdin", "  First observation.\n\nSecond observation.  \n"},
		{"stdin_crlf_blank_lines", "stdin", "  First observation.\r\n\r\nSecond observation.  \r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := count(t, "SELECT COUNT(*) FROM agent_messages")
			out, _, err := tell(t, tc.body, tc.source)
			if err != nil {
				t.Fatalf("ordinary multiline input refused: %v", err)
			}
			var envelope messageEnvelope
			if err := json.Unmarshal([]byte(out), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.MessageID == "" || !envelope.Delivered || envelope.From != "paimos:sender" || envelope.To != "claude:receiver" || len(envelope.Parts) != 1 || envelope.Parts[0].Text != tc.body {
				t.Fatal("HTTP envelope lost exact body bytes or canonical attribution")
			}
			var storedBody, storedParts string
			if err := db.DB.QueryRow(`SELECT body,parts_json FROM agent_messages WHERE message_id=?`, envelope.MessageID).Scan(&storedBody, &storedParts); err != nil {
				t.Fatal(err)
			}
			var parts []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(storedParts), &parts) != nil || storedBody != tc.body || len(parts) != 1 || parts[0].Text != tc.body {
				t.Fatal("durable body or envelope parts changed whitespace")
			}
			if count(t, "SELECT COUNT(*) FROM agent_messages") != before+1 {
				t.Fatal("one CLI send did not create exactly one message")
			}
		})
	}
}
