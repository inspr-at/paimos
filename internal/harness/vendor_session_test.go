// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestVendorSessionBinding(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	const (
		lease    = "vendor-lease-000000000000000000000001"
		ref      = "private-ref-0000000000000000000001"
		vendor   = "claude-session-000000000000000001"
		otherRef = "private-ref-0000000000000000000002"
		other    = "claude-session-000000000000000002"
	)
	body := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "build-host",
		"harness_session_ref": ref, "worker_lease": lease, "management_mode": "unmanaged", "role": "worker",
		"vendor_session_ref": vendor,
	}
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	if strings.Contains(w.Body.String(), vendor) || strings.Contains(w.Body.String(), ref) || strings.Contains(w.Body.String(), lease) || strings.Contains(w.Body.String(), "vendor_ref") {
		t.Fatal("registration response exposed a session reference")
	}
	if decode(t, w)["has_vendor_session_ref"] != true {
		t.Fatal("vendor binding omitted has_vendor_session_ref")
	}
	sum := sha256.Sum256([]byte("aeon.harness.ref\x00" + vendor))
	if !bytes.Equal(vendorDigest(t, f, id), sum[:]) {
		t.Fatal("vendor digest does not match the harness ref domain")
	}
	if got := bindSession(t, f, f.agent, vendor); got != id {
		t.Fatalf("vendor lookup %s", got)
	}
	if got := bindSession(t, f, f.agent, ref); got != id {
		t.Fatalf("private ref lookup %s", got)
	}

	delete(body, "vendor_session_ref")
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id || !bytes.Equal(vendorDigest(t, f, id), sum[:]) {
		t.Fatal("omitted vendor ref cleared the stored digest")
	}
	body["vendor_session_ref"] = vendor
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != id {
		t.Fatal("same vendor ref created a generation")
	}
	body["vendor_session_ref"] = other
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 409)
	if decode(t, w)["error"] != "vendor_session_ref differs from this active generation's existing binding" {
		t.Fatal("changed vendor binding diagnostic did not name the conflict")
	}
	if strings.Contains(w.Body.String(), other) || strings.Contains(w.Body.String(), vendor) {
		t.Fatal("conflict response exposed a session reference")
	}
	if !bytes.Equal(vendorDigest(t, f, id), sum[:]) {
		t.Fatal("rejected vendor ref replaced the stored digest")
	}

	second := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "build-host",
		"harness_session_ref": otherRef, "worker_lease": "vendor-lease-000000000000000000000002",
		"management_mode": "unmanaged", "role": "worker", "vendor_session_ref": other,
	}
	w = f.call(f.person, "POST", base, second, "")
	expect(t, w, 201)
	secondID := decode(t, w)["id"].(string)
	if secondID == id || bindSession(t, f, f.agent, other) != secondID || bindSession(t, f, f.agent, vendor) != id {
		t.Fatal("vendor lookup crossed generations")
	}
	second["harness_session_ref"] = "private-ref-0000000000000000000003"
	second["worker_lease"] = "vendor-lease-000000000000000000000003"
	w = f.call(f.person, "POST", base, second, "")
	expect(t, w, 409)
	if decode(t, w)["error"] != "vendor_session_ref is already bound to an active generation for this agent" {
		t.Fatal("duplicate vendor diagnostic did not name the conflict")
	}

	if bindSession(t, f, f.person, vendor) != "" || bindSession(t, f, f.foreign, vendor) != "" {
		t.Fatal("lookup was not limited to the caller")
	}
	if status := f.call(f.agent, "POST", "/api/inbox/session-binding", map[string]string{"harness_session_ref": "short"}, "").Code; status != 400 {
		t.Fatalf("short reference status %d", status)
	}
	if status := f.call(f.agent, "POST", "/api/inbox/session-binding", map[string]string{"harness_session_ref": "unknown-session-00000000000001"}, "").Code; status != 404 {
		t.Fatalf("unknown reference status %d", status)
	}

	shared := "shared-session-ref-0000000000001"
	wrapper := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "build-host",
		"harness_session_ref": shared, "worker_lease": "vendor-lease-000000000000000000000004",
		"management_mode": "unmanaged", "role": "worker",
	}
	w = f.call(f.person, "POST", base, wrapper, "")
	expect(t, w, 201)
	wrapperID := decode(t, w)["id"].(string)
	if vendorDigest(t, f, wrapperID) != nil || bindSession(t, f, f.agent, shared) != wrapperID {
		t.Fatal("private ref was not resolved")
	}
	if _, ok := decode(t, w)["has_vendor_session_ref"]; ok {
		t.Fatal("session without a vendor digest reported one")
	}
	wrapper["vendor_session_ref"] = shared
	w = f.call(f.person, "POST", base, wrapper, "")
	expect(t, w, 201)
	if decode(t, w)["id"] != wrapperID || vendorDigest(t, f, wrapperID) != nil || bindSession(t, f, f.agent, shared) != wrapperID {
		t.Fatal("vendor ref equal to the private ref was stored separately")
	}
	crossed := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "build-host",
		"harness_session_ref": "private-ref-0000000000000000000004", "worker_lease": "vendor-lease-000000000000000000000006",
		"management_mode": "unmanaged", "role": "worker", "vendor_session_ref": shared,
	}
	w = f.call(f.person, "POST", base, crossed, "")
	expect(t, w, 201)
	if bindSession(t, f, f.agent, shared) != "" {
		t.Fatal("two generations sharing one reference resolved to a session")
	}

	expect(t, f.call(f.agent, "POST", base+"/"+id+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	if bindSession(t, f, f.agent, vendor) != "" {
		t.Fatal("stopped generation stayed resolvable")
	}
	reused := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "build-host",
		"harness_session_ref": "private-ref-0000000000000000000005", "worker_lease": "vendor-lease-000000000000000000000005",
		"management_mode": "unmanaged", "role": "worker", "vendor_session_ref": vendor,
	}
	w = f.call(f.person, "POST", base, reused, "")
	expect(t, w, 201)
	if bindSession(t, f, f.agent, vendor) != decode(t, w)["id"].(string) {
		t.Fatal("stopped vendor ref could not bind the next generation")
	}
}

func TestVendorRefFillsOnReplay(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	const vendor = "claude-session-fill-000000000001"
	body := map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "build-host",
		"harness_session_ref": "private-ref-fill-000000000000001", "worker_lease": "vendor-lease-fill-00000000000000001",
		"management_mode": "unmanaged", "role": "worker",
	}
	w := f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	if vendorDigest(t, f, id) != nil {
		t.Fatal("omitted vendor ref was stored")
	}
	if _, ok := decode(t, w)["has_vendor_session_ref"]; ok {
		t.Fatal("omitted vendor ref reported a vendor session ref")
	}
	body["vendor_session_ref"] = vendor
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	sum := sha256.Sum256([]byte("aeon.harness.ref\x00" + vendor))
	if decode(t, w)["id"] != id || decode(t, w)["has_vendor_session_ref"] != true || !bytes.Equal(vendorDigest(t, f, id), sum[:]) || strings.Contains(w.Body.String(), vendor) || strings.Contains(w.Body.String(), "vendor_ref") {
		t.Fatal("replay did not fill the vendor digest quietly")
	}
	delete(body, "vendor_session_ref")
	w = f.call(f.person, "POST", base, body, "")
	expect(t, w, 201)
	if !bytes.Equal(vendorDigest(t, f, id), sum[:]) {
		t.Fatal("later omission cleared the filled digest")
	}
}

func vendorDigest(t *testing.T, f *harnessFixture, id string) []byte {
	t.Helper()
	var raw []byte
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT vendor_ref_digest FROM harness_sessions WHERE id=$1::uuid`, id).Scan(&raw)
	})
	return raw
}

func bindSession(t *testing.T, f *harnessFixture, p tenant.Principal, ref string) string {
	t.Helper()
	w := f.call(p, "POST", "/api/inbox/session-binding", map[string]string{"harness_session_ref": ref}, "")
	if strings.Contains(w.Body.String(), ref) {
		t.Fatal("lookup exposed the reference")
	}
	if w.Code == 404 {
		return ""
	}
	expect(t, w, 200)
	return decode(t, w)["session_id"].(string)
}
