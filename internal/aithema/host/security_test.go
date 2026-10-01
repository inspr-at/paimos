// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/websocket"
)

func TestDefaultOperatorServiceWriteRefusesLocalAddress(t *testing.T) {
	f := newFixture(t, nil)
	m, err := New(f.db.App, f.m.Journal, f.m.Keys, bytes.Repeat([]byte{5}, 32), hostOrigin)
	if err != nil {
		t.Fatal(err)
	}
	f.mux = http.NewServeMux()
	m.Mount(f.mux)
	for _, base := range []string{f.s.ServiceURL, strings.Replace(f.s.ServiceURL, "http:", "https:", 1)} {
		s := f.s
		s.ServiceURL = base
		if w := f.call("PUT", "/api/plugins/aithema/settings", s); w.Code != 400 {
			t.Error("tenant operator setting enabled local service access without deployment permission")
		}
	}
}

func TestOperatorLocalExactEndpointPolicy(t *testing.T) {
	for _, bad := range []string{"localhost", "localhost:*", "*.example:80", "localhost:0", "localhost:65536", "localhost:080", "localhost:80/", "user@localhost:80", "[fe80::1%lo0]:80", "", "localhost.:80"} {
		if _, err := newServicePolicy([]string{bad}); err == nil {
			t.Error("invalid server allowlist accepted")
		}
	}
	if _, err := newServicePolicy([]string{"localhost:80", "LOCALHOST:80"}); err == nil {
		t.Fatal("duplicate server allowlist accepted")
	}
	p, err := newServicePolicy([]string{"renderer.internal:8910"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, location, ip string
		allowed           bool
	}{
		{"http://renderer.internal:8910", "operator", "127.0.0.1", true},
		{"http://renderer.internal:8910", "operator", "8.8.8.8", false},
		{"https://renderer.internal:8910", "operator", "10.1.2.3", true},
		{"https://renderer.internal:8910", "operator", "fc00::1", true},
		{"http://renderer.internal:8911", "operator", "127.0.0.1", false},
		{"http://other.internal:8910", "operator", "127.0.0.1", false},
		{"https://renderer.internal:8910", "cloud", "127.0.0.1", false},
		{"http://renderer.internal:8910", "cloud", "8.8.8.8", false},
		{"https://renderer.internal:8910", "operator", "169.254.169.254", false},
		{"https://renderer.internal:8910", "operator", "100.64.0.1", false},
		{"https://renderer.internal:8910", "operator", "::", false},
		{"https://public.example", "operator", "127.0.0.1", false},
		{"https://public.example", "cloud", "8.8.8.8", true},
	} {
		p.resolve = func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP(tc.ip)}}, nil
		}
		_, err := p.addresses(t.Context(), Settings{ServiceURL: tc.url, Location: tc.location})
		if (err == nil) != tc.allowed {
			t.Errorf("unexpected address policy for %s %s", tc.url, tc.location)
		}
	}
	// A mixed answer is denied as a whole, rather than trying the public IP
	// first and falling back to a forbidden destination.
	p.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := p.addresses(t.Context(), Settings{ServiceURL: "https://public.example", Location: "operator"}); err == nil {
		t.Fatal("mixed DNS answer accepted")
	}
}

func TestServiceDNSRecheckedAfterWriteAndPinnedAtDial(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	u, _ := url.Parse(f.service.URL)
	s := f.s
	s.ServiceURL = "https://renderer.example:" + u.Port()
	var answers atomic.Int32
	p := servicePolicy{resolve: func(context.Context, string) ([]net.IPAddr, error) {
		ip := "8.8.8.8"
		if answers.Add(1) > 1 {
			ip = "127.0.0.1"
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}}
	f.m.servicePolicy = p
	if _, err := f.m.saveSettings(f.ctx(), f.p, settingsWrite{Settings: s}); err != nil {
		t.Fatal("public settings write failed")
	}
	if _, err := serviceClient(s, p).Get(s.ServiceURL); err == nil || calls.Load() != 0 || answers.Load() != 2 {
		t.Fatal("changed DNS answer bypassed dial check")
	}
	// The allowlisted hostname is deliberately nonexistent in real DNS. The
	// connection succeeds only if the checked literal is dialed without a
	// second hostname lookup; no network access outside the local fixture.
	p, err := newServicePolicy([]string{net.JoinHostPort("renderer.invalid", u.Port())})
	if err != nil {
		t.Fatal(err)
	}
	answers.Store(0)
	p.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		answers.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	s.ServiceURL = "http://renderer.invalid:" + u.Port()
	resp, err := serviceClient(s, p).Get(s.ServiceURL)
	if err != nil {
		t.Fatal("checked IP was not pinned at dial")
	}
	resp.Body.Close()
	if calls.Load() != 1 || answers.Load() != 1 {
		t.Fatal("dial repeated hostname resolution")
	}
	if _, err := serviceClient(s, p).Get(f.service.URL); err == nil || calls.Load() != 1 {
		t.Fatal("transport reached an unconfigured hostname")
	}
}

func TestDefaultOperatorServiceDialRefusesLocalAddress(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	s := Settings{ServiceURL: upstream.URL, Location: "operator"}
	resp, err := serviceClient(s).Get(upstream.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || calls.Load() != 0 {
		t.Fatal("tenant operator setting dialed loopback without deployment permission")
	}
}

// Inspect every JSON string, so a renamed or nested field cannot silently
// reintroduce a host-audience bearer in a person response.
func assertNoHostToken(t *testing.T, raw []byte) {
	t.Helper()
	var response any
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if _, exists := v["delegated_token"]; exists {
				t.Error("person response contains a delegated-token field")
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		case string:
			parts := strings.Split(v, ".")
			if len(parts) != 3 {
				return
			}
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			var c struct {
				Audience string `json:"aud"`
			}
			if err == nil && json.Unmarshal(payload, &c) == nil && c.Audience == hostOrigin {
				t.Error("person response contains a host-audience token")
			}
		}
	}
	visit(response)
}

func TestPersonSessionResponsesContainOnlySessionAudience(t *testing.T) {
	var delivered atomic.Bool
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			parts := strings.Split(r.Header.Get("X-Aithema-Delegated-Token"), ".")
			if len(parts) != 3 {
				t.Error("server delegated delivery missing")
			} else {
				payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
				var claims map[string]any
				json.Unmarshal(payload, &claims)
				if claims["aud"] != hostOrigin {
					t.Error("service token has wrong audience")
				}
				delivered.Store(true)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	creation := f.createSession()
	assertNoHostToken(t, creation.Body.Bytes())
	var s sessionTokens
	if err := json.Unmarshal(creation.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	f.deliver()
	if !delivered.Load() {
		t.Fatal("delegated token did not reach service")
	}
	w := f.call("POST", "/api/projects/"+f.project+"/aithema/sessions/"+s.Session+"/tokens", nil)
	if w.Code != 200 {
		t.Fatal("token refresh failed")
	}
	assertNoHostToken(t, w.Body.Bytes())
	var response map[string]any
	json.Unmarshal(w.Body.Bytes(), &response)
	token, ok := response["session_token"].(string)
	if !ok || token == "" {
		t.Fatal("browser session token missing")
	}
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(payload, &claims)
	if claims["aud"] != "aithema" || claims["capabilities"] != nil {
		t.Fatal("browser token granted host capabilities")
	}
}

func TestWebSocketCredentialReflectionBlocked(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/inline") {
			websocket.Handler(func(conn *websocket.Conn) {
				defer conn.Close()
				websocket.Message.Send(conn, r.Header.Get("Authorization"))
			}).ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	s := f.session()
	f.deliver()
	server := httptest.NewServer((&httpapi.Server{Modules: []httpapi.Module{f.m}, Middleware: []func(http.Handler) http.Handler{func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.p)))
		})
	}}}).Handler())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/projects/" + f.project + "/aithema/sessions/" + s.Session + "/proxy/inline?ticket=one-use"
	config, err := websocket.NewConfig(wsURL, hostOrigin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal("upgrade failed")
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	var received string
	err = websocket.Message.Receive(conn, &received)
	if strings.Contains(received, mockCredential) || err == nil {
		t.Fatal("reflecting service exposed its credential through WebSocket output")
	}
}

type credentialReadTracer struct{ reads atomic.Int32 }

func (tr *credentialReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "service_credential") {
		tr.reads.Add(1)
	}
	return ctx
}
func (*credentialReadTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestInvalidPreviewCapNeverReadsCredential(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<p>safe</p>"))
	})
	s := f.session()
	tracer := &credentialReadTracer{}
	config := f.db.App.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.m.Pool = pool
	handler := (&httpapi.Server{Modules: []httpapi.Module{f.m}}).Handler()
	design := strings.Repeat("a", 64)
	cap := f.cap(s.Session, design, nil)
	parts := strings.Split(cap, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	for _, cap := range []string{
		strings.Join(parts, "."),
		f.cap(s.Session, design, func(c map[string]any) { c["aud"] = "https://foreign.example" }),
		f.cap(s.Session, design, func(c map[string]any) { c["exp"] = time.Now().Unix() - 1 }),
		f.cap(s.Session, strings.Repeat("b", 64), nil),
	} {
		tracer.reads.Store(0)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/aithema/preview/"+design+"?cap="+cap, nil))
		if w.Code != 404 || w.Body.String() != "404 page not found\n" {
			t.Fatal("cap rejection changed")
		}
		if tracer.reads.Load() != 0 {
			t.Error("unverified preview cap read the encrypted service credential")
		}
	}
	tracer.reads.Store(0)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/aithema/preview/"+design+"?cap="+cap, nil))
	if w.Code != 200 || tracer.reads.Load() == 0 {
		t.Fatal("valid preview cap did not permit authenticated fetch")
	}
}
