// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPublicPortalBoundary(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	m := New(d.App, false, bytes.Repeat([]byte{11}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantA := makeTenant(t, d, "portal-a", "Portal A")
	tenantB := makeTenant(t, d, "portal-b", "Portal B")
	tenantC := makeTenant(t, d, "portal-c", "Portal C")
	tenantQuiet := makeTenant(t, d, "portal-quiet", "Quiet")
	admin := makePerson(t, d, tenantA, "Ada Admin", "admin")
	member := makePerson(t, d, tenantA, "Mina Member", "member")
	quietAdmin := makePerson(t, d, tenantQuiet, "Quinn Admin", "admin")

	productA := insertNode(t, d, tenantA, "PPR-1", "portal_product", "Harbour catalog", "Work that is ready in the morning.", "published", "", "{}")
	insertNode(t, d, tenantA, "PCF-1", "portal_feature", "Deadline radar", "Public summary of the radar", "live", productA,
		`{"live_since":"260926120000.0.0","legal_basis":"§ 16 MRG","internal_note":"SECRET-NOTE-A","ticket_key":"AEON-SECRET-A","assignee_email":"leak@example.com"}`)
	insertNode(t, d, tenantA, "PCF-2", "portal_feature", "Planned export", "A planned public summary", "planned", productA, `{"live_since":"SHOULD-NOT-SHOW-VERSION"}`)
	insertNode(t, d, tenantA, "PCF-3", "portal_feature", "Paper archive", "Declined in public", "declined", productA, `{"decline_reason":"The record is already digital.","live_since":"SHOULD-NOT-SHOW-VERSION"}`)
	insertNode(t, d, tenantA, "PCF-8", "portal_feature", "ORPHAN-FEATURE", "hidden orphan", "live", "", "{}")
	insertNode(t, d, tenantA, "PCF-9", "portal_feature", "SECRET-DRAFT-A", "hidden draft", "open", productA, "{}")
	insertNode(t, d, tenantA, "PWS-1", "portal_wish", "A public wish", "Join without an account.", "published", productA, "{}")
	insertNode(t, d, tenantA, "PWS-2", "portal_wish", "SECRET-PENDING-A", "still in review", "pending", productA, "{}")
	insertNode(t, d, tenantA, "PWS-9", "portal_wish", "ORPHAN-WISH", "no product", "published", "", "{}")
	insertNode(t, d, tenantA, "TKT-1", "work", "SECRET-TICKET-A", "SECRET-TICKET-BODY", "open", "", "{}")
	insertNode(t, d, tenantB, "PPR-1", "portal_product", "OTHER-TENANT-PRODUCT", "other summary", "published", "", "{}")
	insertNode(t, d, tenantC, "PPR-1", "portal_product", "SHOULD-STAY-HIDDEN", "closed summary", "published", "", "{}")
	setPortal(t, d, tenantB, true)
	setPortal(t, d, tenantC, false)

	const (
		readA    = "/api/public/portal/portal-a"
		readB    = "/api/public/portal/portal-b"
		readC    = "/api/public/portal/portal-c"
		missing  = "/api/public/portal/no-such-portal"
		voteA    = "/api/public/portal/portal-a/wishes/PWS-1/votes"
		readIP   = "203.0.113.10:1000"
		voteIP   = "203.0.113.20:1000"
		rateIP   = "203.0.113.30:1000"
		crossIP  = "203.0.113.40:1000"
		emailIP  = "203.0.113.41:1000"
		orphanIP = "203.0.113.42:1000"
		closedIP = "203.0.113.43:1000"
		unknown  = "203.0.113.50:1000"
	)
	secrets := []string{
		"SECRET-NOTE-A", "AEON-SECRET-A", "leak@example.com", "SECRET-DRAFT-A", "SECRET-TICKET-A",
		"SECRET-TICKET-BODY", "SECRET-PENDING-A", "ORPHAN-FEATURE", "ORPHAN-WISH", "OTHER-TENANT-PRODUCT",
		"SHOULD-STAY-HIDDEN", "SHOULD-NOT-SHOW-VERSION", "internal_note", "ticket_key", "assignee_email",
	}

	closed := f.do(http.MethodGet, readA, "", readIP, nil, nil, nil)
	unknownRes := f.do(http.MethodGet, missing, "", readIP, nil, nil, nil)
	explicitOff := f.do(http.MethodGet, readC, "", readIP, nil, nil, nil)
	if closed.Code != http.StatusNotFound {
		t.Fatalf("closed portal: %d %s", closed.Code, closed.Body)
	}
	sameResponse(t, closed, unknownRes)
	sameResponse(t, closed, explicitOff)
	for _, secret := range secrets {
		if strings.Contains(closed.Body.String(), secret) {
			t.Fatalf("closed body leaked %s", secret)
		}
	}

	if rec := f.do(http.MethodGet, "/api/portal/settings", "", readIP, nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous settings: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/api/portal/settings", "", readIP, &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member settings: %d %s", rec.Code, rec.Body)
	}
	agent := tenant.Principal{ID: admin.ID, TenantID: tenantA, Kind: tenant.Agent, Name: "Portal agent", Scopes: []string{"settings.manage"}}
	if rec := f.do(http.MethodGet, "/api/portal/settings", "", readIP, &agent, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent settings: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true}`, readIP, &agent, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent enable: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true}`, readIP, &member, nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member enable: %d", rec.Code)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true,"note":"SECRET-NOTE-A"}`, readIP, &admin, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown settings field: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, readA, "", readIP, nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatal("rejected settings write enabled the portal")
	}
	enabled := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true}`, readIP, &admin, nil, nil)
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) || !strings.Contains(enabled.Body.String(), `"slug":"portal-a"`) {
		t.Fatalf("enable: %d %s", enabled.Code, enabled.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":false}`, readIP, &admin, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, readA, "", readIP, nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatal("disabled portal still readable")
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true}`, readIP, &admin, nil, nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := f.do(http.MethodPatch, "/api/portal/settings", `{"enabled":true}`, readIP, &quietAdmin, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("quiet enable: %d %s", rec.Code, rec.Body)
	}

	body := f.do(http.MethodGet, readA, "", readIP, nil, nil, nil)
	if body.Code != http.StatusOK {
		t.Fatalf("public read: %d %s", body.Code, body.Body)
	}
	assertPublicDocument(t, body.Body.Bytes(), secrets)
	other := f.do(http.MethodGet, readB, "", readIP, nil, nil, nil)
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), "OTHER-TENANT-PRODUCT") || strings.Contains(other.Body.String(), "Harbour catalog") {
		t.Fatalf("tenant B read: %d %s", other.Code, other.Body)
	}
	quiet := f.do(http.MethodGet, "/api/public/portal/portal-quiet", "", readIP, nil, nil, nil)
	if quiet.Code != http.StatusOK {
		t.Fatalf("quiet portal: %d %s", quiet.Code, quiet.Body)
	}
	var quietDoc map[string]any
	if err := json.Unmarshal(quiet.Body.Bytes(), &quietDoc); err != nil || quietDoc["product"] != nil || len(quietDoc) != 3 {
		t.Fatalf("quiet document: %s", quiet.Body)
	}

	first := f.do(http.MethodPost, voteA, "{}", voteIP, nil, nil, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("vote: %d %s", first.Code, first.Body)
	}
	ballot := cookieFrom(t, first)
	if !ballot.HttpOnly || ballot.Secure || ballot.SameSite != http.SameSiteLaxMode {
		t.Fatalf("ballot cookie httpOnly %v secure %v sameSite %v", ballot.HttpOnly, ballot.Secure, ballot.SameSite)
	}
	if strings.Contains(first.Body.String(), ballot.Value) {
		t.Fatal("ballot returned in the vote body")
	}
	replay := f.do(http.MethodPost, voteA, "", voteIP, nil, ballot, nil)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %s want %s", replay.Code, replay.Body, first.Body)
	}
	listed := f.do(http.MethodGet, readA, "", readIP, nil, nil, nil)
	if !strings.Contains(listed.Body.String(), `"votes":1`) {
		t.Fatalf("count: %s", listed.Body)
	}
	assertVoteStorage(t, d, tenantA, tenantB, ballot.Value, voteIP)

	m.secureCookies = true
	secure := f.do(http.MethodPost, voteA, "{}", "203.0.113.21:1000", nil, nil, nil)
	m.secureCookies = false
	if secure.Code != http.StatusCreated || !cookieFrom(t, secure).Secure {
		t.Fatalf("secure ballot: %d %s", secure.Code, secure.Header().Get("Set-Cookie"))
	}

	var rateBallot *http.Cookie
	beforeRate := voteCount(t, d, tenantA)
	for i := 0; i < 10; i++ {
		rec := f.do(http.MethodPost, voteA, "{}", rateIP, nil, rateBallot, nil)
		want := http.StatusOK
		if i == 0 {
			want = http.StatusCreated
			rateBallot = cookieFrom(t, rec)
		}
		if rec.Code != want {
			t.Fatalf("rate attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	limited := f.do(http.MethodPost, voteA, "{}", rateIP, nil, rateBallot, nil)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("limit: %d retry %q body %s", limited.Code, limited.Header().Get("Retry-After"), limited.Body)
	}
	if got := voteCount(t, d, tenantA); got != beforeRate+1 {
		t.Fatalf("rate limit stored %d new votes, want 1 (before %d)", got-beforeRate, beforeRate)
	}

	cross := f.do(http.MethodPost, voteA, "{}", crossIP, nil, nil, map[string]string{"Origin": "https://evil.test"})
	if cross.Code != http.StatusForbidden || cross.Header().Get("Set-Cookie") != "" {
		t.Fatalf("cross-site: %d cookie %q", cross.Code, cross.Header().Get("Set-Cookie"))
	}
	ident := f.do(http.MethodPost, voteA, `{"email":"leak@example.com"}`, emailIP, nil, nil, nil)
	if ident.Code != http.StatusBadRequest {
		t.Fatalf("identity field: %d %s", ident.Code, ident.Body)
	}
	if rec := f.do(http.MethodPost, "/api/public/portal/portal-a/wishes/PWS-9/votes", "{}", orphanIP, nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("orphan wish: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, "/api/public/portal/portal-c/wishes/PWS-1/votes", "{}", closedIP, nil, nil, nil); rec.Code != http.StatusNotFound || rec.Body.String() != closed.Body.String() {
		t.Fatalf("disabled vote: %d %s", rec.Code, rec.Body)
	}
	if got := voteCount(t, d, tenantA); got != beforeRate+1 {
		t.Fatalf("refused votes changed the count to %d, want %d", got, beforeRate+1)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantA, func(tx pgx.Tx) error {
		var blob string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(coalesce(after::text,''), ''), '') FROM events`).Scan(&blob); err != nil {
			return err
		}
		if strings.Contains(blob, "leak@example.com") {
			t.Fatal("identity field entered the event log")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	unknownVote := "/api/public/portal/no-such-portal/wishes/PWS-1/votes"
	disabledVote := "/api/public/portal/portal-c/wishes/PWS-1/votes"
	firstUnknown := f.do(http.MethodPost, unknownVote, "{}", unknown, nil, nil, nil)
	firstDisabled := f.do(http.MethodPost, disabledVote, "{}", unknown, nil, nil, nil)
	if firstUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown vote: %d %s", firstUnknown.Code, firstUnknown.Body)
	}
	sameResponse(t, firstUnknown, firstDisabled)
	for i := 0; i < 8; i++ {
		rec := f.do(http.MethodPost, unknownVote, "{}", unknown, nil, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	blockedUnknown := f.do(http.MethodPost, unknownVote, "{}", unknown, nil, nil, nil)
	blockedDisabled := f.do(http.MethodPost, disabledVote, "{}", unknown, nil, nil, nil)
	if blockedUnknown.Code != http.StatusTooManyRequests {
		t.Fatalf("unknown limit: %d %s", blockedUnknown.Code, blockedUnknown.Body)
	}
	sameResponse(t, blockedUnknown, blockedDisabled)
	blockedOpen := f.do(http.MethodPost, voteA, "{}", unknown, nil, nil, nil)
	if blockedOpen.Code != http.StatusTooManyRequests {
		t.Fatalf("open portal escaped the shared client budget: %d %s", blockedOpen.Code, blockedOpen.Body)
	}
	sameResponse(t, blockedUnknown, blockedOpen)

	ip := "198.51.100.10"
	key := hash("portal-read:" + ip)
	for i := 0; i < 2; i++ {
		ok, _, err := m.allow(ctx, tenantA, key, 2)
		if err != nil || !ok {
			t.Fatalf("allow %d: %v %v", i, ok, err)
		}
	}
	ok, retry, err := m.allow(ctx, tenantA, key, 2)
	if err != nil || ok || retry < 1 {
		t.Fatalf("shared limiter: ok %v retry %d err %v", ok, retry, err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		var stored string
		if err := tx.QueryRow(ctx, `SELECT bucket_key FROM quote_public_rate_limits WHERE bucket_key=$1`, key).Scan(&stored); err != nil {
			return err
		}
		if stored != key || strings.Contains(stored, "198.51.100") {
			t.Fatalf("bucket stored the address: %s", stored)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sameResponse(t *testing.T, a, b *httptest.ResponseRecorder) {
	t.Helper()
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Fatalf("status/body %d %s vs %d %s", a.Code, a.Body, b.Code, b.Body)
	}
	ah, bh := a.Header().Clone(), b.Header().Clone()
	aRetry, bRetry := ah.Values("Retry-After"), bh.Values("Retry-After")
	if a.Code == http.StatusTooManyRequests {
		if !retryAfterAgrees(aRetry, bRetry) {
			t.Fatalf("header Retry-After %v vs %v", aRetry, bRetry)
		}
	} else if strings.Join(aRetry, "\n") != strings.Join(bRetry, "\n") {
		t.Fatalf("header Retry-After %v vs %v", aRetry, bRetry)
	}
	ah.Del("Retry-After")
	bh.Del("Retry-After")
	if len(ah) != len(bh) {
		t.Fatalf("headers %#v vs %#v", ah, bh)
	}
	for key, values := range ah {
		if strings.Join(values, "\n") != strings.Join(bh[key], "\n") {
			t.Fatalf("header %s %v vs %v", key, values, bh[key])
		}
	}
}

// retryAfterAgrees compares Retry-After on two 429 responses. Each side must
// be one integer from 1 through the limiter window before the values are
// compared. Equal values in that range agree. The only unequal pair that
// agrees is 59 and 60, either order: allow() ceils the remaining minute from
// clock_timestamp(), so two denials that straddle the top of that minute can
// report 60 and then 59. Callers comparing any other status keep absent
// headers and do not use this helper.
func retryAfterAgrees(a, b []string) bool {
	av, aok := oneRetryAfter(a)
	bv, bok := oneRetryAfter(b)
	if !aok || !bok {
		return false
	}
	if av == bv {
		return true
	}
	return (av == 59 && bv == 60) || (av == 60 && bv == 59)
}

func oneRetryAfter(values []string) (int, bool) {
	if len(values) != 1 {
		return 0, false
	}
	seconds, err := strconv.Atoi(values[0])
	if err != nil {
		return 0, false
	}
	limit := int(publicLimitWindow / time.Second)
	if seconds < 1 || seconds > limit {
		return 0, false
	}
	return seconds, true
}

func TestSameResponseAllowsRetryAfterSecondBoundary(t *testing.T) {
	sameResponse(t, rateLimited(t, "60"), rateLimited(t, "59"))
	sameResponse(t, rateLimited(t, "59"), rateLimited(t, "60"))
	sameResponse(t, rateLimited(t, "60"), rateLimited(t, "60"))
}

func TestSameResponseAllowsAbsentRetryAfterOffLimit(t *testing.T) {
	a := httptest.NewRecorder()
	b := httptest.NewRecorder()
	a.WriteHeader(http.StatusNotFound)
	b.WriteHeader(http.StatusNotFound)
	sameResponse(t, a, b)
}

func TestRetryAfterAgreesOnlyAtFiftyNineSixty(t *testing.T) {
	agree := [][2][]string{
		{{"59"}, {"60"}},
		{{"60"}, {"59"}},
		{{"60"}, {"60"}},
	}
	for _, pair := range agree {
		if !retryAfterAgrees(pair[0], pair[1]) {
			t.Fatalf("Retry-After %v vs %v should agree", pair[0], pair[1])
		}
	}
	reject := [][2][]string{
		{{"1"}, {"2"}},
		{{"58"}, {"59"}},
		{{"0"}, {"0"}},
		{{"61"}, {"61"}},
		{nil, nil},
		{{"60"}, {"58"}},
		{{"60"}, nil},
		{{"61"}, {"60"}},
		{{"0"}, {"1"}},
		{{"soon"}, {"60"}},
	}
	for _, pair := range reject {
		if retryAfterAgrees(pair[0], pair[1]) {
			t.Fatalf("Retry-After %v vs %v should not agree", pair[0], pair[1])
		}
	}
}

func rateLimited(t *testing.T, retryAfter string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("Retry-After", retryAfter)
	rec.WriteHeader(http.StatusTooManyRequests)
	if _, err := rec.WriteString(`{"error":"too many attempts"}`); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPublicWishIntake(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{11}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	f := &fixture{t: t, m: m, d: d, h: mux}

	tenantA := makeTenant(t, d, "wish-a", "Wish A")
	tenantB := makeTenant(t, d, "wish-b", "Wish B")
	productA := insertNode(t, d, tenantA, "PPR-1", "portal_product", "Harbour catalog", "Ready in the morning.", "published", "", "{}")
	insertNode(t, d, tenantA, "PWS-9", "portal_wish", "SECRET-PENDING-A", "still in review", "pending", productA, "{}")
	insertNode(t, d, tenantB, "PPR-1", "portal_product", "Other catalog", "Other summary.", "published", "", "{}")
	setPortal(t, d, tenantA, true)
	setPortal(t, d, tenantB, false)

	const (
		readA   = "/api/public/portal/wish-a"
		postA   = "/api/public/portal/wish-a/wishes"
		postB   = "/api/public/portal/wish-b/wishes"
		missing = "/api/public/portal/no-such-portal/wishes"
		ip      = "203.0.113.80:1000"
	)
	before := wishCount(t, d, tenantA)
	created := f.do(http.MethodPost, postA, `{"title":"A morning bell","summary":"Ring once, before the office opens.","website":""}`, ip, nil, nil, nil)
	if created.Code != http.StatusCreated || strings.TrimSpace(created.Body.String()) != `{"accepted":true}` {
		t.Fatalf("intake: %d %s", created.Code, created.Body)
	}
	if got := wishCount(t, d, tenantA); got != before+1 {
		t.Fatalf("stored wishes %d, want %d", got, before+1)
	}
	listed := f.do(http.MethodGet, readA, "", "203.0.113.81:1000", nil, nil, nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "A morning bell") || strings.Contains(listed.Body.String(), "SECRET-PENDING-A") {
		t.Fatalf("pending leaked: %d %s", listed.Code, listed.Body)
	}
	assertWishPending(t, d, tenantA, "A morning bell")

	honeypot := f.do(http.MethodPost, postA, `{"title":"HONEYPOT-WISH","summary":"Should not be stored.","website":"https://evil.test"}`, "203.0.113.82:1000", nil, nil, nil)
	if honeypot.Code != http.StatusCreated || strings.TrimSpace(honeypot.Body.String()) != strings.TrimSpace(created.Body.String()) {
		t.Fatalf("honeypot: %d %s", honeypot.Code, honeypot.Body)
	}
	if got := wishCount(t, d, tenantA); got != before+1 {
		t.Fatalf("honeypot stored a wish: %d", got)
	}
	if rec := f.do(http.MethodPost, postA, `{"title":"Named","summary":"No.","email":"leak@example.com"}`, "203.0.113.83:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("email field: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, postA, `{"title":"Named","summary":"No.","name":"Ada"}`, "203.0.113.84:1000", nil, nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("name field: %d %s", rec.Code, rec.Body)
	}
	cross := f.do(http.MethodPost, postA, `{"title":"Cross","summary":"No."}`, "203.0.113.85:1000", nil, nil, map[string]string{"Origin": "https://evil.test"})
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d %s", cross.Code, cross.Body)
	}
	if got := wishCount(t, d, tenantA); got != before+1 {
		t.Fatalf("refused intake stored wishes: %d", got)
	}

	closed := f.do(http.MethodPost, postB, `{"title":"Closed","summary":"No."}`, "203.0.113.86:1000", nil, nil, nil)
	unknown := f.do(http.MethodPost, missing, `{"title":"Missing","summary":"No."}`, "203.0.113.86:1000", nil, nil, nil)
	if closed.Code != http.StatusNotFound {
		t.Fatalf("closed intake: %d %s", closed.Code, closed.Body)
	}
	sameResponse(t, closed, unknown)

	limitIP := "203.0.113.90:1000"
	for i := 0; i < wishIntakeLimit; i++ {
		rec := f.do(http.MethodPost, missing, `{"title":"Limit","summary":"No."}`, limitIP, nil, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("limit attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	blockedMissing := f.do(http.MethodPost, missing, `{"title":"Limit","summary":"No."}`, limitIP, nil, nil, nil)
	blockedOpen := f.do(http.MethodPost, postA, `{"title":"Limit","summary":"No."}`, limitIP, nil, nil, nil)
	if blockedMissing.Code != http.StatusTooManyRequests || blockedMissing.Header().Get("Retry-After") == "" {
		t.Fatalf("intake limit: %d retry %q", blockedMissing.Code, blockedMissing.Header().Get("Retry-After"))
	}
	sameResponse(t, blockedMissing, blockedOpen)
	if got := wishCount(t, d, tenantA); got != before+1 {
		t.Fatalf("limited intake stored wishes: %d", got)
	}

	publishWish(t, d, tenantA, "A morning bell")
	published := f.do(http.MethodGet, readA, "", "203.0.113.91:1000", nil, nil, nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), "A morning bell") || strings.Contains(published.Body.String(), "SECRET-PENDING-A") || strings.Contains(published.Body.String(), "HONEYPOT-WISH") {
		t.Fatalf("published wish: %d %s", published.Code, published.Body)
	}
	rejectWish(t, d, tenantA, "A morning bell")
	rejected := f.do(http.MethodGet, readA, "", "203.0.113.92:1000", nil, nil, nil)
	if rejected.Code != http.StatusOK || strings.Contains(rejected.Body.String(), "A morning bell") {
		t.Fatalf("rejected wish still public: %s", rejected.Body)
	}
}

func wishCount(t *testing.T, d *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='portal_wish' AND n.deleted_at IS NULL`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func assertWishPending(t *testing.T, d *dbtest.DB, tenantID, title string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		var state, body string
		if err := tx.QueryRow(t.Context(), `SELECT state, body FROM nodes WHERE title=$1`, title).Scan(&state, &body); err != nil {
			return err
		}
		if state != "pending" || body == "" {
			t.Fatalf("wish state %s body %q", state, body)
		}
		var payload string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(string_agg(coalesce(after::text,''), ''), '') FROM events WHERE type='portal.wish_submitted'`).Scan(&payload); err != nil {
			return err
		}
		if !strings.Contains(payload, `"state": "pending"`) || strings.Contains(payload, "leak@example.com") || strings.Contains(payload, title) {
			t.Fatalf("wish event %s", payload)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publishWish(t *testing.T, d *dbtest.DB, tenantID, title string) {
	setWishState(t, d, tenantID, title, "published")
}

func rejectWish(t *testing.T, d *dbtest.DB, tenantID, title string) {
	setWishState(t, d, tenantID, title, "rejected")
}

func setWishState(t *testing.T, d *dbtest.DB, tenantID, title, state string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		// Fixture stands in for a moderator transaction. The catalog trigger
		// refuses this state change until that flag is set.
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		tag, err := tx.Exec(t.Context(), `UPDATE nodes SET state=$2 WHERE title=$1 AND deleted_at IS NULL`, title, state)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("wish update %d", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPortalLimitKeyIsNotAddressHash(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{11}, 32))
	mux := http.NewServeMux()
	m.Mount(mux)
	const remote = "198.51.100.77:443"
	req := httptest.NewRequest(http.MethodGet, "/api/public/portal/no-such-portal", nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	const ip = "198.51.100.77"
	want, err := m.bucketKey("portal-read", ip)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, zeroTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT bucket_key FROM quote_public_rate_limits`).Scan(&stored)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored == hash(ip) || stored == hash("portal-read:"+ip) || stored != want || strings.Contains(stored, "198.51.100") {
		t.Fatalf("limiter key is a plain digest of the address: %s", stored)
	}
}

func TestPortalLimitRowsExpireWithoutTraffic(t *testing.T) {
	d := dbtest.Open(t)
	m := New(d.App, false, bytes.Repeat([]byte{11}, 32))
	ctx := t.Context()
	oldKey := strings.Repeat("ab", 32)
	freshKey := strings.Repeat("cd", 32)
	err := db.InTenant(ctx, d.App, zeroTenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO quote_public_rate_limits(tenant_id,bucket_key,attempts,updated_at) VALUES
			($1::uuid,$2,ARRAY[clock_timestamp()-interval '3 minutes'],clock_timestamp()-interval '3 minutes'),
			($1::uuid,$3,ARRAY[clock_timestamp()],clock_timestamp())`, zeroTenant, oldKey, freshKey)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ExpireLimits(ctx); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(ctx, d.App, zeroTenant, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_rate_limits WHERE bucket_key=$1`, oldKey).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("expired limiter row remained without further traffic")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_rate_limits WHERE bucket_key=$1`, freshKey).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("fresh limiter rows %d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stop, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		m.RunLimitSweep(stop)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("limit sweep ignored cancellation")
	}
}

func assertPublicDocument(t *testing.T, body []byte, secrets []string) {
	t.Helper()
	raw := string(body)
	for _, secret := range secrets {
		if strings.Contains(raw, secret) {
			t.Fatalf("public document leaked %s", secret)
		}
	}
	var doc struct {
		Product *struct {
			Key     string `json:"key"`
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"product"`
		Catalog []portalFeature `json:"catalog"`
		Wishes  []portalWish    `json:"wishes"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Product == nil || doc.Product.Title != "Harbour catalog" || doc.Product.Summary != "Work that is ready in the morning." || doc.Product.Key != "PPR-1" {
		t.Fatalf("product %+v", doc.Product)
	}
	if len(doc.Catalog) != 3 || len(doc.Wishes) != 1 {
		t.Fatalf("catalog %d wishes %d", len(doc.Catalog), len(doc.Wishes))
	}
	if doc.Catalog[0].Title != "Deadline radar" || doc.Catalog[0].Status != "live" || doc.Catalog[0].LiveSince != "260926120000.0.0" || doc.Catalog[0].LegalBasis != "§ 16 MRG" || doc.Catalog[0].Summary != "Public summary of the radar" {
		t.Fatalf("live feature %+v", doc.Catalog[0])
	}
	if doc.Catalog[1].Status != "planned" || doc.Catalog[1].LiveSince != "" || doc.Catalog[1].Title != "Planned export" {
		t.Fatalf("planned feature %+v", doc.Catalog[1])
	}
	if doc.Catalog[2].Status != "declined" || doc.Catalog[2].DeclineReason != "The record is already digital." || doc.Catalog[2].LiveSince != "" {
		t.Fatalf("declined feature %+v", doc.Catalog[2])
	}
	if doc.Wishes[0].Key != "PWS-1" || doc.Wishes[0].Title != "A public wish" || doc.Wishes[0].Votes != 0 {
		t.Fatalf("wish %+v", doc.Wishes[0])
	}
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		t.Fatal(err)
	}
	walkPublicKeys(t, tree)
}

func walkPublicKeys(t *testing.T, v any) {
	t.Helper()
	allowed := map[string]bool{
		"product": true, "catalog": true, "wishes": true, "key": true, "title": true, "summary": true,
		"status": true, "live_since": true, "legal_basis": true, "decline_reason": true, "votes": true,
	}
	switch n := v.(type) {
	case map[string]any:
		for key, child := range n {
			if !allowed[key] {
				t.Fatalf("unexpected public field %s", key)
			}
			walkPublicKeys(t, child)
		}
	case []any:
		for _, child := range n {
			walkPublicKeys(t, child)
		}
	}
}

func assertVoteStorage(t *testing.T, d *dbtest.DB, tenantA, tenantB, ballot, remote string) {
	t.Helper()
	ctx := t.Context()
	var wishID string
	err := db.InTenant(dbtest.Seed(ctx), d.App, tenantA, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE key='PWS-1'`).Scan(&wishID); err != nil {
			return err
		}
		var cols []string
		rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name='portal_votes'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			cols = append(cols, name)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		got := map[string]bool{}
		for _, name := range cols {
			got[name] = true
		}
		for _, name := range []string{"tenant_id", "wish_id", "voter_hash", "weight", "created_at"} {
			if !got[name] {
				t.Fatalf("missing column %s in %v", name, cols)
			}
		}
		if len(got) != 5 {
			t.Fatalf("vote columns %v", cols)
		}
		var stored string
		var weight int
		if err := tx.QueryRow(ctx, `SELECT voter_hash, weight FROM portal_votes`).Scan(&stored, &weight); err != nil {
			return err
		}
		if stored != hash(ballot+":"+wishID) || stored == ballot || weight != 1 || strings.Contains(stored, remote) {
			t.Fatalf("stored ballot hash %s weight %d", stored, weight)
		}
		var eventsText string
		if err := tx.QueryRow(ctx, `SELECT coalesce(string_agg(coalesce(before::text,'') || coalesce(after::text,''), ''), '') FROM events`).Scan(&eventsText); err != nil {
			return err
		}
		for _, secret := range []string{ballot, stored, remote, "leak@example.com", "SECRET-NOTE-A", "user_agent", "remote_address"} {
			if strings.Contains(eventsText, secret) {
				t.Fatalf("event log contains %s", secret)
			}
		}
		var payload []byte
		if err := tx.QueryRow(ctx, `SELECT after FROM events WHERE type='portal.vote_cast'`).Scan(&payload); err != nil {
			return err
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			return err
		}
		if len(fields) != 2 || fields["wish_key"] != "PWS-1" || fields["weight"] != float64(1) {
			t.Fatalf("vote event %#v", fields)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT weight_check`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO portal_votes(tenant_id,wish_id,voter_hash,weight) VALUES($1::uuid,$2::uuid,$3,2)`, tenantA, wishID, strings.Repeat("ab", 32)); err == nil {
			t.Fatal("weight 2 accepted")
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT weight_check`); err != nil {
			return err
		}
		var other int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM portal_votes`).Scan(&other); err != nil {
			return err
		}
		if other != 1 {
			t.Fatalf("tenant A votes %d", other)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tenantB, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM portal_votes`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatalf("tenant B sees %d votes", n)
		}
		var title string
		err := tx.QueryRow(ctx, `SELECT title FROM nodes WHERE title='SECRET-TICKET-A'`).Scan(&title)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("tenant B sees private node: %v %s", err, title)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var open int
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM portal_votes`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("votes visible without a tenant setting: %d %v", open, err)
	}
	var settings int
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM portal_settings`).Scan(&settings); err != nil || settings != 0 {
		t.Fatalf("settings visible without a tenant setting: %d %v", settings, err)
	}
}

func voteCount(t *testing.T, d *dbtest.DB, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM portal_votes`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type fixture struct {
	t *testing.T
	m *Module
	d *dbtest.DB
	h http.Handler
}

func (f *fixture) do(method, path, body, remote string, p *tenant.Principal, ballot *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if ballot != nil {
		req.AddCookie(ballot)
	}
	if p != nil {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func cookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == ballotCookie {
			return cookie
		}
	}
	t.Fatal("missing ballot cookie")
	return nil
}

func makeTenant(t *testing.T, d *dbtest.DB, slug, name string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, zeroTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$2) RETURNING id::text`, slug, name).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func makePerson(t *testing.T, d *dbtest.DB, tenantID, name, role string) tenant.Principal {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,ARRAY[$3]::text[]) RETURNING id::text`, tenantID, name, role).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tenantID, id, role)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person, Name: name, Roles: []string{role}}
}

func setPortal(t *testing.T, d *dbtest.DB, tenantID string, enabled bool) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO portal_settings(tenant_id,enabled) VALUES($1::uuid,$2) ON CONFLICT (tenant_id) DO UPDATE SET enabled=EXCLUDED.enabled`, tenantID, enabled)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertNode(t *testing.T, d *dbtest.DB, tenantID, key, kind, title, body, state, parent, fields string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		var parentID any
		if parent != "" {
			parentID = parent
		}
		return tx.QueryRow(t.Context(), `
			INSERT INTO nodes(tenant_id,key,kind_id,title,body,state,parent_id,fields)
			SELECT $1::uuid,$2,k.id,$3,$4,$5,$6::uuid,$7::jsonb
			FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug=$8
			RETURNING id::text`, tenantID, key, title, body, state, parentID, fields, kind).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
