// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestExactAPIWebhookPublicNamesContainingX(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			question := append([]byte(nil), buf[:n]...)
			answer := append([]byte(nil), question...)
			answer[2] = 0x81
			answer[3] = 0x80
			if binary.BigEndian.Uint16(question[n-4:n-2]) == 1 {
				answer[7] = 1
				answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 8, 8, 8, 8)
			}
			conn.WriteTo(answer, addr)
		}
	}()
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	t.Cleanup(func() { net.DefaultResolver = old })
	for _, host := range []string{"hooks.example.org", "x.example.org", "EXAMPLE.org"} {
		if err := validateWebhookURL(t.Context(), "https://"+host+"/hook"); err != nil {
			t.Errorf("public DNS name rejected: %s: %v", host, err)
		}
	}
	for _, host := range []string{"0x7f000001", "0x7f.0.0.1", "0177.0.0.1", "127.1", "2130706433", "127.0.0.1"} {
		if err := validateWebhookURL(t.Context(), "https://"+host+"/hook"); err == nil {
			t.Errorf("ambiguous/private numeric address accepted: %s", host)
		}
	}
}

func TestExactAPIDeliverySettingsAtomicAuditAndNoop(t *testing.T) {
	w, _, _, srv := messagingWorld(t)
	put := func(body string) int {
		status, _ := do(t, srv, w.admin.ID, "PUT", "/api/settings/inbox-delivery", body, nil)
		return status
	}
	first := `{"session_deadline_seconds":120,"unbound_deadline_seconds":900,"max_attempts":1}`
	if status := put(first); status != 200 {
		t.Fatalf("settings write %d", status)
	}
	var actor string
	var before, after []byte
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT actor_principal_id::text,before,after FROM events WHERE tenant_id=$1 AND type='inbox.delivery_settings_changed'`, w.admin.TenantID).Scan(&actor, &before, &after); err != nil {
		t.Fatal(err)
	}
	if actor != w.admin.ID || mustJSON[DeliverySettings](t, before) != DefaultDeliverySettings || mustJSON[DeliverySettings](t, after) != (DeliverySettings{120, 900, 1}) {
		t.Fatal("audit lacks effective before/after or actor")
	}
	if put(first) != 200 {
		t.Fatal("no-op refused")
	}
	var count int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='inbox.delivery_settings_changed'`, w.admin.TenantID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("no-op audit count %d: %v", count, err)
	}
	// Force an event failure after the settings UPSERT: the settings must roll back.
	_, err := w.db.Admin.Exec(t.Context(), `CREATE FUNCTION reject_delivery_settings_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='inbox.delivery_settings_changed' THEN RAISE EXCEPTION 'injected event failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_delivery_settings_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_delivery_settings_event()`)
	if err != nil {
		t.Fatal(err)
	}
	if put(strings.Replace(first, `"max_attempts":1`, `"max_attempts":2`, 1)) != 500 {
		t.Fatal("failed audit reported success")
	}
	status, body := do(t, srv, w.admin.ID, "GET", "/api/settings/inbox-delivery", "", nil)
	if status != 200 || mustJSON[DeliverySettings](t, body) != (DeliverySettings{120, 900, 1}) {
		t.Fatalf("settings not rolled back: %s", body)
	}
	var obj map[string]any
	if err := json.Unmarshal(after, &obj); err != nil || len(obj) != 3 {
		t.Fatal("audit payload is not content-free settings")
	}
}
