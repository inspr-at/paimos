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
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

func contentData(t *testing.T, document string) map[string]any {
	t.Helper()
	canonical, err := tokens.CanonicalJSON(marshal(map[string]any{"document_bytes": document, "metadata": map[string]any{"title": "Quoted \"draft\" 😀", "source_kind": "text"}}))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"canonical": string(canonical), "sha256": digest(canonical), "size": len(canonical)}
}

func contentOp(t *testing.T, sid string, seq int64, data map[string]any) map[string]any {
	t.Helper()
	payload, err := tokens.CanonicalJSON(marshal(map[string]any{"kind": "pending_op.content", "record_seq": seq, "sha256": data["sha256"], "size": data["size"]}))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"op_key": sid + ":source:1", "op": "post_source", "payload_kind": "pending_op.content", "payload": string(payload), "payload_sha256": digest(payload)}
}

func TestPendingContentContractPinsAndValidation(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("contracts/index.json")
	if err != nil {
		t.Fatal(err)
	}
	index, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"aithema.journal.record", "aithema.spec.snapshot"} {
		found := false
		for _, value := range array(index["contracts"]) {
			entry := object(value)
			if entry["contract"] == contract {
				found = number(entry["minor"]) == 2
			}
		}
		if !found {
			t.Fatalf("missing minor 2 pin for %s", contract)
		}
	}
	mutations := map[string]func(map[string]any){
		"old-reader": func(doc map[string]any) { doc["min_reader"] = 1 },
		"old-minor":  func(doc map[string]any) { doc["minor"], doc["min_reader"] = 1, 1 },
		"digest":     func(doc map[string]any) { object(doc["data"])["sha256"] = strings.Repeat("f", 64) },
		"size":       func(doc map[string]any) { object(doc["data"])["size"] = 1 },
		"noncanonical": func(doc map[string]any) {
			data := object(doc["data"])
			canonical := " " + text(data["canonical"])
			data["canonical"], data["sha256"], data["size"] = canonical, digest([]byte(canonical)), len(canonical)
		},
		"envelope": func(doc map[string]any) {
			canonical := `{"document_bytes":"{}","extra":true,"metadata":{}}`
			doc["data"] = map[string]any{"canonical": canonical, "sha256": digest([]byte(canonical)), "size": len(canonical)}
		},
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			doc := fixture(t, "valid/record.pending-content.json")
			change(doc)
			_, err := v.Validate(marshal(doc), "aithema.journal.record")
			errorIs(t, err, 400, "invalid_request")
		})
	}
	doc := fixture(t, "valid/record.pending-content.json")
	doc["data"] = contentData(t, strings.Repeat("\"😀<\n", 180000))
	if _, err := v.Validate(marshal(doc), "aithema.journal.record"); err != nil {
		t.Fatalf("large Unicode canonical content rejected: %v", err)
	}
	if _, err := decode(marshal(doc)); err == nil {
		t.Fatal("transport decoder lost its 1 MiB limit")
	}
	doc["min_reader"], doc["minor"] = 3, 3
	_, err = v.Validate(marshal(doc), "aithema.journal.record")
	errorIs(t, err, 422, "contract_too_new")
}

func TestPendingContentReferenceBoundsAndLegacySnapshots(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := fixture(t, "valid/snapshot.persisted.json")
	if _, err := v.Validate(marshal(doc), "aithema.spec.snapshot"); err != nil {
		t.Fatalf("legacy inline operation rejected: %v", err)
	}
	data := map[string]any{"sha256": strings.Repeat("a", 64), "size": tokens.MaxSafeInteger}
	op := contentOp(t, text(doc["sid"]), tokens.MaxSafeInteger, data)
	if len(text(op["payload"])) != 159 {
		t.Fatal("reference no longer fits the contract-owned 159-byte budget")
	}
	doc["pending_ops"], doc["minor"], doc["min_reader"] = []any{op}, 2, 2
	if _, err := v.Validate(marshal(doc), "aithema.spec.snapshot"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"old-reader", "old-minor", "noncanonical", "extra-field", "unsafe-number", "wrong-kind", "overlong"} {
		t.Run(change, func(t *testing.T) {
			candidate, _ := decodeDocument(marshal(doc))
			op := object(array(candidate["pending_ops"])[0])
			payload := text(op["payload"])
			switch change {
			case "old-reader":
				candidate["min_reader"] = 1
			case "old-minor":
				candidate["minor"], candidate["min_reader"] = 1, 1
			case "noncanonical":
				payload = strings.Replace(payload, ":", ": ", 1)
			case "extra-field":
				payload = strings.Replace(payload, "{", `{"extra":true,`, 1)
			case "unsafe-number":
				payload = strings.Replace(payload, "9007199254740991", "9007199254740992", 1)
			case "wrong-kind":
				payload = strings.Replace(payload, "pending_op.content", "source", 1)
			case "overlong":
				payload += " "
			}
			op["payload"], op["payload_sha256"] = payload, digest([]byte(payload))
			_, err := v.Validate(marshal(candidate), "aithema.spec.snapshot")
			errorIs(t, err, 400, "invalid_request")
		})
	}
}

func TestPendingContentDedupePreservesEveryEventConflict(t *testing.T) {
	f := newWorld(t, generous())
	doc := f.record("pending-content")
	doc["data"] = contentData(t, ` {"original":"😀"} `)
	raw := append([]byte(" \n"), marshal(doc)...)
	first := f.success("journal", "records", "POST", raw, url.Values{"format": {"stored"}})
	if number(object(first["document"])["seq"]) != 1 || first["bytes"] != string(raw) {
		t.Fatal("content acknowledgement lost bytes or sequence")
	}
	duplicate, _ := decodeDocument(raw)
	duplicate["client_event_id"] = newID()
	dupBytes := marshal(duplicate)
	for range 2 {
		got := f.success("journal", "records", "POST", dupBytes, url.Values{"format": {"stored"}})
		if !bytes.Equal(marshal(first), marshal(got)) {
			t.Fatal("dedupe/retry did not return the first immutable record")
		}
	}
	for _, original := range [][]byte{raw, dupBytes} {
		_, err := f.request("journal", "records", "POST", append(bytes.Clone(original), ' '), nil)
		errorIs(t, err, 409, "idempotency_conflict")
	}
	// A deduplicated ID cannot be reused for another event kind either.
	turn := f.record("turn")
	turn["client_event_id"] = duplicate["client_event_id"]
	_, err := f.request("journal", "records", "POST", marshal(turn), nil)
	errorIs(t, err, 409, "idempotency_conflict")
	const writers = 6
	inputs := make([][]byte, writers)
	for i := range inputs {
		copy, _ := decodeDocument(raw)
		copy["client_event_id"] = newID()
		inputs[i] = marshal(copy)
	}
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Go(func() { _, errs[i] = f.request("journal", "records", "POST", inputs[i], url.Values{"ack": {"seq"}}) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if number(f.success("journal", "cursor", "GET", nil, nil)["seq"]) != 1 {
		t.Fatal("deduplicated content consumed journal sequences")
	}
	// Lookup, original-byte hydration and alias conflicts survive a store restart.
	f.store, err = NewStore(f.database.App)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.request("journal", "records", "POST", append(dupBytes, ' '), nil)
	errorIs(t, err, 409, "idempotency_conflict")
	result, err := f.request("journal", "records", "GET", nil, url.Values{"digests": {text(object(doc["data"])["sha256"]) + "," + strings.Repeat("f", 64)}})
	if err != nil || string(result.Body) != `[{"seq":1}]` {
		t.Fatalf("content lookup did not survive restart: %s %v", result.Body, err)
	}
}

func TestPendingContentSnapshotClosureAndScope(t *testing.T) {
	f := newWorld(t, generous())
	doc := f.record("pending-content")
	data := contentData(t, "exact \"draft\" 😀\n")
	doc["data"] = data
	original := marshal(doc)
	f.success("journal", "records", "POST", original, nil)
	snapshot := f.snapshot(0, 1)
	snapshot["minor"], snapshot["min_reader"] = 2, 2
	snapshot["pending_ops"] = []any{contentOp(t, f.claims.SessionID, 1, data)}
	for _, change := range []string{"missing", "digest", "size"} {
		candidate, _ := decodeDocument(marshal(snapshot))
		badData, _ := decode(marshal(data))
		seq := int64(1)
		switch change {
		case "missing":
			seq = 2
		case "digest":
			badData["sha256"] = strings.Repeat("e", 64)
		case "size":
			badData["size"] = number(badData["size"]) + 1
		}
		candidate["pending_ops"] = []any{contentOp(t, f.claims.SessionID, seq, badData)}
		_, err := f.request("journal", "snapshots", "POST", marshal(candidate), nil)
		errorIs(t, err, 422, "citation_invalid")
	}
	ack := f.success("journal", "snapshots", "POST", marshal(snapshot), url.Values{"ack": {"seq"}})
	if number(ack["seq"]) != 2 {
		t.Fatal("invalid references allocated a sequence or committed their event id")
	}
	cursor := f.success("journal", "cursor", "GET", nil, url.Values{"snapshot": {"seq"}})
	if number(object(cursor["snapshot"])["seq"]) != 2 || number(cursor["working_rev"]) != 1 {
		t.Fatal("sequence-only cursor lost the snapshot")
	}
	result, err := f.request("journal", "records", "GET", nil, url.Values{"ids": {"1,2"}, "format": {"stored"}})
	var closure []Record
	if err != nil || json.Unmarshal(result.Body, &closure) != nil || len(closure) != 2 || closure[0].Bytes != string(original) || closure[1].Bytes != string(marshal(snapshot)) {
		t.Fatal("dependency hydration changed content or reference bytes")
	}
	// The same digest belongs to this session only, even for the same tenant.
	authz, _ := decode(f.config.Authorization)
	authz["sid"] = newID()
	config := f.config
	config.Authorization = marshal(authz)
	if err := f.store.CreateSession(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	other := f.claims
	other.SessionID = text(authz["sid"])
	result, err = f.store.Request(t.Context(), other, "journal", "records", "GET", nil, url.Values{"digests": {text(data["sha256"])}})
	if err != nil || string(result.Body) != "[]" {
		t.Fatal("content digest escaped its session")
	}
	foreign, _ := decodeDocument(marshal(snapshot))
	foreign["sid"], foreign["consumed_seq"] = other.SessionID, 0
	foreign["pending_ops"] = []any{contentOp(t, other.SessionID, 1, data)}
	_, err = f.store.Request(t.Context(), other, "journal", "snapshots", "POST", marshal(foreign), nil)
	errorIs(t, err, 422, "citation_invalid")
}

func chunkBody(raw []byte, offset int) []byte {
	end := min(offset+journalChunkBytes, len(raw))
	return marshal(map[string]any{"offset": offset, "total": len(raw), "chunk": base64.StdEncoding.EncodeToString(raw[offset:end])})
}

func uploadQuery(raw []byte) url.Values {
	return url.Values{"upload": {digest(raw)}, "ack": {"seq"}}
}

func TestPendingContentChunkedHTTPRestartAndHydration(t *testing.T) {
	f := newWorld(t, generous())
	doc := f.record("pending-content")
	doc["data"] = contentData(t, strings.Repeat("\"😀<\n", 180000))
	raw := append([]byte(" \n"), marshal(doc)...)
	jwt, err := f.keys.MintDelegated(t.Context(), f.claims)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, action string, q url.Values, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		mux := http.NewServeMux()
		(&Module{Store: f.store, Keys: f.keys}).Mount(mux)
		req := httptest.NewRequest(method, "/api/aithema/journal/sessions/"+f.claims.SessionID+"/"+action+"?"+q.Encode(), bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+jwt)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("POST", "records", nil, raw); rec.Code != 413 {
		t.Fatal("large direct HTTP request bypassed the 1 MiB cap")
	}
	q := uploadQuery(raw)
	for offset := 0; offset < len(raw); offset += journalChunkBytes {
		body := chunkBody(raw, offset)
		if len(body) > 1<<20 {
			t.Fatal("upload request exceeds 1 MiB")
		}
		rec := call("POST", "records", q, body)
		if rec.Code != 200 || rec.Body.Len() > 1<<20 {
			t.Fatalf("chunk transfer failed: %d %s", rec.Code, rec.Body.String())
		}
		ack, err := decode(rec.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if offset+journalChunkBytes < len(raw) {
			if number(ack["offset"]) != int64(offset+journalChunkBytes) || number(ack["total"]) != int64(len(raw)) || ack["seq"] != nil {
				t.Fatal("intermediate chunk was not acknowledged as staging")
			}
			if number(f.success("journal", "cursor", "GET", nil, nil)["seq"]) != 0 {
				t.Fatal("unfinished upload became visible to journal readers")
			}
			f.store, err = NewStore(f.database.App)
			if err != nil {
				t.Fatal(err)
			}
		} else if number(ack["seq"]) != 1 || len(ack) != 1 {
			t.Fatal("complete upload did not commit exactly one sequence")
		}
	}
	var hydrated []byte
	for offset := 0; offset < len(raw); offset += journalChunkBytes {
		q := url.Values{"ids": {"1"}, "offset": {fmt.Sprint(offset)}, "length": {fmt.Sprint(journalChunkBytes)}}
		rec := call("GET", "records", q, nil)
		if rec.Code != 200 || rec.Body.Len() > 1<<20 {
			t.Fatalf("bounded hydration failed: %d", rec.Code)
		}
		doc, err := decode(rec.Body.Bytes())
		if err != nil || number(doc["seq"]) != 1 || number(doc["offset"]) != int64(offset) || number(doc["total"]) != int64(len(raw)) {
			t.Fatal("read chunk changed its range or record identity")
		}
		part, err := base64.StdEncoding.Strict().DecodeString(text(doc["chunk"]))
		if err != nil {
			t.Fatal(err)
		}
		hydrated = append(hydrated, part...)
	}
	if !bytes.Equal(hydrated, raw) {
		t.Fatal("chunk reads changed exact UTF-8 submission bytes")
	}
	var remaining int
	if err := f.database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_upload_chunks`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("committed upload retained staging chunks")
	}
}

func TestPendingContentChunkValidationFencingAndRLS(t *testing.T) {
	f := newWorld(t, generous())
	doc := f.record("pending-content")
	doc["data"] = contentData(t, strings.Repeat("a", 1<<20))
	raw := marshal(doc)
	q := uploadQuery(raw)
	_, err := f.request("journal", "records", "POST", chunkBody(raw, journalChunkBytes), q)
	errorIs(t, err, 400, "invalid_request")
	first, _ := decode(chunkBody(raw, 0))
	for _, change := range []string{"empty", "oversize", "noncanonical", "total", "extra"} {
		bad, _ := decode(marshal(first))
		switch change {
		case "empty":
			bad["chunk"] = ""
		case "oversize":
			bad["chunk"] = base64.StdEncoding.EncodeToString(make([]byte, journalChunkBytes+1))
		case "noncanonical":
			bad["chunk"] = text(bad["chunk"]) + "\n"
		case "total":
			bad["total"] = json.Number("9007199254740992")
		case "extra":
			bad["extra"] = true
		}
		_, err := f.request("journal", "records", "POST", marshal(bad), q)
		errorIs(t, err, 400, "invalid_request")
	}
	f.success("journal", "records", "POST", chunkBody(raw, 0), q)
	// Restart at offset zero is explicit and does not append an event.
	f.success("journal", "records", "POST", chunkBody(raw, 0), q)
	badNext, _ := decode(chunkBody(raw, journalChunkBytes))
	badNext["total"] = len(raw) + 1
	_, err = f.request("journal", "records", "POST", marshal(badNext), q)
	errorIs(t, err, 400, "invalid_request")
	if err := db.InTenant(t.Context(), f.database.App, newID(), func(tx pgx.Tx) error {
		for _, table := range []string{"aithema_journal_uploads", "aithema_journal_upload_chunks", "aithema_journal_content_events"} {
			var count int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("staging or content events escaped forced tenant RLS")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := f.store.Takeover(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, f.claims.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.request("journal", "records", "POST", chunkBody(raw, journalChunkBytes), q)
	errorIs(t, err, 409, "fenced_generation")
	f.claims.Generation = state.Generation
	_, err = f.request("journal", "records", "POST", chunkBody(raw, journalChunkBytes), q)
	errorIs(t, err, 400, "invalid_request")
	// Complete tampering is refused before allocating a sequence/event ID.
	first["chunk"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'a'}, journalChunkBytes))
	f.success("journal", "records", "POST", marshal(first), q)
	for offset := journalChunkBytes; offset < len(raw); offset += journalChunkBytes {
		_, err = f.request("journal", "records", "POST", chunkBody(raw, offset), q)
		if offset+journalChunkBytes < len(raw) && err != nil {
			t.Fatal(err)
		}
	}
	errorIs(t, err, 409, "idempotency_conflict")
	if number(f.success("journal", "cursor", "GET", nil, nil)["seq"]) != 0 {
		t.Fatal("tampered upload became a journal record")
	}
}

func TestPendingContentUploadCapsPurgeAndReadRanges(t *testing.T) {
	f := newWorld(t, generous())
	content := f.record("pending-content")
	raw := marshal(content)
	f.success("journal", "records", "POST", raw, url.Values{"ack": {"seq"}})
	content["client_event_id"] = newID()
	f.success("journal", "records", "POST", marshal(content), nil)
	for _, q := range []url.Values{
		{"ids": {"1"}, "offset": {"0"}, "length": {"262145"}},
		{"ids": {"1"}, "offset": {"0"}, "length": {"0"}},
		{"ids": {"1,2"}, "offset": {"0"}},
		{"ids": {"1"}, "offset": {""}},
		{"ids": {"1"}, "offset": {"-1"}},
		{"ids": {"1"}, "offset": {fmt.Sprint(len(raw))}},
		{"ids": {"1"}, "length": {"1"}},
		{"ids": {"1"}, "offset": {"0"}, "after": {"0"}},
		{"digests": {"bad"}},
		{"digests": {text(object(content["data"])["sha256"])}, "ids": {"1"}},
		{"snapshot": {"bad"}},
	} {
		action := "records"
		if q.Has("snapshot") {
			action = "cursor"
		}
		_, err := f.request("journal", action, "GET", nil, q)
		errorIs(t, err, 400, "invalid_request")
	}
	_, err := f.request("journal", "records", "GET", nil, url.Values{"ids": {"2"}, "offset": {"0"}})
	errorIs(t, err, 404, "not_found")
	part := f.success("journal", "records", "GET", nil, url.Values{"ids": {"1"}, "offset": {"1"}, "length": {"3"}})
	if text(part["chunk"]) != base64.StdEncoding.EncodeToString(raw[1:4]) {
		t.Fatal("small range did not retain exact original bytes")
	}
	content["data"] = contentData(t, strings.Repeat("a", 1<<20))
	large := marshal(content)
	first := chunkBody(large, 0)
	for i := range maxUnfinishedUploads {
		q := url.Values{"upload": {fmt.Sprintf("%064x", i+1)}, "ack": {"seq"}}
		f.success("journal", "records", "POST", first, q)
	}
	q := uploadQuery(large)
	_, err = f.request("journal", "records", "POST", first, q)
	errorIs(t, err, 413, "too_large")
	f.success("journal", "records", "POST", first, url.Values{"upload": {fmt.Sprintf("%064x", 1)}, "ack": {"seq"}})
	if err := db.InTenant(t.Context(), f.database.App, newID(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_content_events`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("deduplicated event IDs escaped tenant RLS")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Revoke(t.Context(), f.claims.TenantID, f.claims.ProjectID, f.claims.SessionID, "purge"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := f.database.Admin.QueryRow(t.Context(), `SELECT count(*) FROM aithema_journal_uploads`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("purge retained unfinished uploads")
	}
	_, err = f.request("journal", "records", "POST", first, q)
	errorIs(t, err, 409, "revoked")
}

func TestPendingContentLargeSnapshotsAndOrdinaryEventBound(t *testing.T) {
	f := newWorld(t, generous())
	large := f.snapshot(0, 0)
	large["pending_ops"] = []any{}
	seed := object(array(object(large["spec"])["items"])[0])
	items := []any{}
	for i := range 55 {
		item, _ := decode(marshal(seed))
		item["item_ref"] = fmt.Sprintf("REQ-%d", i+1)
		content := object(item["content"])
		content["statement"] = strings.Repeat("a", 4000)
		criteria := []any{}
		for range 20 {
			criteria = append(criteria, strings.Repeat("a", 1000))
		}
		content["acceptance_criteria"] = criteria
		canonical, err := tokens.CanonicalJSON(marshal(content))
		if err != nil {
			t.Fatal(err)
		}
		item["content_sha256"] = digest(canonical)
		items = append(items, item)
	}
	object(large["spec"])["items"] = items
	raw := marshal(large)
	if len(raw) <= 1<<20 {
		t.Fatal("large snapshot test does not cross the transport bound")
	}
	for offset := 0; offset < len(raw); offset += journalChunkBytes {
		f.success("journal", "snapshots", "POST", chunkBody(raw, offset), uploadQuery(raw))
	}
	cursor := f.success("journal", "cursor", "GET", nil, url.Values{"snapshot": {"seq"}})
	if number(cursor["seq"]) != 1 || number(object(cursor["snapshot"])["seq"]) != 1 {
		t.Fatal("large snapshot was not committed with its revision")
	}
	// Chunking does not remove the existing ordinary-event size restriction.
	ordinary := f.record("turn")
	object(ordinary["data"])["text"] = strings.Repeat("a", 1<<20)
	raw = marshal(ordinary)
	var err error
	for offset := 0; offset < len(raw); offset += journalChunkBytes {
		_, err = f.request("journal", "records", "POST", chunkBody(raw, offset), uploadQuery(raw))
		if offset+journalChunkBytes < len(raw) && err != nil {
			t.Fatal(err)
		}
	}
	errorIs(t, err, 413, "too_large")
	if number(f.success("journal", "cursor", "GET", nil, url.Values{"snapshot": {"seq"}})["seq"]) != 1 {
		t.Fatal("oversized ordinary event advanced the cursor")
	}
}
