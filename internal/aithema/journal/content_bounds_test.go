// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

func TestPendingContentStagedTotalCeiling(t *testing.T) {
	f := newWorld(t, generous())
	q := url.Values{"upload": {strings.Repeat("a", 64)}, "ack": {"seq"}}
	body := map[string]any{"offset": 0, "total": maxStagedBytes + 1, "chunk": base64.StdEncoding.EncodeToString(make([]byte, journalChunkBytes))}
	for _, total := range []int64{maxStagedBytes + 1, 9007199254740991} {
		body["total"] = total
		_, err := f.request("journal", "records", "POST", marshal(body), q)
		errorIs(t, err, 413, "too_large")
	}
	var staged int
	if err := f.database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_uploads`).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("oversized total created staging: %d %v", staged, err)
	}
	// The exact ceiling is permitted for staging, before any completion buffer.
	body["total"] = maxStagedBytes
	f.success("journal", "records", "POST", marshal(body), q)
	_, err := f.database.Admin.Exec(t.Context(), `UPDATE aithema_journal_uploads SET total=$1`, maxStagedBytes+1)
	if err == nil {
		t.Fatal("database accepted an unbounded staged total")
	}
}

func TestPendingContentThirteenMiBOnlyHydratesThroughDurableRanges(t *testing.T) {
	f := newWorld(t, generous())
	doc := f.record("pending-content")
	doc["data"] = contentData(t, strings.Repeat("a", 13<<20))
	raw := marshal(doc)
	if len(raw) <= 13<<20 || len(raw) > maxStagedBytes {
		t.Fatal("contract case no longer fits within the staged ceiling")
	}
	q := uploadQuery(raw)
	for offset := 0; offset < len(raw); offset += journalChunkBytes {
		f.success("journal", "records", "POST", chunkBody(raw, offset), q)
	}
	var err error
	f.store, err = NewStore(f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"projection", "stored"} {
		for _, q := range []url.Values{{"ids": {"1"}, "format": {format}}, {"after": {"0"}, "format": {format}}} {
			_, err := f.request("journal", "records", "GET", nil, q)
			errorIs(t, err, 413, "too_large")
		}
	}
	lookup, err := f.request("journal", "records", "GET", nil, url.Values{"digests": {text(object(doc["data"])["sha256"])}})
	if err != nil || string(lookup.Body) != `[{"seq":1}]` {
		t.Fatalf("large record sequence lookup: %s %v", lookup.Body, err)
	}
	for _, span := range [][2]int{{0, journalChunkBytes}, {journalChunkBytes - 3, journalChunkBytes}, {2*journalChunkBytes + 7, 31}, {len(raw) - 3, journalChunkBytes}} {
		got := f.success("journal", "records", "GET", nil, url.Values{"ids": {"1"}, "offset": {fmt.Sprint(span[0])}, "length": {fmt.Sprint(span[1])}})
		part, err := base64.StdEncoding.DecodeString(text(got["chunk"]))
		if err != nil || number(got["total"]) != int64(len(raw)) || !bytes.Equal(part, raw[span[0]:min(span[0]+span[1], len(raw))]) {
			t.Fatal("durable range changed original bytes across a chunk boundary or at EOF")
		}
	}
	var count, total, largest int
	if err := f.database.Admin.QueryRow(t.Context(), `SELECT count(*),sum(octet_length(bytes)),max(octet_length(bytes)) FROM aithema_journal_record_chunks`).Scan(&count, &total, &largest); err != nil || count != (len(raw)+journalChunkBytes-1)/journalChunkBytes || total != len(raw) || largest > journalChunkBytes {
		t.Fatalf("durable chunks count=%d total=%d largest=%d: %v", count, total, largest, err)
	}
	if err := db.InTenant(t.Context(), f.database.App, newID(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_record_chunks`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("durable content chunks escaped tenant RLS")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Missing chunks fail closed rather than falling back to the large bytea.
	if _, err := f.database.Admin.Exec(t.Context(), `DELETE FROM aithema_journal_record_chunks WHERE chunk_offset=0`); err != nil {
		t.Fatal(err)
	}
	_, err = f.request("journal", "records", "GET", nil, url.Values{"ids": {"1"}, "offset": {"0"}})
	errorIs(t, err, 503, "unavailable")
	if _, err := f.database.Admin.Exec(t.Context(), `DELETE FROM aithema_sessions`); err != nil {
		t.Fatal(err)
	}
	if err := f.database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_record_chunks`).Scan(&count); err != nil || count != 0 {
		t.Fatal("durable chunks did not cascade with their session")
	}
}

func TestPendingContentInlineHTTPResponseCeiling(t *testing.T) {
	f := newWorld(t, generous())
	for i := range 8 {
		doc := f.record("pending-content")
		doc["data"] = contentData(t, fmt.Sprint(i)+strings.Repeat("a", 600000))
		f.success("journal", "records", "POST", marshal(doc), url.Values{"ack": {"seq"}})
	}
	jwt, err := f.keys.MintDelegated(t.Context(), f.claims)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	(&Module{Store: f.store, Keys: f.keys}).Mount(mux)
	for _, format := range []string{"projection", "stored"} {
		for _, selection := range []string{"after=0", "ids=1,2,3,4,5,6,7,8"} {
			req := httptest.NewRequest("GET", "/api/aithema/journal/sessions/"+f.claims.SessionID+"/records?format="+format+"&"+selection, nil)
			req.Header.Set("Authorization", "Bearer "+jwt)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			var body map[string]any
			if rec.Code != 413 || rec.Body.Len() > maxResponseBytes || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["code"] != "too_large" {
				t.Fatalf("oversized inline HTTP response: status=%d size=%d", rec.Code, rec.Body.Len())
			}
		}
		result, err := f.request("journal", "records", "GET", nil, url.Values{"format": {format}, "ids": {"1,2,3"}})
		var records []json.RawMessage
		if err != nil || len(result.Body) > maxResponseBytes || json.Unmarshal(result.Body, &records) != nil || len(records) != 3 {
			t.Fatalf("bounded inline response was rejected: %v", err)
		}
	}
}
