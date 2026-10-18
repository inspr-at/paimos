// SPDX-License-Identifier: AGPL-3.0-only

package hooknote

import "sync"

// Claim is untrusted metadata from hook stdin and the environment.
// It never selects a session. A value that contradicts the one process
// binding is a forgery and the offer is refused.
type Claim struct {
	Event           string
	VendorRef       string
	AssertedSession string
	EnvSession      string
	Subagent        bool
}

// Grant is one approved harness generation. Two grants that share a process
// identity are ambiguous even when they share a directory or a vendor reference
// would pick one of them.
type Grant struct {
	Binding   Binding
	VendorRef string
	Pending   bool
	Revoked   bool
	// epoch is the revocation counter stamped while this grant was current.
	// Revocation of its message generation replaces it. Zero is never current.
	epoch uint64
}

// Registry is the daemon's in-memory set of approved bindings.
// An empty registry refuses every peer; there is no newest-session or
// same-directory fallback.
//
// counter is the monotonic settlement epoch. It starts at 1 so a zero
// Epoch cannot settle as shown. Revocation and a second grant for the same
// harness bump it; an offered epoch from before that bump is not shown.
// revision changes when the grant set changes so an observation that raced
// a writer is discarded and retried.
type Registry struct {
	mu       sync.Mutex
	grants   []Grant
	revoked  map[string]bool
	receipts map[string]string
	counter  uint64
	revision uint64
}

func NewRegistry() *Registry {
	return &Registry{revoked: map[string]bool{}, counter: 1}
}

func (r *Registry) Add(g Grant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked == nil {
		r.revoked = map[string]bool{}
	}
	if r.counter == 0 {
		r.counter = 1
	}
	if r.revoked[g.Binding.MessageGeneration] {
		g.Revoked = true
	}
	// Ambiguity has to move the epoch. A source that only compares epochs,
	// including AEON-393's grant epoch, would otherwise accept shown.
	if !g.Revoked && r.conflictsLocked(g.Binding) {
		r.counter++
		for i := range r.grants {
			if r.grants[i].Revoked || r.revoked[r.grants[i].Binding.MessageGeneration] {
				continue
			}
			if sameHookTarget(r.grants[i].Binding, g.Binding) {
				r.grants[i].epoch = r.counter
			}
		}
	}
	g.epoch = r.counter
	r.revision++
	r.grants = append(r.grants, g)
}

func (r *Registry) conflictsLocked(b Binding) bool {
	for i := range r.grants {
		if r.grants[i].Revoked || r.revoked[r.grants[i].Binding.MessageGeneration] {
			continue
		}
		if sameHookTarget(r.grants[i].Binding, b) {
			return true
		}
	}
	return false
}

// RevokeGeneration invalidates every binding minted under that message
// generation. It takes the registry write lock, bumps the revocation counter,
// and marks those grants revoked. Other generations keep the counter they
// were stamped with.
func (r *Registry) RevokeGeneration(generation string) {
	if r == nil || generation == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked[generation] {
		return
	}
	if r.revoked == nil {
		r.revoked = map[string]bool{}
	}
	if r.counter == 0 {
		r.counter = 1
	}
	r.counter++
	r.revision++
	r.revoked[generation] = true
	for i := range r.grants {
		if r.grants[i].Binding.MessageGeneration == generation {
			r.grants[i].Revoked = true
			r.grants[i].epoch = r.counter
		}
	}
}

// Live reports whether epoch still names a grant revocation has not
// superseded. It does not observe processes and does not decide harness
// uniqueness; BindingCurrent and Commit do that.
func (r *Registry) Live(epoch Epoch) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.liveLocked(epoch)
}

func (r *Registry) liveLocked(epoch Epoch) bool {
	if epoch.Generation == "" || epoch.Counter == 0 || r.revoked[epoch.Generation] {
		return false
	}
	for _, g := range r.grants {
		if !g.Revoked && g.Binding.MessageGeneration == epoch.Generation && g.epoch == epoch.Counter {
			return true
		}
	}
	return false
}

// Receipt is the compare-and-settle result recorded for nonce, or "" when
// nothing has been recorded.
func (r *Registry) Receipt(nonce string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.receipts[nonce]
}

// Select returns the single binding whose approved harness is the hook's
// direct parent and whose pinned hook image matches the loaded executable.
// Tool descendants, siblings and subagents do not match. observe is called
// again so a start-time or image change since hook was snapshotted is a miss.
// The returned binding carries the epoch Commit checks.
func (r *Registry) Select(hook Process, claim Claim, observe func(int) (Process, error)) (Binding, error) {
	if r == nil || observe == nil || claim.Subagent || hook.PID < 1 || hook.Parent < 1 || hook.Parent == hook.PID {
		return Binding{}, ErrPeer
	}
	if claim.Event != "PostToolUse" && claim.Event != "UserPromptSubmit" && claim.Event != "Stop" {
		return Binding{}, ErrPeer
	}
	if !plain(claim.VendorRef, 128) && claim.VendorRef != "" || !plain(claim.AssertedSession, 128) && claim.AssertedSession != "" || !plain(claim.EnvSession, 128) && claim.EnvSession != "" {
		return Binding{}, ErrForged
	}
	var result Binding
	err := r.withStable(hook, observe, func(seen map[int]Process) error {
		g, err := r.resolveLocked(hook, seen)
		if err != nil {
			return err
		}
		if claim.AssertedSession != "" && claim.AssertedSession != g.Binding.SessionID {
			return ErrForged
		}
		if claim.EnvSession != "" && claim.EnvSession != g.Binding.SessionID {
			return ErrForged
		}
		// A vendor reference never chooses among bindings. It can only complete
		// one pending binding, or contradict the reference already stored.
		if claim.VendorRef != "" && g.VendorRef != "" && claim.VendorRef != g.VendorRef {
			return ErrForged
		}
		if g.Pending && g.VendorRef == "" && claim.VendorRef != "" {
			for i := range r.grants {
				if r.grants[i].Binding.SessionID == g.Binding.SessionID && r.grants[i].Binding.MessageGeneration == g.Binding.MessageGeneration && r.grants[i].VendorRef == "" {
					r.grants[i].VendorRef = claim.VendorRef
					r.grants[i].Pending = false
				}
			}
			g.VendorRef = claim.VendorRef
		}
		g.Binding.VendorRef = g.VendorRef
		g.Binding.Epoch = Epoch{Generation: g.Binding.MessageGeneration, Counter: g.epoch}
		result = g.Binding
		return nil
	})
	if err != nil {
		return Binding{}, err
	}
	return result, nil
}

// BindingCurrent reports whether an offer's binding is still the single
// approved direct-hook grant. Revocation, a second grant for the same
// harness, or a harness or hook image change since the offer fails closed.
// Processes are observed before the registry lock is taken.
func (r *Registry) BindingCurrent(b Binding, hook Process, observe func(int) (Process, error)) error {
	if r == nil || observe == nil || b.SessionID == "" || b.MessageGeneration == "" || b.DaemonGeneration == "" || hook.PID < 1 {
		return ErrPeer
	}
	return r.withStable(hook, observe, func(seen map[int]Process) error {
		return r.currentLocked(b, hook, seen)
	})
}

// Commit is the local check before a receipt is sent. It observes first,
// then under the same lock records outcome only when epoch is still the
// single current grant for this harness. Otherwise it records uncertain.
// A stored uncertain receipt is never upgraded to shown; a later uncertain
// commit replaces shown. The lock is not held across observation and not
// across a NoteSource callback. Call Commit before NoteSource.Settle.
// Do not call it again to apply a revocation that arrived during Settle.
// The server's recorded outcome is the receipt, and a completed write stays
// shown. A confirmed server answer is stored with Adopt, which replaces
// this proposal. Commit uncertain only when the response was lost.
func (r *Registry) Commit(nonce, outcome string, epoch Epoch, offered Binding, hook Process, observe func(int) (Process, error)) (string, error) {
	if r == nil || observe == nil {
		return "", ErrPeer
	}
	if !ValidNonce(nonce) || !ValidOutcome(outcome) {
		return "", ErrMalformedNonce
	}
	decided := OutcomeUncertain
	err := r.withStable(hook, observe, func(seen map[int]Process) error {
		decided = r.decideLocked(nonce, outcome, epoch, offered, hook, seen)
		return nil
	})
	if err != nil {
		r.mu.Lock()
		decided = r.storeLocked(nonce, OutcomeUncertain)
		r.mu.Unlock()
	}
	return decided, nil
}

func (r *Registry) decideLocked(nonce, outcome string, epoch Epoch, offered Binding, hook Process, seen map[int]Process) string {
	// Uncertain stays uncertain. Shown and dropped are recorded only while
	// epoch is still the single current grant; a dead epoch cannot stay shown.
	if outcome == OutcomeUncertain || !r.liveLocked(epoch) || r.currentLocked(offered, hook, seen) != nil {
		return r.storeLocked(nonce, OutcomeUncertain)
	}
	return r.storeLocked(nonce, outcome)
}

func (r *Registry) storeLocked(nonce, outcome string) string {
	if r.receipts == nil {
		r.receipts = map[string]string{}
	}
	prev, ok := r.receipts[nonce]
	if !ok {
		r.receipts[nonce] = outcome
		return outcome
	}
	if prev == OutcomeUncertain || outcome == OutcomeUncertain {
		r.receipts[nonce] = OutcomeUncertain
		return OutcomeUncertain
	}
	return prev
}

// accept reports whether epoch is still the single current local grant for
// the offered harness and hook image. It is not the server's compare-and-settle.
// The server compares its own epoch and does not read this registry. A
// NoteSource must not call accept to rewrite a receipt.
func (r *Registry) accept(epoch Epoch, offered Binding) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.liveLocked(epoch) || epoch.Generation != offered.MessageGeneration || epoch.Counter == 0 {
		return false
	}
	n := 0
	for _, g := range r.grants {
		if g.Revoked || r.revoked[g.Binding.MessageGeneration] || g.Binding.MessageGeneration == "" {
			continue
		}
		if sameHookTarget(g.Binding, offered) {
			n++
		}
	}
	return n == 1
}

func sameHookTarget(a, b Binding) bool {
	if a.HookExecutable == "" || (a.HookDev == 0 && a.HookIno == 0) {
		return false
	}
	return SameIdentity(a.Harness, b.Harness) && a.HookExecutable == b.HookExecutable && a.HookDev == b.HookDev && a.HookIno == b.HookIno
}

// withStable reads the revision, observes hook and harness pids without the
// lock, then runs fn under the lock only when that revision is unchanged.
// fn must not call Observe, Add, RevokeGeneration, or withStable.
func (r *Registry) withStable(hook Process, observe func(int) (Process, error), fn func(map[int]Process) error) error {
	if r == nil || observe == nil || hook.PID < 1 {
		return ErrPeer
	}
	for range 4 {
		r.mu.Lock()
		rev := r.revision
		pids := []int{hook.PID}
		if hook.Parent > 0 {
			pids = append(pids, hook.Parent)
		}
		for _, g := range r.grants {
			if g.Binding.Harness.PID > 0 {
				pids = append(pids, g.Binding.Harness.PID)
			}
		}
		r.mu.Unlock()
		seen := make(map[int]Process, len(pids))
		done := make(map[int]bool, len(pids))
		for _, pid := range pids {
			if done[pid] {
				continue
			}
			done[pid] = true
			proc, err := observe(pid)
			if err != nil {
				continue
			}
			seen[pid] = proc
		}
		r.mu.Lock()
		if r.revision != rev {
			r.mu.Unlock()
			continue
		}
		err := fn(seen)
		r.mu.Unlock()
		return err
	}
	return ErrPeer
}

// resolveLocked is the one direct-hook resolver. Select and BindingCurrent
// both use it. Exactly one current grant for this harness may match.
func (r *Registry) resolveLocked(hook Process, seen map[int]Process) (Grant, error) {
	again, ok := seen[hook.PID]
	if !ok || !SameIdentity(hook, again) {
		return Grant{}, ErrPeer
	}
	matched := make([]int, 0, 1)
	revokedHit := false
	for i, g := range r.grants {
		if !directHookSeen(hook, g, seen) {
			continue
		}
		if g.Revoked || r.revoked[g.Binding.MessageGeneration] || g.Binding.MessageGeneration == "" || !ValidID(g.Binding.SessionID) {
			revokedHit = true
			continue
		}
		matched = append(matched, i)
	}
	if len(matched) != 1 {
		if len(matched) == 0 && revokedHit {
			return Grant{}, ErrRevoked
		}
		if len(matched) > 1 {
			return Grant{}, ErrAmbiguous
		}
		return Grant{}, ErrPeer
	}
	return r.grants[matched[0]], nil
}

func (r *Registry) currentLocked(b Binding, hook Process, seen map[int]Process) error {
	g, err := r.resolveLocked(hook, seen)
	if err != nil {
		return err
	}
	if g.Binding.SessionID != b.SessionID || g.Binding.MessageGeneration != b.MessageGeneration || g.Binding.DaemonGeneration != b.DaemonGeneration {
		return ErrPeer
	}
	if !SameIdentity(g.Binding.Harness, b.Harness) {
		return ErrPeer
	}
	if g.Binding.HookExecutable != b.HookExecutable || g.Binding.HookDev != b.HookDev || g.Binding.HookIno != b.HookIno {
		return ErrPeer
	}
	if hook.Executable != b.HookExecutable || hook.Dev != b.HookDev || hook.Ino != b.HookIno {
		return ErrPeer
	}
	if b.Epoch.Generation != "" && (b.Epoch.Generation != g.Binding.MessageGeneration || b.Epoch.Counter != g.epoch || b.Epoch.Counter == 0) {
		return ErrRevoked
	}
	return nil
}

// directHookSeen allows only a direct child of the approved harness running
// the pinned hook image. The parent observation is the one withStable took
// before the lock. An intermediate tool shell or a sibling subagent is a
// different parent and is refused. An exec-preserving launcher is already a
// direct child: exec keeps the harness as the parent.
func directHookSeen(hook Process, g Grant, seen map[int]Process) bool {
	h := g.Binding.Harness
	if hook.Parent != h.PID || hook.UID != h.UID {
		return false
	}
	if g.Binding.HookExecutable == "" || (g.Binding.HookDev == 0 && g.Binding.HookIno == 0) {
		return false
	}
	if hook.Executable != g.Binding.HookExecutable || hook.Dev != g.Binding.HookDev || hook.Ino != g.Binding.HookIno {
		return false
	}
	parent, ok := seen[hook.Parent]
	if !ok || !SameIdentity(parent, h) {
		return false
	}
	return true
}

// Disclose serializes the body write with local revocation. Re-observation occurs
// before locking; a registry change during observation cannot validate stale state.
func (r *Registry) Disclose(b Binding, hook Process, observe func(int) (Process, error), write func() error) error {
	if write == nil {
		return ErrPeer
	}
	return r.withStable(hook, observe, func(seen map[int]Process) error {
		if err := r.currentLocked(b, hook, seen); err != nil {
			return err
		}
		return write()
	})
}
