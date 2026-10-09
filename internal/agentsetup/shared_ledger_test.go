// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func ledgerFixture(t *testing.T) (*SharedLedger, LedgerMember, LedgerMember, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := OpenSharedLedger(filepath.Join(root, "ledger"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	a := ledgerFixtureMember(t, l, root, "ppm")
	b := ledgerFixtureMember(t, l, root, "pma")
	d, _, err := l.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []LedgerMember{a, b} {
		ledgerFixtureImport(t, l, m, d.Generation, 2, nil)
	}
	return l, a, b, d.Generation
}
func ledgerFixtureMember(t *testing.T, l *SharedLedger, root, name string) LedgerMember {
	t.Helper()
	id, err := LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	label, _ := InstanceLabel(name)
	m := LedgerMember{ID: id, Label: label, Root: filepath.Join(root, "paired-"+name), JoinedAt: time.Unix(100, 0).UTC()}
	if err = l.Register(m); err != nil {
		t.Fatal(err)
	}
	return m
}
func ledgerFixtureImport(t *testing.T, l *SharedLedger, m LedgerMember, generation string, cap int, groups []LedgerGroup) {
	t.Helper()
	v := LedgerInstance{Fingerprint: LedgerFingerprint(generation, "instance", m.ID), PID: os.Getpid(), StartedAt: time.Unix(101, 0).UTC(), MaximumAgents: cap}
	got, err := l.Import(m.ID, v, groups)
	if err != nil || got != generation {
		t.Fatal("import", got, err)
	}
	if err = l.Enrolled(m.ID, generation); err != nil {
		t.Fatal(err)
	}
}
func fixtureLogin(generation, identity string) LedgerLogin {
	return LedgerLogin{Harness: "codex", Identity: LedgerFingerprint(generation, "login/codex", identity)}
}
func fixtureAcquire(t *testing.T, l *SharedLedger, m LedgerMember, generation string, candidates []LedgerCandidate) (string, []LedgerCandidate) {
	t.Helper()
	gid, err := LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	subset, err := l.Acquire(m.ID, generation, gid, candidates)
	if err != nil {
		t.Fatal(err)
	}
	return gid, subset
}

// Risk: deleting a service and losing the ledger must never forget a surviving
// worker. Tombstones, not plists, determine who must import before admission.
func TestSharedLedgerTombstonesBlockRebuildAndOwnerLeave(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt"} {
		t.Run(damage, func(t *testing.T) {
			l, a, b, generation := ledgerFixture(t)
			login := fixtureLogin(generation, "A")
			gid, _ := fixtureAcquire(t, l, b, generation, []LedgerCandidate{{AccountID: "private-account", Login: login}})
			if err := l.Claimed(b.ID, generation, gid, login); err != nil {
				t.Fatal(err)
			}
			if err := l.Launching(b.ID, generation, gid); err != nil {
				t.Fatal(err)
			}
			if err := l.Running(b.ID, generation, gid, 3456, time.Unix(102, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			// There is no plist in this fixture, exactly as after manual service removal.
			ledgerPath := filepath.Join(l.root.Path(), "ledger.json")
			if damage == "missing" {
				if err := os.Remove(ledgerPath); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(ledgerPath, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := l.Rebuild()
			if err != nil || fresh == generation {
				t.Fatal("fresh generation", err)
			}
			ledgerFixtureImport(t, l, a, fresh, 2, nil)
			try, _ := LedgerID()
			if _, err = l.Acquire(a.ID, fresh, try, []LedgerCandidate{{AccountID: "A", Login: fixtureLogin(fresh, "A")}}); !errors.Is(err, ErrLedgerUnavailable) {
				t.Fatal("surviving member forgotten", err)
			}
			if err = l.Launching(b.ID, generation, gid); !errors.Is(err, ErrLedgerGeneration) {
				t.Fatal("old-generation launch admitted", err)
			}
			if err = l.Leave(b.ID, fresh, false); !errors.Is(err, ErrLedgerOwner) {
				t.Fatal("unverified exit removed member", err)
			}
			group := LedgerGroup{ID: gid, Instance: b.ID, Generation: fresh, State: "running", Holds: []LedgerLogin{fixtureLogin(fresh, "A")}, PID: 3456, StartedAt: time.Unix(102, 0).UTC()}
			ledgerFixtureImport(t, l, b, fresh, 2, []LedgerGroup{group})
			if err = l.Release(a.ID, fresh, gid); !errors.Is(err, ErrLedgerOwner) {
				t.Fatal("peer released surviving worker", err)
			}
			if err = l.Leave(b.ID, fresh, true); !errors.Is(err, ErrLedgerOccupied) {
				t.Fatal("live group permitted leave", err)
			}
			// Only the owner's verified exit permits release, followed by tombstone deletion.
			if err = l.Release(b.ID, fresh, gid); err != nil {
				t.Fatal(err)
			}
			if err = l.Leave(b.ID, fresh, true); err != nil {
				t.Fatal(err)
			}
			_, members, err := l.Snapshot()
			if err != nil || len(members) != 1 || members[0].ID != a.ID {
				t.Fatal("wrong membership", members, err)
			}
			fixtureAcquire(t, l, a, fresh, []LedgerCandidate{{AccountID: "A", Login: fixtureLogin(fresh, "A")}})
		})
	}
}

// Risk: pending candidate sets can overbook a login or occupy the wrong number
// of machine slots. Pins must wait and unknown identities conflict per harness.
func TestSharedLedgerAtomicPruningPinsUnknownAndMachineCap(t *testing.T) {
	l, a, b, generation := ledgerFixture(t)
	ca := LedgerCandidate{AccountID: "A", Login: fixtureLogin(generation, "A")}
	cb := LedgerCandidate{AccountID: "B", Login: fixtureLogin(generation, "B")}
	gid, _ := fixtureAcquire(t, l, a, generation, []LedgerCandidate{ca})
	if err := l.Claimed(a.ID, generation, gid, ca.Login); err != nil {
		t.Fatal(err)
	}
	pin, _ := LedgerID()
	if _, err := l.Acquire(b.ID, generation, pin, []LedgerCandidate{ca}); !errors.Is(err, ErrLedgerOccupied) {
		t.Fatal("pinned A substituted", err)
	}
	second, subset := fixtureAcquire(t, l, b, generation, []LedgerCandidate{ca, cb})
	if !slices.Equal(subset, []LedgerCandidate{cb}) {
		t.Fatal("busy A was offered", subset)
	}
	d, _, err := l.Snapshot()
	if err != nil || len(d.Groups) != 2 || len(d.Groups[second].Holds) != 1 {
		t.Fatal("groups count once", d, err)
	}
	third, _ := LedgerID()
	if _, err = l.Acquire(a.ID, generation, third, []LedgerCandidate{{AccountID: "C", Login: fixtureLogin(generation, "C")}}); !errors.Is(err, ErrLedgerOccupied) {
		t.Fatal("machine cap bypassed", err)
	}
	if err = l.Release(a.ID, generation, gid); err != nil {
		t.Fatal(err)
	}
	if err = l.Release(b.ID, generation, second); err != nil {
		t.Fatal(err)
	}
	unknown := LedgerCandidate{AccountID: "pi", Login: LedgerLogin{Harness: "codex", Identity: "unknown"}}
	fixtureAcquire(t, l, a, generation, []LedgerCandidate{unknown})
	try, _ := LedgerID()
	if _, err = l.Acquire(b.ID, generation, try, []LedgerCandidate{cb}); !errors.Is(err, ErrLedgerOccupied) {
		t.Fatal("unknown login failed to fence harness", err)
	}
}

// Risk: missing or unreadable membership may be treated as an empty machine,
// including after a rebuild. Every operation must fail for that exact reason.
func TestSharedLedgerMissingOrUnreadableMemberFailsClosed(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt", "catalog"} {
		t.Run(damage, func(t *testing.T) {
			l, a, b, generation := ledgerFixture(t)
			path := filepath.Join(l.members.Path(), b.ID+".json")
			if damage == "catalog" {
				path = filepath.Join(l.root.Path(), "membership.json")
			}
			if damage == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			id, _ := LedgerID()
			if _, err := l.Acquire(a.ID, generation, id, []LedgerCandidate{{AccountID: "A", Login: fixtureLogin(generation, "A")}}); !errors.Is(err, ErrLedgerUnavailable) {
				t.Fatal("damaged membership admitted work", err)
			}
			if _, err := l.Rebuild(); !errors.Is(err, ErrLedgerUnavailable) {
				t.Fatal("rebuild forgot member", err)
			}
		})
	}
}

// Risk: renaming ledger.json while holding its inode lock permits a second
// writer. The permanent separate lock remains the serialization authority.
func TestSharedLedgerPermanentLockSurvivesDataReplacement(t *testing.T) {
	l, _, _, _ := ledgerFixture(t)
	lock, err := l.root.LockNamed("ledger.lock")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(l.root.Path(), "ledger.lock"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := l.root.Read("ledger.json", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.root.Write("ledger.json", raw, false); err != nil {
		t.Fatal(err)
	}
	peer, err := OpenSharedLedger(l.root.Path(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, _, err = peer.Snapshot(); !errors.Is(err, ErrBusy) {
		t.Fatal("data replacement bypassed lock", err)
	}
	lock.Close()
	after, err := os.Stat(filepath.Join(l.root.Path(), "ledger.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock was replaced", err)
	}
	if _, _, err = peer.Snapshot(); err != nil {
		t.Fatal(err)
	}
}
