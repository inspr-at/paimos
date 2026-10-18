// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/releasehistory"
)

func TestPackHistoricReservation(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	const version = "260930160000.0.0"
	const tenant = "11111111-1111-4111-8111-111111111111"
	const project = "22222222-2222-4222-8222-222222222222"
	write("version.json", map[string]any{"product": "PAIMOS AEON", "version_scheme": releasehistory.SchemeCalVer3, "version": version, "release_channel": "stable", "release_sequence": 114})
	for _, args := range [][]string{{"init", "-q"}, {"add", "version.json"}, {"commit", "-qm", "AEON-999: unrelated historic ticket"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	export := releasehistory.HistoricTicketExport{
		Schema: releasehistory.HistoricNotesSchema, TenantID: tenant, ProjectID: project,
		CapturedAt:  time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC),
		Reservation: &releasehistory.NoteReservation{Version: version, Channel: "stable", Sequence: 114, Tickets: []string{"AEON-405", "AEON-406", "AEON-407"}},
		Tickets: []releasehistory.HistoricTicket{
			{Key: "AEON-405", Kind: "ticket", Fields: json.RawMessage(`{"tags":["bug"],"pill_en":"Honest release history","pill_de":"Ehrliche Release Historie","benefit_en":"This release carries its notes.","benefit_de":"Dieses Release enthält seine Hinweise.","private":"PRIVATE EXPORT ONLY"}`)},
			{Key: "AEON-406", Kind: "ticket", Fields: json.RawMessage(`{"hide_from_release_notes":true,"pill_en":"HIDDEN TEXT"}`)},
			{Key: "AEON-407", Kind: "ticket", Fields: json.RawMessage(`{"pill_en":"New release feature","pill_de":"Neue Release Funktion","benefit_en":"This release adds a feature.","benefit_de":"Dieses Release enthält eine neue Funktion."}`)},
		},
	}
	exportRaw := write("own.json", export)
	args := []string{"-repo", dir, "-historic", filepath.Join(dir, "own.json"), "-reserve", version, "-tenant", tenant, "-project", project}
	if err := runWithArgs(args); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, releasehistory.ProductNotesPath)
	packed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := releasehistory.ReadProductNotes(packed)
	if err != nil {
		t.Fatal(err)
	}
	n := bundle.Releases[version]
	sum := sha256.Sum256(exportRaw)
	if len(bundle.Releases) != 1 || n.ReleaseChannel != "stable" || n.ReleaseSequence != 114 || n.WrittenAfterRelease || n.SHA256 != hex.EncodeToString(sum[:]) || len(n.Items) != 2 || n.Items[0].Key != "AEON-405" || n.Items[0].Group != releasehistory.GroupFixes || n.Items[0].BenefitDE == "" || n.Items[1].Key != "AEON-407" || n.Items[1].Group != releasehistory.GroupFeatures {
		t.Fatalf("wrong reservation projection: %+v", bundle)
	}
	for _, forbidden := range []string{"HIDDEN", "PRIVATE EXPORT ONLY", "AEON-406", "AEON-999", tenant, project} {
		if strings.Contains(string(packed), forbidden) {
			t.Fatal("unsafe projection:", forbidden)
		}
	}
	if err := runWithArgs(args); err != nil {
		t.Fatal("identical rerun:", err)
	}
	for name, mutate := range map[string]func(*releasehistory.HistoricTicketExport){
		"unbound":        func(e *releasehistory.HistoricTicketExport) { e.Reservation = nil },
		"wrong sequence": func(e *releasehistory.HistoricTicketExport) { e.Reservation.Sequence-- },
		"wrong version":  func(e *releasehistory.HistoricTicketExport) { e.Reservation.Version = "260930160001.0.0" },
		"wrong tenant":   func(e *releasehistory.HistoricTicketExport) { e.TenantID = project },
		"missing translation": func(e *releasehistory.HistoricTicketExport) {
			e.Tickets[0].Fields = json.RawMessage(`{"pill_en":"English only pill","benefit_en":"English only."}`)
		},
		"changed capture": func(e *releasehistory.HistoricTicketExport) { e.CapturedAt = e.CapturedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			var changed releasehistory.HistoricTicketExport
			if err := json.Unmarshal(exportRaw, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			write("own.json", changed)
			if err := runWithArgs(args); err == nil {
				t.Fatal("accepted invalid or changed reservation")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, packed) {
				t.Fatal("rejected reservation changed the frozen bundle", err)
			}
		})
	}
	write("own.json", export)
	wrongVersion := append([]string{}, args...)
	wrongVersion[5] = "260930160001.0.0"
	if err := runWithArgs(wrongVersion); err == nil {
		t.Fatal("accepted a reserve argument different from version.json")
	}
	// A retry keeps the original export and both immutable version entries.
	const retry = "261002120000.0.0"
	write("version.json", map[string]any{"product": "PAIMOS AEON", "version_scheme": releasehistory.SchemeCalVer3, "version": retry, "release_channel": "stable", "release_sequence": 114, "unpublished_reservations": []string{version}})
	retryArgs := append([]string{}, args...)
	retryArgs[5] = retry
	retryArgs = append(retryArgs, "-reuse-from", version)
	if err := runWithArgs(retryArgs); err != nil {
		t.Fatal("re-reserve original export:", err)
	}
	reread, _ := os.ReadFile(path)
	result, err := releasehistory.ReadProductNotes(reread)
	if err != nil || len(result.Releases) != 2 || result.Releases[retry].SHA256 != n.SHA256 || !result.Releases[retry].CapturedAt.Equal(*n.CapturedAt) {
		t.Fatal("retry provenance changed", err)
	}
	original, _ := json.Marshal(result.Releases[version])
	frozen, _ := json.Marshal(n)
	if !bytes.Equal(original, frozen) {
		t.Fatal("original capture rewritten")
	}
}

func TestPackRetryRequiresSequenceAbovePublished(t *testing.T) {
	for _, tc := range []struct {
		name              string
		sequence          int
		publishedSequence int
		withdrawn         bool
		wantReject        bool
	}{
		{"withdrawn 116 below published 118", 116, 118, true, true},
		{"second coordinate reuses published 119", 119, 119, false, true},
		{"legitimate max plus one retry", 119, 118, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, value any) []byte {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			versionFields := func(version, channel string, sequence int) map[string]any {
				return map[string]any{"product": "PAIMOS AEON", "version_scheme": releasehistory.SchemeCalVer3, "version": version, "release_channel": channel, "release_sequence": sequence}
			}
			const source = "261001205522.0.0"
			const published = "261002004358.0.0"
			const preview = "261002010000.0.0"
			const retry = "261002120000.0.0"
			const tenant = "11111111-1111-4111-8111-111111111111"
			const project = "22222222-2222-4222-8222-222222222222"
			write("version.json", versionFields(source, "stable", tc.sequence))
			git("init", "-q")
			git("add", "version.json")
			git("commit", "-qm", "reserve original attempt")
			write("own.json", releasehistory.HistoricTicketExport{
				Schema: releasehistory.HistoricNotesSchema, TenantID: tenant, ProjectID: project,
				CapturedAt:  time.Date(2026, 10, 1, 20, 55, 22, 0, time.UTC),
				Reservation: &releasehistory.NoteReservation{Version: source, Channel: "stable", Sequence: tc.sequence, Tickets: []string{}},
				Tickets:     []releasehistory.HistoricTicket{},
			})
			args := []string{"-repo", dir, "-historic", filepath.Join(dir, "own.json"), "-reserve", source, "-tenant", tenant, "-project", project}
			if err := runWithArgs(args); err != nil {
				t.Fatal("freeze original attempt:", err)
			}
			path := filepath.Join(dir, releasehistory.ProductNotesPath)
			frozen, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			write("version.json", versionFields(published, "stable", tc.publishedSequence))
			git("add", "version.json")
			git("commit", "-qm", "publish stable release")
			git("tag", "v"+published)
			// A higher published sequence in another channel must not block retry.
			write("version.json", versionFields(preview, "preview", 200))
			git("add", "version.json")
			git("commit", "-qm", "publish preview release")
			git("tag", "v"+preview)
			fields := versionFields(retry, "stable", tc.sequence)
			if tc.withdrawn {
				fields["withdrawn_releases"] = []releasehistory.Withdrawal{{Version: source, Digest: "sha256:" + strings.Repeat("a", 64), Ticket: "AEON-530", Reason: "failed index completion"}}
			} else {
				fields["unpublished_reservations"] = []string{source}
			}
			beforeVersion := write("version.json", fields)
			args[5] = retry
			args = append(args, "-reuse-from", source)
			err = runWithArgs(args)
			if tc.wantReject {
				if err == nil || !strings.Contains(err.Error(), "cannot reuse sequence") {
					t.Fatalf("want published sequence rejection, got %v", err)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(got, frozen) {
					t.Fatal("rejected retry changed frozen notes", readErr)
				}
				gotVersion, readErr := os.ReadFile(filepath.Join(dir, "version.json"))
				if readErr != nil || !bytes.Equal(gotVersion, beforeVersion) {
					t.Fatal("rejected retry changed version.json", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal("legitimate retry rejected:", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := releasehistory.ReadProductNotes(got)
			if err != nil || len(bundle.Releases) != 2 || bundle.Releases[retry].ReleaseSequence != tc.sequence {
				t.Fatal("retry missing or has wrong sequence", err)
			}
			original, _ := json.Marshal(bundle.Releases[source])
			reused, _ := json.Marshal(bundle.Releases[retry])
			old, err := releasehistory.ReadProductNotes(frozen)
			if err != nil {
				t.Fatal(err)
			}
			oldNotes, _ := json.Marshal(old.Releases[source])
			if !bytes.Equal(original, oldNotes) || !bytes.Equal(reused, oldNotes) {
				t.Fatal("retry changed original notes or provenance")
			}
		})
	}
}
