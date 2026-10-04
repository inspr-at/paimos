// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/jackc/pgx/v5"
)

func TestPersonAPIErrorPreservesStatusAndRedactsSession(t *testing.T) {
	const cookie = "synthetic-person-session"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("aeon_session"); err != nil || ck.Value != cookie {
			t.Error("request must use the explicit person session")
		}
		http.Error(w, "missing record for "+cookie, http.StatusNotFound)
	}))
	defer srv.Close()
	rt := &runtime{personClient: client.NewSession(srv.URL, cookie)}
	err := rt.do(http.MethodGet, "/api/missing", nil, nil)
	var exit *exitError
	if !errors.As(err, &exit) || exit.apiStatus != http.StatusNotFound {
		t.Fatal("person API error lost its HTTP status classification")
	}
	if strings.Contains(exit.msg, cookie) || !strings.Contains(exit.msg, "[redacted]") {
		t.Fatal("person API error must redact the session")
	}
}

func TestTellPersonHeldReplyUsesSessionCookie(t *testing.T) {
	isolate(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, person, agent, identity, project, parent string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('tell-person','Tell person') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	for i, id := range []*string{&person, &agent} {
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,$3) RETURNING id::text`, tid, []string{"person", "agent"}[i], []string{"owner", "worker"}[i]).Scan(id); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, *id, "owner")
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO identities(issuer,subject) VALUES('test','tell-person') RETURNING id::text`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	rawToken := bytes.Repeat([]byte{5}, 32)
	cookie := hex.EncodeToString(rawToken)
	hash := sha256.Sum256(rawToken)
	if _, err := d.Admin.Exec(ctx, `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, hex.EncodeToString(hash[:]), identity, tid, person); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'TELL-1','Tell','{"project_key":"TELL"}' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		var ev int64
		if err := tx.QueryRow(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'inbox.compat_sent','{}') RETURNING id`, tid, agent).Scan(&ev); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO inbox_compat_messages(tenant_id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,thread_id,hop,sent_event_id,is_action_request,expects_reply,delivery_level) VALUES($1,$2,$3,$4,'paimos:owner','held','key','digest','thread',1,$5,true,true,'simple') RETURNING id::text`, tid, project, agent, person, ev).Scan(&parent)
	}); err != nil {
		t.Fatal(err)
	}
	am, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	qm := questions.New(d.App)
	im, err := inbox.NewMessaging(d.App, bytes.Repeat([]byte{7}, 32), inbox.WithHeldReplyBridge(qm.ReplyHeld))
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.Server{Pool: d.App, Modules: []httpapi.Module{am, nodes.New(d.App, nodes.SQLWriter{}), qm, im, inbox.New(d.App)}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}
	handler := api.Handler()
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("aeon_session")
		if err != nil || ck.Value != cookie || len(r.Header.Values("Authorization")) != 0 {
			t.Error("person CLI request did not use cookie-only authentication")
		}
		if r.Method == "POST" && r.URL.Path == "/api/projects/"+project+"/messages" {
			posts.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cookiePath := filepath.Join(t.TempDir(), "session-cookie")
	if err := os.WriteFile(cookiePath, []byte(cookie+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Ambient agent credentials and a harness generation must not replace the
	// explicit person's authentication or become a sender-session binding.
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", "synthetic-agent-key-must-not-be-used")
	t.Setenv("AEON_SESSION_ID", "00000000-0000-4000-8000-000000000123")
	for _, program := range []string{"aeon", "paimos"} {
		var out, errOut bytes.Buffer
		code := RunMessaging([]string{program, "--config", filepath.Join(t.TempDir(), "missing"), "tell", agent, "--project", "TELL", "--reply-to", parent, "--session-cookie-file", cookiePath, "--idempotency-key", "person-reply", "-m", "Proceed"}, strings.NewReader(""), &out, &errOut)
		if strings.Contains(out.String()+errOut.String(), cookie) {
			t.Fatal("session credential leaked")
		}
		if code != 0 || !strings.Contains(out.String(), "reply pending") {
			t.Errorf("person tell exit=%d output=%s error=%s", code, out.String(), errOut.String())
		}
	}
	if posts.Load() != 2 {
		t.Errorf("actual CLI reply requests=%d, want 2", posts.Load())
	}
	var answers, messages int
	if err := d.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM desk_answers),(SELECT count(*) FROM inbox_messages)`).Scan(&answers, &messages); err != nil {
		t.Fatal(err)
	}
	if answers != 1 || messages != 0 {
		t.Errorf("answers=%d messages=%d: require one durable answer behind grace", answers, messages)
	}
}
