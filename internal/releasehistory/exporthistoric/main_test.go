// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func TestExporterFreezesOwnReleaseBeforeTag(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, stringData string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stringData), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "/tmp/\n")
	write("version.json", `{"product":"PAIMOS AEON","version_scheme":"inspr-calver-3","version":"260930160000.0.0","release_channel":"stable","release_sequence":114}`)
	for _, args := range [][]string{{"add", ".gitignore", "version.json"}, {"commit", "-qm", "AEON-999: historical ticket must not enter own scope"}} {
		if err := exec.Command("git", append([]string{"-C", dir}, args...)...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	const tenant = "11111111-1111-4111-8111-111111111111"
	const project = "22222222-2222-4222-8222-222222222222"
	client := filepath.Join(dir, "fake-client")
	write("fake-client", `#!/bin/sh
case "$2" in
/api/me) echo '{"tenant":{"id":"11111111-1111-4111-8111-111111111111"}}';;
/api/kinds) echo '{"items":[{"id":"pk","slug":"project"},{"id":"tk","slug":"ticket"}]}';;
/api/nodes/22222222-2222-4222-8222-222222222222) echo '{"id":"22222222-2222-4222-8222-222222222222","kind_id":"pk"}';;
/api/node-keys/AEON-405) echo '{"id":"ticket","key":"AEON-405","kind_id":"tk","parent_id":"22222222-2222-4222-8222-222222222222","fields":{"tags":["bug"],"pill_en":"Honest release history","pill_de":"Ehrliche Release Historie","benefit_en":"This release carries its notes.","benefit_de":"Dieses Release enthält seine Hinweise.","private":"DO NOT EMBED"}}';;
/api/node-keys/AEON-406) echo '{"id":"hidden","key":"AEON-406","kind_id":"tk","parent_id":"22222222-2222-4222-8222-222222222222","fields":{"hide_from_release_notes":true,"pill_en":"HIDDEN TEXT"}}';;
*) exit 1;;
esac
`)
	if err := os.Chmod(client, 0700); err != nil {
		t.Fatal(err)
	}
	write("scope.json", `["AEON-406","AEON-405"]`)
	o := options{repo: dir, client: client, tenant: tenant, project: project, out: filepath.Join(dir, "tmp/own.json"), reserve: "260930160000.0.0", tickets: filepath.Join(dir, "scope.json")}
	if err := runExport(o); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(o.out)
	if err != nil {
		t.Fatal(err)
	}
	var e releasehistory.HistoricTicketExport
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Reservation == nil || e.Reservation.Sequence != 114 || len(e.Tickets) != 2 {
		t.Fatalf("wrong own scope: %+v", e.Reservation)
	}
	b := releasehistory.EmptyProductNotes()
	if err := b.AddReserved(raw, *e.Reservation, tenant, project); err != nil {
		t.Fatal(err)
	}
	n := b.Releases[o.reserve]
	if n.WrittenAfterRelease || len(n.Items) != 1 || n.Items[0].Group != releasehistory.GroupFixes || n.Items[0].BenefitDE == "" {
		t.Fatalf("own notes: %+v", n)
	}
	packed, _ := json.Marshal(b)
	for _, forbidden := range []string{"HIDDEN", "DO NOT EMBED", "AEON-406", "AEON-999", tenant, project} {
		if strings.Contains(string(packed), forbidden) {
			t.Fatal("unsafe projection", forbidden)
		}
	}
	if err := runExport(o); err == nil {
		t.Fatal("overwrote original export")
	}
	o.out = filepath.Join(dir, "tmp/wrong-version.json")
	o.reserve = "260930160001.0.0"
	if err := runExport(o); err == nil {
		t.Fatal("accepted different version")
	}
	o.out = filepath.Join(dir, "tracked.json")
	o.reserve = "260930160000.0.0"
	if err := runExport(o); err == nil {
		t.Fatal("wrote raw export outside ignored output")
	}
}
