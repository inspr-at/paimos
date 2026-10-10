// SPDX-License-Identifier: AGPL-3.0-only

package hooknote

import (
	"errors"
	"testing"
)

const (
	sessionA = "00000000-0000-4000-8000-0000000000a1"
	sessionB = "00000000-0000-4000-8000-0000000000b2"
	genA     = "00000000-0000-4000-8000-0000000000c3"
	genB     = "00000000-0000-4000-8000-0000000000d4"
)

func harnessProc() Process {
	return Process{PID: 10, UID: 501, Parent: 1, Started: "100:1", Executable: "/usr/bin/claude", Dev: 1, Ino: 2, CWD: "/work/app", PIDVersion: 7}
}

func hookProc() Process {
	return Process{PID: 20, UID: 501, Parent: 10, Started: "200:1", Executable: "/usr/local/bin/aeon", Dev: 1, Ino: 9, CWD: "/work/app", PIDVersion: 8}
}

func grantFor(h, hook Process, session, gen string) Grant {
	return Grant{Binding: Binding{SessionID: session, MessageGeneration: gen, DaemonGeneration: "daemon-1", Harness: h, HookExecutable: hook.Executable, HookDev: hook.Dev, HookIno: hook.Ino}}
}

type world map[int]Process

func (w world) observe(pid int) (Process, error) {
	p, ok := w[pid]
	if !ok || p.PID != pid {
		return Process{}, ErrPeer
	}
	return p, nil
}

func TestSelectDirectChildAndRejectsSpoofedTree(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	tool := Process{PID: 15, UID: 501, Parent: 10, Started: "150:1", Executable: "/bin/zsh", Dev: 1, Ino: 3, CWD: "/work/app", PIDVersion: 4}
	descendant := hook
	descendant.PID, descendant.Parent, descendant.Started = 21, tool.PID, "210:1"
	sibling := Process{PID: 16, UID: 501, Parent: 1, Started: "160:1", Executable: "/usr/bin/claude", Dev: 1, Ino: 4, CWD: "/work/app", PIDVersion: 5}
	siblingHook := hook
	siblingHook.PID, siblingHook.Parent, siblingHook.Started = 22, sibling.PID, "220:1"
	w := world{h.PID: h, hook.PID: hook, tool.PID: tool, descendant.PID: descendant, sibling.PID: sibling, siblingHook.PID: siblingHook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	claim := Claim{Event: "PostToolUse"}

	got, err := reg.Select(hook, claim, w.observe)
	if err != nil || got.SessionID != sessionA || got.MessageGeneration != genA || got.DaemonGeneration != "daemon-1" {
		t.Fatalf("direct child: %v %+v", err, got)
	}
	for _, peer := range []Process{descendant, sibling, siblingHook, tool} {
		if _, err = reg.Select(peer, claim, w.observe); !errors.Is(err, ErrPeer) {
			t.Fatalf("spoofed peer pid %d: %v", peer.PID, err)
		}
	}
	if _, err = reg.Select(hook, Claim{Event: "PostToolUse", Subagent: true}, w.observe); !errors.Is(err, ErrPeer) {
		t.Fatal("subagent was accepted", err)
	}
}

func TestSelectRejectsIdentityDrift(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	claim := Claim{Event: "Stop"}

	wrongUID := hook
	wrongUID.UID = hook.UID + 1
	wrongParent := hook
	wrongParent.Parent = 99
	wrongImage := hook
	wrongImage.Ino = hook.Ino + 1
	cases := []Process{wrongUID, wrongParent, wrongImage}
	for _, peer := range cases {
		w[peer.PID] = peer
		if _, err := reg.Select(peer, claim, w.observe); !errors.Is(err, ErrPeer) {
			t.Fatal("identity drift accepted", err)
		}
	}
	// Earlier cases overwrite w[hook.PID]. The harness re-observation must
	// still see the original hook image, or this check never reaches SameIdentity.
	originalHook := hookProc()
	staleParent := h
	staleParent.Started = "stale"
	staleParent.Ino = h.Ino + 40
	staleParent.Executable = "/usr/bin/other-harness"
	if _, err := reg.Select(originalHook, claim, func(pid int) (Process, error) {
		switch pid {
		case originalHook.PID:
			return originalHook, nil
		case h.PID:
			return staleParent, nil
		default:
			return Process{}, ErrPeer
		}
	}); !errors.Is(err, ErrPeer) {
		t.Fatal("harness identity comparison was not required", err)
	}
	if _, err := reg.Select(hook, claim, func(pid int) (Process, error) {
		if pid == hook.PID {
			changed := hook
			changed.Started = "reused"
			return changed, nil
		}
		return w.observe(pid)
	}); !errors.Is(err, ErrPeer) {
		t.Fatal("pid reuse accepted", err)
	}
	if _, err := reg.Select(hook, claim, func(pid int) (Process, error) {
		if pid == hook.PID {
			changed := hook
			changed.Ino = hook.Ino + 5
			changed.Executable = "/tmp/replaced"
			return changed, nil
		}
		return w.observe(pid)
	}); !errors.Is(err, ErrPeer) {
		t.Fatal("exec-in-place accepted", err)
	}
}

func TestSelectAmbiguousSameDirectoryAndForgedClaims(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	other := h
	other.PID, other.Started, other.Ino = 11, "101:1", 6
	w := world{h.PID: h, hook.PID: hook, other.PID: other}
	reg := NewRegistry()
	first := grantFor(h, hook, sessionA, genA)
	first.VendorRef = "real-ref"
	second := grantFor(h, hook, sessionB, genB)
	second.VendorRef = "other-ref"
	second.Binding.Harness.CWD = h.CWD
	reg.Add(first)
	reg.Add(second)
	if _, err := reg.Select(hook, Claim{Event: "UserPromptSubmit", VendorRef: "real-ref"}, w.observe); !errors.Is(err, ErrAmbiguous) {
		t.Fatal("vendor ref disambiguated same-directory sessions", err)
	}

	unique := NewRegistry()
	unique.Add(grantFor(h, hook, sessionA, genA))
	unique.Add(grantFor(other, hook, sessionB, genB))
	got, err := unique.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil || got.SessionID != sessionA {
		t.Fatal("same directory selected the wrong session", err, got.SessionID)
	}
	if _, err = unique.Select(hook, Claim{Event: "PostToolUse", VendorRef: "forged-ref"}, w.observe); err != nil {
		t.Fatal("unbound vendor ref rejected a direct child", err)
	}
	bound := NewRegistry()
	g := grantFor(h, hook, sessionA, genA)
	g.VendorRef = "real-ref"
	bound.Add(g)
	if _, err = bound.Select(hook, Claim{Event: "PostToolUse", VendorRef: "forged-ref"}, w.observe); !errors.Is(err, ErrForged) {
		t.Fatal("forged vendor ref", err)
	}
	if _, err = bound.Select(hook, Claim{Event: "PostToolUse", AssertedSession: sessionB}, w.observe); !errors.Is(err, ErrForged) {
		t.Fatal("forged asserted session", err)
	}
	if _, err = bound.Select(hook, Claim{Event: "PostToolUse", EnvSession: sessionB}, w.observe); !errors.Is(err, ErrForged) {
		t.Fatal("forged environment binding", err)
	}
	miss := hook
	miss.Parent = 404
	if _, err = bound.Select(miss, Claim{Event: "PostToolUse", EnvSession: sessionA}, w.observe); !errors.Is(err, ErrPeer) {
		t.Fatal("environment binding selected a session", err)
	}
}

func TestSelectRevokedGenerationAndPendingRef(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	reg := NewRegistry()
	g := grantFor(h, hook, sessionA, genA)
	g.Pending = true
	reg.Add(g)
	got, err := reg.Select(hook, Claim{Event: "PostToolUse", VendorRef: "first-ref"}, w.observe)
	if err != nil || got.VendorRef != "first-ref" {
		t.Fatal("pending ref", err, got.VendorRef)
	}
	if _, err = reg.Select(hook, Claim{Event: "PostToolUse", VendorRef: "second-ref"}, w.observe); !errors.Is(err, ErrForged) {
		t.Fatal("replaced vendor ref", err)
	}
	reg.RevokeGeneration(genA)
	if _, err = reg.Select(hook, Claim{Event: "PostToolUse", VendorRef: "first-ref"}, w.observe); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked generation", err)
	}
}

func TestBindingCurrentRejectsRevocationAndHarnessDrift(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	reg := NewRegistry()
	g := grantFor(h, hook, sessionA, genA)
	reg.Add(g)
	if err := reg.BindingCurrent(g.Binding, hook, world{h.PID: h, hook.PID: hook}.observe); err != nil {
		t.Fatal(err)
	}
	reg.RevokeGeneration(genA)
	if err := reg.BindingCurrent(g.Binding, hook, world{h.PID: h, hook.PID: hook}.observe); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked generation still current", err)
	}
	fresh := NewRegistry()
	fresh.Add(grantFor(h, hook, sessionA, genA))
	drifted := h
	drifted.Started = "replaced"
	drifted.Ino = h.Ino + 3
	if err := fresh.BindingCurrent(g.Binding, hook, func(pid int) (Process, error) {
		if pid == h.PID {
			return drifted, nil
		}
		if pid == hook.PID {
			return hook, nil
		}
		return Process{}, ErrPeer
	}); !errors.Is(err, ErrPeer) {
		t.Fatal("harness drift still current", err)
	}
}

const epochNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRevocationDuringObservationFailsClosed(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	reg := NewRegistry()
	g := grantFor(h, hook, sessionA, genA)
	reg.Add(g)
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, world{h.PID: h, hook.PID: hook}.observe)
	if err != nil || offered.Epoch.Counter == 0 || offered.Epoch.Generation != genA {
		t.Fatal(err, offered.Epoch)
	}
	obs := func(pid int) (Process, error) {
		if pid == h.PID {
			reg.RevokeGeneration(genA)
			return h, nil
		}
		return hook, nil
	}
	if err = reg.BindingCurrent(offered, hook, obs); err == nil {
		t.Fatal("binding accepted after revocation completed during re-observation")
	}
	out, err := reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, obs)
	if err != nil || out == OutcomeShown || reg.Receipt(epochNonce) == OutcomeShown {
		t.Fatalf("shown after revocation during observation out=%s receipt=%s err=%v", out, reg.Receipt(epochNonce), err)
	}
}

func TestSecondGrantForTheSameHarnessIsUncertain(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	for _, next := range []Grant{
		grantFor(h, hook, sessionA, genB),
		grantFor(h, hook, sessionB, genA),
	} {
		reg := NewRegistry()
		g := grantFor(h, hook, sessionA, genA)
		reg.Add(g)
		offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
		if err != nil {
			t.Fatal(err)
		}
		reg.Add(next)
		if err = reg.BindingCurrent(offered, hook, w.observe); !errors.Is(err, ErrAmbiguous) {
			t.Fatal(err)
		}
		out, err := reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, w.observe)
		if err != nil || out != OutcomeUncertain || reg.Receipt(epochNonce) == OutcomeShown {
			t.Fatalf("ambiguous grant settled %s %v", out, err)
		}
	}
}

func TestRevokeDoesNotRetireAnotherGeneration(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	other := h
	other.PID, other.Started, other.Ino = 11, "101:1", 6
	w := world{h.PID: h, hook.PID: hook, other.PID: other}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	reg.Add(grantFor(other, hook, sessionB, genB))
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil || offered.SessionID != sessionA {
		t.Fatal(err, offered.SessionID)
	}
	reg.RevokeGeneration(genB)
	if !reg.Live(offered.Epoch) {
		t.Fatal("revoking another generation retired this epoch")
	}
	if err = reg.BindingCurrent(offered, hook, w.observe); err != nil {
		t.Fatal(err)
	}
	reg.RevokeGeneration(genA)
	if reg.Live(offered.Epoch) {
		t.Fatal("revoked epoch still live")
	}
}

func TestCommitDowngradesADeadEpochAndRefusesZero(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Commit(epochNonce, OutcomeShown, Epoch{}, offered, hook, w.observe)
	if err != nil || out != OutcomeUncertain {
		t.Fatalf("zero epoch settled %s %v", out, err)
	}
	second := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	out, err = reg.Commit(second, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeShown {
		t.Fatal(out, err)
	}
	reg.RevokeGeneration(genA)
	out, err = reg.Commit(second, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeUncertain || reg.Receipt(second) != OutcomeUncertain {
		t.Fatalf("dead epoch stayed shown %s receipt %s", out, reg.Receipt(second))
	}
	out, _ = reg.Commit(second, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if out != OutcomeUncertain {
		t.Fatal("uncertain receipt was upgraded")
	}
}

func TestAdoptReplacesTheLocalProposal(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeShown || reg.Receipt(epochNonce) != OutcomeShown {
		t.Fatalf("proposal %s receipt %s err %v", out, reg.Receipt(epochNonce), err)
	}
	out, err = reg.Commit(epochNonce, OutcomeDropped, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeShown || reg.Receipt(epochNonce) != OutcomeShown {
		t.Fatalf("local conflict replaced shown with %s receipt %s", out, reg.Receipt(epochNonce))
	}
	stored, err := reg.Adopt(epochNonce, OutcomeDropped)
	if err != nil || stored != OutcomeDropped || reg.Receipt(epochNonce) != OutcomeDropped {
		t.Fatalf("server dropped stored %s receipt %s err %v", stored, reg.Receipt(epochNonce), err)
	}
	stored, err = reg.Adopt(epochNonce, OutcomeUncertain)
	if err != nil || stored != OutcomeUncertain || reg.Receipt(epochNonce) != OutcomeUncertain {
		t.Fatalf("server uncertain stored %s receipt %s err %v", stored, reg.Receipt(epochNonce), err)
	}
	out, err = reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeUncertain || reg.Receipt(epochNonce) != OutcomeUncertain {
		t.Fatalf("local commit upgraded uncertain to %s receipt %s", out, reg.Receipt(epochNonce))
	}
	stored, err = reg.Adopt(epochNonce, OutcomeShown)
	if err != nil || stored != OutcomeShown || reg.Receipt(epochNonce) != OutcomeShown {
		t.Fatalf("server shown stored %s receipt %s err %v", stored, reg.Receipt(epochNonce), err)
	}
	if _, err = reg.Adopt(epochNonce, "nope"); err == nil || reg.Receipt(epochNonce) != OutcomeShown {
		t.Fatalf("invalid outcome adopted, receipt %s err %v", reg.Receipt(epochNonce), err)
	}
}

func TestConflictingGrantRetiresTheOfferedEpoch(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	w := world{h.PID: h, hook.PID: hook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Live(offered.Epoch) || !reg.accept(offered.Epoch, offered) {
		t.Fatal("fresh epoch was not acceptable")
	}
	other := h
	other.PID, other.Started, other.Ino = 11, "101:1", 6
	otherHook := hook
	otherHook.PID, otherHook.Parent, otherHook.Started = 21, other.PID, "210:1"
	reg.Add(grantFor(other, otherHook, sessionB, genB))
	if !reg.Live(offered.Epoch) || !reg.accept(offered.Epoch, offered) {
		t.Fatal("another harness retired this epoch")
	}
	reg.Add(grantFor(h, hook, sessionB, genA))
	if reg.Live(offered.Epoch) || reg.accept(offered.Epoch, offered) {
		t.Fatal("conflicting grant left the offered epoch current")
	}
	out, err := reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeUncertain || reg.Receipt(epochNonce) == OutcomeShown {
		t.Fatalf("conflict settled %s %v receipt %s", out, err, reg.Receipt(epochNonce))
	}
}

func TestSameGenerationOnAnotherHarnessStaysShown(t *testing.T) {
	h, hook := harnessProc(), hookProc()
	other := h
	other.PID, other.Started, other.Ino = 11, "101:1", 6
	otherHook := hook
	otherHook.PID, otherHook.Parent, otherHook.Started = 21, other.PID, "210:1"
	w := world{h.PID: h, hook.PID: hook, other.PID: other, otherHook.PID: otherHook}
	reg := NewRegistry()
	reg.Add(grantFor(h, hook, sessionA, genA))
	reg.Add(grantFor(other, otherHook, sessionB, genA))
	offered, err := reg.Select(hook, Claim{Event: "PostToolUse"}, w.observe)
	if err != nil || offered.SessionID != sessionA {
		t.Fatal(err, offered.SessionID)
	}
	out, err := reg.Commit(epochNonce, OutcomeShown, offered.Epoch, offered, hook, w.observe)
	if err != nil || out != OutcomeShown {
		t.Fatal(out, err)
	}
}

func TestUnavailableReleasesNothing(t *testing.T) {
	note, nonce, err := Unavailable{}.Offer(t.Context(), Binding{})
	if err != ErrUnavailable || note.Body != "" || nonce != "" {
		t.Fatal("stub released a note")
	}
	stub := Unavailable{}
	if stub.Settle(t.Context(), "abcd", OutcomeShown, Epoch{}) != ErrUnavailable {
		t.Fatal("stub settled")
	}
}
