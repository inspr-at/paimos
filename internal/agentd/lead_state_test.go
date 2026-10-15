// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Risk: a writer makes paired computers impossible to roll back, or makes a
// rollback forget an unresolved native owner. All state is under t.TempDir;
// the middle leg uses the frozen released decoder, never today's decoder.
func TestAgentdStateDowngradeRoundTrip(t *testing.T) {
	for _, kind := range []string{"routine_pending", "routine_exited", "native_pending", "native_uncertain", "native_abandoned", "native_exited"} {
		t.Run(kind, func(t *testing.T) {
			s, api, proc := testSupervisor(t)
			want := leadRecord{Version: leadRecordSchemaVersion, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: uidLead("70")}, DaemonID: s.daemonID}, Group: "retained-group"}
			if strings.HasPrefix(kind, "native_") {
				want.Native = &nativeLeadRecord{SessionID: uidLead("71"), VendorSessionID: uidLead("72"), Attempts: map[string]string{uidLead("73"): strings.Repeat("a", 64)}}
			}
			if strings.HasSuffix(kind, "exited") {
				want.Attempted, want.ExitObserved, want.ExitReported = true, true, true
				want.Ownership = &ownedprocess.Identity{ProcessID: "retained-owner", RootPID: 99, GroupID: 99, StartedAt: time.Unix(100, 0).UTC()}
			}
			want.Abandoned = kind == "native_abandoned"
			want.Attempted = want.Attempted || kind == "native_uncertain"
			blocked := kind == "native_pending" || kind == "native_uncertain"
			if err := s.writeLead(want, true); err != nil {
				t.Fatal(err)
			}
			name := "lead-runtime-" + want.Pickup.Demand.ID + ".json"
			before, err := s.state.Read(name, 32768)
			if err != nil {
				t.Fatal(err)
			}
			// N writes; the released N-1 startup decoder consumes those exact
			// bytes; N reads them again with full native evidence intact.
			err = release131CheckRetainedLeads(s.state)
			if blocked && !errors.Is(err, ErrProcessesUnconfirmed) || !blocked && err != nil {
				t.Fatal("released 131 fence returned the wrong outcome", err)
			}
			got, major, err := readLeadState(s.state, name)
			if err != nil || major != 1 || !reflect.DeepEqual(got, want) {
				t.Fatal("N → N-1 → N changed retained provenance", err)
			}
			after, err := s.state.Read(name, 32768)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("reader changed the compatibility envelope", err)
			}
			root, workspace := s.journalDir(), s.workspace
			if err := s.Close(t.Context()); blocked && !errors.Is(err, ErrProcessesUnconfirmed) || !blocked && err != nil {
				t.Fatal("close forgot retained ownership", err)
			}
			next, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: root, DaemonID: "daemon", Workspace: workspace, Adapters: []Adapter{&fakeAdapter{proc: proc}}})
			if blocked {
				if !errors.Is(err, ErrProcessesUnconfirmed) {
					t.Fatal("current restart adopted an unresolved native owner", err)
				}
				return
			}
			if err != nil {
				t.Fatal("current daemon could not restart after downgrade", err)
			}
			if err := next.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("released133-repair", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		for _, native := range []bool{false, true} {
			r := leadRecord{Version: 2, Abandoned: true, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: uidLead("74")}, DaemonID: s.daemonID}}
			if native {
				r.Native = &nativeLeadRecord{SessionID: uidLead("75")}
			}
			raw, _ := json.Marshal(r)
			name := "lead-runtime-" + r.Pickup.Demand.ID + ".json"
			if err := s.state.Write(name, raw, false); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(release131CheckRetainedLeads(s.state), ErrProcessesUnconfirmed) {
				t.Fatal("fixture does not reproduce the released incompatibility")
			}
			if err := checkRetainedLeads(s.state); err != nil {
				t.Fatal("current startup did not repair the settled 133 receipt", err)
			}
			if err := release131CheckRetainedLeads(s.state); err != nil {
				t.Fatal("repaired 133 state still strands 131", err)
			}
			got, _, err := readLeadState(s.state, name)
			if err != nil || !reflect.DeepEqual(got, r) {
				t.Fatal("repair discarded retained evidence", err)
			}
		}
	})

	t.Run("additive-minor", func(t *testing.T) {
		for _, major := range []int{1, 2} {
			raw := []byte(`{"version":` + strconv.Itoa(major) + `,"minor_version":99,"future":{"enabled":true}}`)
			if r, err := decodeLeadRecord(raw); err != nil || r.MinorVersion != 99 || r.Native != nil {
				t.Fatal("additive fields/minor revision changed authority or blocked loading", err)
			}
		}
		_, err := decodeLeadRecord([]byte(`{"version":3,"minor_version":0}`))
		var version *localjournal.SchemaVersionError
		if !errors.As(err, &version) || version.Stored != 3 {
			t.Fatal("future major was accepted", err)
		}
		for _, raw := range []string{`{"version":1,"native":{}}`, `{"version":2,"minor_version":-1}`, `{"version":2} {}`} {
			if _, err := decodeLeadRecord([]byte(raw)); !errors.Is(err, ErrProcessesUnconfirmed) {
				t.Fatal("invalid state gained authority", err)
			}
		}
	})

	t.Run("interrupted-sidecar-write", func(t *testing.T) {
		s, _, _ := testSupervisor(t)
		r := leadRecord{Native: &nativeLeadRecord{SessionID: uidLead("77")}, Abandoned: true, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: uidLead("76")}, DaemonID: s.daemonID}}
		// An exclusive sidecar collision interrupts the multi-file commit after
		// its legacy guard. Neither reader may infer a completed settlement.
		if err := s.state.Write(nativeLeadStateName(r.Pickup.Demand.ID), []byte("unrelated"), true); err != nil {
			t.Fatal(err)
		}
		if err := s.writeLead(r, true); !errors.Is(err, agentsetup.ErrCollision) {
			t.Fatal("sidecar collision was concealed", err)
		}
		if !errors.Is(release131CheckRetainedLeads(s.state), ErrProcessesUnconfirmed) || !errors.Is(checkRetainedLeads(s.state), ErrProcessesUnconfirmed) {
			t.Fatal("partial commit discarded retained ownership")
		}
		// A missing provenance file must not be treated as an absent lead and
		// permit preparation of a replacement owner.
		if err := s.state.RemoveExact(nativeLeadStateName(r.Pickup.Demand.ID), agentsetup.Hash([]byte("unrelated"))); err != nil {
			t.Fatal(err)
		}
		_, _, err := readLeadState(s.state, "lead-runtime-"+r.Pickup.Demand.ID+".json")
		if !errors.Is(err, ErrProcessesUnconfirmed) || errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing sidecar became an absent claim", err)
		}
	})
}
