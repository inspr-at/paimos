// SPDX-License-Identifier: AGPL-3.0-only

package agentruns_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentruns"
)

func TestDaemonWaitReasonIsScopedMemory(t *testing.T) {
	f := setup(t)
	agentruns.NewDaemonWait(f.d.App).Mount(f.mux)
	v := f.run(t, f.order(t, nil))
	path := "/api/runs/" + v.ID + "/daemon-wait"
	held := map[string]string{"reason": "checkout_held_by_earlier_pairing"}
	var stored struct {
		Reason *string `json:"reason"`
	}
	f.call(t, f.person, "PUT", path, held, 403, nil)
	f.call(t, f.agent, "PUT", path, map[string]string{"reason": "vendor"}, 400, nil)
	f.call(t, f.agent, "PUT", path, map[string]string{"reason": "checkout_held_by_earlier_pairing", "extra": "no"}, 400, nil)
	f.call(t, f.agent, "PUT", path, held, 200, &stored)
	if stored.Reason == nil || *stored.Reason != "checkout_held_by_earlier_pairing" {
		t.Fatalf("stored %#v", stored.Reason)
	}
	f.call(t, f.agent, "PUT", path, held, 200, &stored)
	f.call(t, f.person, "GET", path, nil, 200, &stored)
	if stored.Reason == nil || *stored.Reason != "checkout_held_by_earlier_pairing" {
		t.Fatalf("read %#v", stored.Reason)
	}
	other := f.key(t, f.other, []string{"run.read", "run.claim"})
	if w := f.request(f.other, "GET", path, "", other); w.Code != 404 {
		t.Fatalf("other agent read %d %s", w.Code, w.Body.String())
	}
	if w := f.request(f.other, "PUT", path, `{"reason":"checkout_held_by_earlier_pairing"}`, other); w.Code != 404 {
		t.Fatalf("other agent write %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Items []struct {
			RunID  string  `json:"run_id"`
			Reason *string `json:"reason"`
		} `json:"items"`
	}
	f.call(t, f.person, "GET", "/api/runs/daemon-waits?ids="+v.ID+","+v.ID, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].RunID != v.ID || page.Items[0].Reason == nil {
		t.Fatalf("batch %#v", page.Items)
	}
	if w := f.request(f.agent, "GET", "/api/runs/daemon-waits?ids="+strings.Repeat(v.ID+",", 50)+v.ID, "", f.token); w.Code != 400 {
		t.Fatalf("overlong batch %d", w.Code)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running' WHERE id=$1`, v.ID)
		return err
	})
	f.call(t, f.agent, "PUT", path, held, 409, nil)
	f.call(t, f.person, "GET", path, nil, 200, &stored)
	if stored.Reason == nil || *stored.Reason != "checkout_held_by_earlier_pairing" {
		t.Fatal("conflict cleared the wait")
	}
	f.call(t, f.agent, "PUT", path, map[string]string{"reason": ""}, 200, &stored)
	if stored.Reason != nil {
		t.Fatalf("clear stored %#v", stored.Reason)
	}
	f.call(t, f.person, "GET", path, nil, 200, &stored)
	if stored.Reason != nil {
		t.Fatalf("clear read %#v", stored.Reason)
	}
	if w := f.request(f.person, "GET", "/api/runs/"+uuid()+"/daemon-wait", "", ""); w.Code != 404 {
		t.Fatalf("missing run %d", w.Code)
	}
}
