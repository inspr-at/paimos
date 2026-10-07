// SPDX-License-Identifier: AGPL-3.0-only
package accountprivacy

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/capacity"
	"strings"
	"testing"
)

func TestAvailabilityRedactionPreservesLegacyRequiredShapes(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"id":"` + id + `","registered_by_principal_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","quota_fingerprint":"private","last_probe_at":"2026-10-02T12:00:00Z","last_probe_ok":true,"plan":"private plan","openrouter_credits":{"remaining":17},"windows":[{"used":41,"ends_at":"2026-10-03T12:00:00Z"}]}`)
	body, err := Redact(raw, Policy{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{`"id":"` + id + `"`, `"windows":[]`, `"quota_fingerprint":""`, `"last_probe_at":null`, `"details_redacted":true`} {
		if !strings.Contains(string(body), shape) {
			t.Fatalf("missing %s: %s", shape, body)
		}
	}
	if strings.Contains(string(body), "private") || strings.Contains(string(body), "remaining") {
		t.Fatalf("quota leak: %s", body)
	}
	own, err := Redact(raw, Policy{id: true}, "", false)
	if err != nil || !strings.Contains(string(own), `"remaining":17`) {
		t.Fatalf("owner lost detail: %s %v", own, err)
	}
}

func TestReadinessRedactionDoesNotPretendMeasuredZero(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"account_id":"` + id + `","state":"ready","can_try":true,"reason_codes":["private reason"],"display_reason":"private reason","checked_at":"2026-10-02T12:00:00Z","next_attempt_at":"2026-10-03T12:00:00Z","measured_usage":[{"used_percent":41}],"details_redacted":false}`)
	body, err := Redact(raw, Policy{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{`"measured_usage":null`, `"checked_at":null`, `"next_attempt_at":null`, `"reason_codes":["available"]`, `"state":"unknown"`} {
		if !strings.Contains(string(body), shape) {
			t.Fatalf("missing %s: %s", shape, body)
		}
	}
	history, err := Redact([]byte(`[{"used_percent":41}]`), Policy{}, id, true)
	if err != nil || string(history) != "[]" {
		t.Fatalf("history leak: %s %v", history, err)
	}
}

func TestEventSnapshotFailsClosedForUnidentifiedAndFutureDetails(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"account_id":"` + id + `","state":"available","unrecognized_future_quota_field":{"private":"private detail"},"allowance":92,"vendor_diagnostics":"private diagnostic"}`)
	body, err := EventSnapshot(raw, Policy{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"account_id":"`+id+`","state":"available"}` {
		t.Fatalf("event availability-only policy: %s", body)
	}
	body, err = EventSnapshot([]byte(`{"quota_fingerprint":"private","reading_support":"none"}`), Policy{id: true}, "")
	if err != nil || string(body) != "{}" {
		t.Fatalf("unidentified historical event: %s %v", body, err)
	}
}

func TestCatalogAndStrictCapacityScheduleRemainReadable(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"hosts":[{"daemon_id":"local","harnesses":[{"accounts":[{"id":"` + id + `","registered_by_principal_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","available":true,"remaining_fraction":0.41,"windows":[]}]}]}]}`)
	body, err := Redact(raw, Policy{}, "", false)
	if err != nil || !strings.Contains(string(body), `"hosts":[`) || !strings.Contains(string(body), `"available":true`) || !strings.Contains(string(body), `"remaining_fraction":null`) {
		t.Fatalf("catalog availability shape: %s %v", body, err)
	}
	raw = []byte(`{"account_id":"` + id + `","cost_limit_supported":true,"schedule":{"timezone":"Europe/Vienna","week":[]},"windows":[]}`)
	body, err = Redact(raw, Policy{}, "", false)
	var response struct {
		Schedule        capacity.Schedule
		DetailsRedacted bool `json:"details_redacted"`
	}
	if err != nil || json.Unmarshal(body, &response) != nil || response.Schedule.Validate() != nil || !response.DetailsRedacted {
		t.Fatalf("strict schedule decoder: %s %v", body, err)
	}
}

func TestFix2MixedShapesAndLegacyEnums(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"groups":[{"id":"group","schedule":{"timezone":"Europe/Vienna","week":[]}}],"pins":[{"id":"pin","code":"schedule"}],"accounts":[{"account_id":"` + id + `","unavailable_reasons":["probe"],"code":"vendor","windows":[{"used":42}]}]}`)
	body, err := Redact(raw, Policy{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	group := got["groups"].([]any)[0].(map[string]any)
	pin := got["pins"].([]any)[0].(map[string]any)
	account := got["accounts"].([]any)[0].(map[string]any)
	if group["details_redacted"] != nil || group["schedule"].(map[string]any)["timezone"] != "Europe/Vienna" || pin["code"] != "schedule" {
		t.Fatalf("non-account records were masked: %s", body)
	}
	if account["code"] != "state" || account["unavailable_reasons"].([]any)[0] != "state" || len(account["windows"].([]any)) != 0 {
		t.Fatalf("legacy enum or quota boundary violated: %s", body)
	}
}

// Risk: a later account route reintroduces quota headroom through routable
// after the owner-sharing boundary has already decided the row is private.
func TestRedactionForcesRoutableFalse(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := []byte(`{"account_id":"` + id + `","routable":true,"state":"available"}`)
	body, err := Redact(raw, Policy{}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"routable":false`) || strings.Contains(string(body), `"routable":true`) {
		t.Fatalf("redacted routable still discloses headroom: %s", body)
	}
	own, err := Redact(raw, Policy{id: true}, "", false)
	if err != nil || !strings.Contains(string(own), `"routable":true`) {
		t.Fatalf("shared account lost routable: %s %v", own, err)
	}
}
