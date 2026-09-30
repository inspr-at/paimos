// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtensionEnvelopeAndSnapshotCandidate(t *testing.T) {
	raw := json.RawMessage(`{ "x-demo.readiness@1": {"version":"1.0","data":{"evidence":"  e\u0301\r\n<script>exact</script>  ","score":1.00}}, "x-demo.readiness@2":{"version":"2.1","data":null} }`)
	valid := draftWrite{Kind: "brief", Extensions: raw}
	if err := validateExtensions(&valid); err != nil || !bytes.Equal(valid.Extensions, raw) {
		t.Fatalf("valid map changed: %v", err)
	}
	for _, bad := range []string{
		`null`, `[]`, `{"other":{"version":"1.0","data":{}}}`,
		`{"x-demo.readiness@1":{"version":"2.0","data":{}}}`,
		`{"x-demo.readiness@1":{"version":"01.0","data":{}}}`,
		`{"x-demo.readiness@1":{"version":"1.0"}}`,
		`{"x-demo.readiness@1":{"version":"1.0","data":{},"extra":true}}`,
		`{"x-demo.readiness@1":null}`,
	} {
		if err := validateExtensions(&draftWrite{Extensions: json.RawMessage(bad)}); err == nil {
			t.Errorf("accepted invalid envelope: %s", bad)
		}
	}
	tooMany := map[string]any{}
	for i := range 9 {
		tooMany[fmt.Sprintf("x-demo.extra%d@1", i)] = map[string]any{"version": "1.0", "data": nil}
	}
	encoded, _ := json.Marshal(tooMany)
	if err := validateExtensions(&draftWrite{Extensions: encoded}); err == nil {
		t.Fatal("accepted nine instances")
	}
	document := reviewDocument(raw)
	in := draftWrite{Kind: "brief", DocumentBytes: &document}
	if err := validateExtensions(&in); err != nil || !bytes.Equal(in.Extensions, raw) || *in.DocumentBytes != document {
		t.Fatalf("snapshot extraction changed bytes: %v", err)
	}
	for _, changed := range []string{
		strings.Replace(document, `"confirmed"`, `"proposed"`, 1),
		strings.Replace(document, `"host":null`, `"host":{}`, 1),
		strings.ReplaceAll(document, `"constraint"`, `"requirement"`),
		strings.Replace(document, `"major":1`, `"major":2`, 1),
		strings.Replace(document, `"min_reader":0`, `"min_reader":2`, 1),
		strings.Replace(document, `"items":[`, `"items":[{"kind":"constraint","state":"confirmed","host":null},`, 1),
	} {
		if err := validateExtensions(&draftWrite{Kind: "brief", DocumentBytes: &changed}); err == nil {
			t.Error("accepted invalid snapshot candidate")
		}
	}
	if err := validateExtensions(&draftWrite{Kind: "brief", DocumentBytes: &document, Extensions: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted a map that differs from the snapshot")
	}
}

func reviewDocument(raw json.RawMessage) string {
	return `{"contract":"aithema.spec.snapshot","major":1,"minor":1,"min_reader":0,"host_mode":"review","spec":{"items":[{"kind":"constraint","state":"superseded","host":{"draft_id":"historical"},"extensions":{"x-old.analysis@1":{"version":"1.0","data":"historical"}}},{"kind":"constraint","state":"confirmed","host":null,"extensions":` + string(raw) + `}]}}`
}

// ASCII string data with three instances exactly at 16 KiB; the complete
// eight-instance map is exactly 64 KiB, including every key and wrapper.
func extensionLimitMap(t *testing.T) json.RawMessage {
	t.Helper()
	instances := map[string]json.RawMessage{}
	base := len(`{"version":"1.0","data":""}`)
	for i := range 8 {
		value := `{"version":"1.0","data":true}`
		if i < 3 {
			value = `{"version":"1.0","data":"` + strings.Repeat("a", (16<<10)-base) + `"}`
		}
		instances[fmt.Sprintf("x-demo.limit%d@1", i)] = json.RawMessage(value)
	}
	first, _ := json.Marshal(instances)
	instances["x-demo.limit3@1"] = json.RawMessage(`{"version":"1.0","data":"` + strings.Repeat("b", (64<<10)-len(first)+2) + `"}`)
	raw, err := json.Marshal(instances)
	if err != nil || len(raw) != 64<<10 {
		t.Fatalf("boundary fixture: %d bytes, %v", len(raw), err)
	}
	for key, value := range instances {
		if len(value) > 16<<10 {
			t.Fatalf("oversized boundary instance %s: %d", key, len(value))
		}
	}
	if err := validateExtensions(&draftWrite{Extensions: raw}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNativeRequestHasRoomForAithemaLimits(t *testing.T) {
	raw := extensionLimitMap(t)
	document := reviewDocument(raw)
	in := draftWrite{Kind: "brief", DocumentBytes: &document}
	request, _ := json.Marshal(in)
	if len(request) <= 65536 || len(request) >= 1<<20 {
		t.Fatalf("request size %d does not exercise overhead above the old limit", len(request))
	}
	if err := validateExtensions(&in); err != nil || !bytes.Equal(in.Extensions, raw) {
		t.Fatalf("boundary map failed: %v", err)
	}
	// Escaping control characters in an otherwise bounded canonical map adds
	// another layer of escaping in document_bytes. The transport still fits.
	escaped := bytes.ReplaceAll(raw, []byte("aaaaaa"), []byte(`\u0001`))
	document = reviewDocument(escaped)
	in = draftWrite{Kind: "brief", DocumentBytes: &document}
	request, _ = json.Marshal(in)
	if len(request) >= 1<<20 {
		t.Fatalf("escaped native request exceeds transport: %d", len(request))
	}
	if err := validateExtensions(&in); err != nil {
		t.Fatal(err)
	}
	// HTTP decoding uses the same transport cap as the real draft endpoint.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(request))
	var decoded draftWrite
	if !decode(w, r, &decoded) || w.Code != http.StatusOK {
		t.Fatal("HTTP decoder rejected the boundary envelope")
	}
}
