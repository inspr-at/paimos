// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	ErrLedgerUnavailable = errors.New("shared ledger unavailable; rebuild and import every member before admission")
	ErrLedgerGeneration  = errors.New("shared ledger generation changed; import before admission")
	ErrLedgerOccupied    = errors.New("shared machine or login capacity occupied")
	ErrLedgerOwner       = errors.New("ledger operation is not owned by this instance")
)

const ledgerSchema = 1
const maxLedgerMembers = 32
const maxLedgerGroups = 4096

// LedgerMember is a durable tombstone, independent of services and ledger.json.
// No tenant, ticket, login or credential content enters a member record.
type LedgerMember struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Root     string    `json:"root"`
	JoinedAt time.Time `json:"joined_at"`
}
type LedgerLogin struct {
	Harness  string `json:"harness"`
	Identity string `json:"identity"`
}
type LedgerCandidate struct {
	AccountID string      `json:"account_id"` // private journal only; never serialized in the shared ledger
	Login     LedgerLogin `json:"login"`
}
type LedgerGroup struct {
	ID         string        `json:"id"`
	Instance   string        `json:"instance"`
	Generation string        `json:"generation"`
	State      string        `json:"state"`
	Holds      []LedgerLogin `json:"holds"`
	PID        int           `json:"pid,omitempty"`
	StartedAt  time.Time     `json:"started_at,omitempty"`
}
type LedgerInstance struct {
	Fingerprint   string        `json:"fingerprint"`
	PID           int           `json:"pid"`
	StartedAt     time.Time     `json:"started_at"`
	MaximumAgents int           `json:"maximum_agents"`
	Imported      bool          `json:"imported"`
	Enrolled      bool          `json:"enrolled"`
	Logins        []LedgerLogin `json:"logins"`
}
type LedgerData struct {
	Schema     int                       `json:"schema"`
	Generation string                    `json:"generation"`
	Rebuilding bool                      `json:"rebuilding"`
	Instances  map[string]LedgerInstance `json:"instances"`
	Groups     map[string]LedgerGroup    `json:"groups"`
}
type ledgerMembership struct {
	Schema  int               `json:"schema"`
	Members map[string]string `json:"members"` // record digests detect missing/replaced tombstones even after ledger loss
}

type SharedLedger struct {
	root           *Store
	members        *Store
	acquireBarrier func() // deterministic fixture barrier inside the admission lock
}

func LedgerID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func ledgerID(id string) bool { return len(id) == 32 && isHex(id) }
func isHex(id string) bool {
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

// LedgerFingerprint uses the private ledger's generation as a local consistency
// salt, not an authentication key. No credential is created or carried between
// trust contexts. Unknown identities remain conservative harness-wide holds.
func LedgerFingerprint(generation, domain, value string) string {
	if !uuidPattern.MatchString(generation) || value == "" {
		return "unknown"
	}
	mac := hmac.New(sha256.New, []byte(generation))
	mac.Write([]byte(domain))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
func CanonicalLedgerOrigin(origin string) (string, error) {
	if err := ValidateOrigin(origin); err != nil {
		return "", err
	}
	// ValidateOrigin excludes credentials, paths, queries and fragments.
	origin = strings.ToLower(strings.TrimSuffix(origin, "/"))
	return strings.TrimSuffix(origin, ":443"), nil
}

func OpenSharedLedger(path string, create bool) (_ *SharedLedger, err error) {
	_, priorErr := os.Lstat(path)
	fresh := errors.Is(priorErr, os.ErrNotExist)
	root, err := OpenStore(path, create)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			root.Close()
		}
	}()
	members, err := OpenStore(filepath.Join(path, "members"), create)
	if err != nil {
		return nil, err
	}
	l := &SharedLedger{root: root, members: members}
	if create {
		err = l.locked(func() error {
			_, e := root.Read("membership.json", 64<<10)
			if errors.Is(e, os.ErrNotExist) {
				if !fresh {
					return ErrLedgerUnavailable
				}
				names, ne := members.Names(maxLedgerMembers + 32)
				if ne != nil || len(names) != 0 {
					return ErrLedgerUnavailable
				}
				// A missing catalog in an existing installation is never an empty machine.
				if _, de := root.Read("ledger.json", 1<<20); !errors.Is(de, os.ErrNotExist) {
					return ErrLedgerUnavailable
				}
				catalog := ledgerMembership{Schema: ledgerSchema, Members: map[string]string{}}
				if e = writeLedgerJSON(root, "membership.json", catalog, true); e != nil {
					return e
				}
				d, de := newLedger(false)
				if de != nil {
					return de
				}
				return writeLedgerJSON(root, "ledger.json", d, true)
			}
			return e
		})
		if err != nil {
			members.Close()
			return nil, err
		}
	}
	return l, nil
}
func (l *SharedLedger) Close() error { return errors.Join(l.members.Close(), l.root.Close()) }
func (l *SharedLedger) locked(fn func() error) error {
	lock, err := l.root.LockNamed("ledger.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn()
}
func writeLedgerJSON(s *Store, name string, v any, exclusive bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Write(name, raw, exclusive)
}
func decodeLedgerJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrLedgerUnavailable
	}
	return nil
}
func newLedger(rebuilding bool) (LedgerData, error) {
	generation, err := uuid()
	return LedgerData{Schema: ledgerSchema, Generation: generation, Rebuilding: rebuilding, Instances: map[string]LedgerInstance{}, Groups: map[string]LedgerGroup{}}, err
}
func validMember(m LedgerMember) bool {
	return ledgerID(m.ID) && validInstanceLabel(m.Label) && filepath.IsAbs(m.Root) && filepath.Clean(m.Root) == m.Root && !m.JoinedAt.IsZero()
}
func (l *SharedLedger) membership() (ledgerMembership, []LedgerMember, error) {
	var catalog ledgerMembership
	raw, err := l.root.Read("membership.json", 64<<10)
	if err != nil || decodeLedgerJSON(raw, &catalog) != nil || catalog.Schema != ledgerSchema || catalog.Members == nil || len(catalog.Members) > maxLedgerMembers {
		return catalog, nil, ErrLedgerUnavailable
	}
	names, err := l.members.Names(maxLedgerMembers + 32)
	if err != nil {
		return catalog, nil, ErrLedgerUnavailable
	}
	for _, name := range names {
		// An interrupted atomic write is not a member. Unknown durable records fail closed.
		if strings.HasPrefix(name, ".write-") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || catalog.Members[id] == "" {
			return catalog, nil, ErrLedgerUnavailable
		}
	}
	out := make([]LedgerMember, 0, len(catalog.Members))
	for id, digest := range catalog.Members {
		if !ledgerID(id) {
			return catalog, nil, ErrLedgerUnavailable
		}
		raw, err := l.members.Read(id+".json", 4096)
		var m LedgerMember
		if err != nil || Hash(raw) != digest || decodeLedgerJSON(raw, &m) != nil || !validMember(m) || m.ID != id {
			return catalog, nil, ErrLedgerUnavailable
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b LedgerMember) int { return strings.Compare(a.ID, b.ID) })
	return catalog, out, nil
}
func (l *SharedLedger) read() (LedgerData, error) {
	var d LedgerData
	raw, err := l.root.Read("ledger.json", 1<<20)
	if err != nil || decodeLedgerJSON(raw, &d) != nil || d.Schema != ledgerSchema || !uuidPattern.MatchString(d.Generation) || d.Instances == nil || d.Groups == nil || len(d.Groups) > maxLedgerGroups || len(d.Instances) > maxLedgerMembers {
		return d, ErrLedgerUnavailable
	}
	catalog, _, err := l.membership()
	if err != nil {
		return d, err
	}
	for id, v := range d.Instances {
		if catalog.Members[id] == "" || len(v.Fingerprint) != 64 || !isHex(v.Fingerprint) || v.PID < 1 || v.StartedAt.IsZero() || v.MaximumAgents < 0 || v.MaximumAgents > 64 || len(v.Logins) > 64 {
			return d, ErrLedgerUnavailable
		}
		for _, login := range v.Logins {
			if !validLogin(login) {
				return d, ErrLedgerUnavailable
			}
		}
	}
	for id, g := range d.Groups {
		if id != g.ID || !ledgerID(id) || g.Generation != d.Generation || catalog.Members[g.Instance] == "" || !validGroup(g) {
			return d, ErrLedgerUnavailable
		}
	}
	return d, nil
}
func validLogin(v LedgerLogin) bool {
	return len(v.Harness) > 0 && len(v.Harness) <= 32 && (v.Identity == "unknown" || len(v.Identity) == 64 && isHex(v.Identity))
}
func validGroup(g LedgerGroup) bool {
	if !ledgerID(g.ID) || !ledgerID(g.Instance) || !uuidPattern.MatchString(g.Generation) || len(g.Holds) < 1 || len(g.Holds) > 64 || g.PID < 0 {
		return false
	}
	switch g.State {
	case "pending", "claimed", "launching", "running":
	default:
		return false
	}
	if g.State != "pending" && len(g.Holds) != 1 {
		return false
	}
	for _, h := range g.Holds {
		if !validLogin(h) {
			return false
		}
	}
	return true
}
func (l *SharedLedger) Snapshot() (LedgerData, []LedgerMember, error) {
	var d LedgerData
	var members []LedgerMember
	err := l.locked(func() error {
		var err error
		d, err = l.read()
		if err != nil {
			return err
		}
		_, members, err = l.membership()
		return err
	})
	return d, members, err
}
func (l *SharedLedger) Register(m LedgerMember) error {
	if !validMember(m) {
		return ErrLedgerOwner
	}
	return l.locked(func() error {
		var repair ledgerMembership
		catalogRaw, err := l.root.Read("membership.json", 64<<10)
		if err != nil || decodeLedgerJSON(catalogRaw, &repair) != nil || repair.Schema != ledgerSchema || repair.Members == nil {
			return ErrLedgerUnavailable
		}
		expected, _ := json.Marshal(m)
		recordRaw, recordErr := l.members.Read(m.ID+".json", 4096)
		if repair.Members[m.ID] == "" && recordErr == nil && bytes.Equal(recordRaw, expected) {
			if len(repair.Members) >= maxLedgerMembers {
				return ErrLedgerUnavailable
			}
			repair.Members[m.ID] = Hash(expected)
			if err = writeLedgerJSON(l.root, "membership.json", repair, false); err != nil {
				return err
			}
		}

		catalog, members, err := l.membership()
		if err != nil {
			return err
		}
		for _, old := range members {
			if old.ID == m.ID {
				if old != m {
					return ErrLedgerOwner
				}
				return nil
			}
			if old.Label == m.Label || old.Root == m.Root {
				return ErrLedgerOwner
			}
		}
		if len(catalog.Members) >= maxLedgerMembers {
			return ErrLedgerUnavailable
		}
		// Tombstone first, then catalog. A crash between these writes blocks all admission.
		raw, _ := json.Marshal(m)
		if err = l.members.Write(m.ID+".json", raw, true); err != nil {
			return err
		}
		catalog.Members[m.ID] = Hash(raw)
		return writeLedgerJSON(l.root, "membership.json", catalog, false)
	})
}

// Import replaces only the owner's occupancy. It does not release another
// member or declare any launch exited. Caller holds its dispatch mutex.
func (l *SharedLedger) Import(member string, instance LedgerInstance, groups []LedgerGroup) (string, error) {
	var generation string
	err := l.locked(func() error {
		d, err := l.read()
		if err != nil {
			return err
		}
		catalog, _, err := l.membership()
		if err != nil || catalog.Members[member] == "" {
			return ErrLedgerOwner
		}
		if instance.PID < 1 || instance.StartedAt.IsZero() || (instance.MaximumAgents < 0 || instance.MaximumAgents > 64) || len(instance.Logins) > 64 {
			return ErrLedgerUnavailable
		}
		for _, login := range instance.Logins {
			if !validLogin(login) {
				return ErrLedgerUnavailable
			}
		}
		if instance.Fingerprint == "unknown" {
			return ErrLedgerUnavailable
		}
		for id, g := range d.Groups {
			if g.Instance == member {
				delete(d.Groups, id)
			}
		}
		for _, g := range groups {
			if g.Instance != member || g.Generation != d.Generation || !validGroup(g) {
				return ErrLedgerGeneration
			}
			if _, exists := d.Groups[g.ID]; exists {
				return ErrLedgerOwner
			}
			d.Groups[g.ID] = g
		}
		if len(d.Groups) > maxLedgerGroups {
			return ErrLedgerUnavailable
		}
		instance.Imported, instance.Enrolled = true, false
		d.Instances[member] = instance
		generation = d.Generation
		return writeLedgerJSON(l.root, "ledger.json", d, false)
	})
	return generation, err
}
func (l *SharedLedger) Enrolled(member, generation string) error {
	return l.mutate(member, generation, false, func(d *LedgerData) error {
		v, ok := d.Instances[member]
		if !ok || !v.Imported {
			return ErrLedgerOwner
		}
		v.Enrolled = true
		d.Instances[member] = v
		catalog, _, err := l.membership()
		if err != nil {
			return err
		}
		ready := true
		for id := range catalog.Members {
			v := d.Instances[id]
			ready = ready && v.Imported && v.Enrolled
		}
		if ready {
			d.Rebuilding = false
		}
		return nil
	})
}
func (l *SharedLedger) ready(d LedgerData) error {
	if d.Rebuilding {
		return ErrLedgerUnavailable
	}
	catalog, _, err := l.membership()
	if err != nil {
		return err
	}
	for id := range catalog.Members {
		v := d.Instances[id]
		if !v.Imported || !v.Enrolled {
			return ErrLedgerUnavailable
		}
	}
	return nil
}
func (l *SharedLedger) mutate(member, generation string, admission bool, fn func(*LedgerData) error) error {
	return l.locked(func() error {
		d, err := l.read()
		if err != nil {
			return err
		}
		if d.Generation != generation {
			return ErrLedgerGeneration
		}
		if _, ok := d.Instances[member]; !ok {
			return ErrLedgerOwner
		}
		if admission {
			if err = l.ready(d); err != nil {
				return err
			}
		}
		if err = fn(&d); err != nil {
			return err
		}
		return writeLedgerJSON(l.root, "ledger.json", d, false)
	})
}
func loginConflict(candidate LedgerLogin, owner string, g LedgerGroup) bool {
	for _, held := range g.Holds {
		if (candidate.Harness == held.Harness || candidate.Harness == "*" || held.Harness == "*") && (candidate.Identity == held.Identity || g.Instance != owner && (candidate.Identity == "unknown" || held.Identity == "unknown")) {
			return true
		}
	}
	return false
}

// Acquire prunes under the same lock as the group write. One group consumes one
// machine slot regardless of the number of candidate login holds.
func (l *SharedLedger) Acquire(member, generation, gid string, candidates []LedgerCandidate) ([]LedgerCandidate, error) {
	var subset []LedgerCandidate
	if !ledgerID(gid) || len(candidates) < 1 || len(candidates) > 64 {
		return nil, ErrLedgerUnavailable
	}
	err := l.mutate(member, generation, true, func(d *LedgerData) error {
		if _, ok := d.Groups[gid]; ok {
			return ErrLedgerOwner
		}
		cap := 0
		for _, v := range d.Instances {
			if v.MaximumAgents > 0 && (cap == 0 || v.MaximumAgents < cap) {
				cap = v.MaximumAgents
			}
		}
		if len(d.Groups) >= maxLedgerGroups || cap > 0 && len(d.Groups) >= cap {
			return ErrLedgerOccupied
		}
		holds := []LedgerLogin{}
		seen := map[string]bool{}
		for _, c := range candidates {
			if c.AccountID == "" || seen[c.AccountID] || !validLogin(c.Login) {
				return ErrLedgerUnavailable
			}
			seen[c.AccountID] = true
			busy := false
			for _, g := range d.Groups {
				busy = busy || loginConflict(c.Login, member, g)
			}
			if !busy {
				subset = append(subset, c)
				if !slices.Contains(holds, c.Login) {
					holds = append(holds, c.Login)
				}
			}
		}
		if len(subset) == 0 {
			return ErrLedgerOccupied
		}
		if l.acquireBarrier != nil {
			l.acquireBarrier()
		}
		d.Groups[gid] = LedgerGroup{ID: gid, Instance: member, Generation: generation, State: "pending", Holds: holds}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return subset, nil
}
func (l *SharedLedger) Claimed(member, generation, gid string, login LedgerLogin) error {
	return l.mutate(member, generation, true, func(d *LedgerData) error {
		g, ok := d.Groups[gid]
		if !ok || g.Instance != member {
			return ErrLedgerOwner
		}
		if g.State != "pending" && g.State != "claimed" || !slices.Contains(g.Holds, login) {
			return ErrLedgerOwner
		}
		g.State, g.Holds = "claimed", []LedgerLogin{login}
		d.Groups[gid] = g
		return nil
	})
}
func (l *SharedLedger) Launching(member, generation, gid string) error {
	return l.mutate(member, generation, true, func(d *LedgerData) error {
		g, ok := d.Groups[gid]
		if !ok || g.Instance != member || g.State != "claimed" {
			return ErrLedgerOwner
		}
		g.State = "launching"
		d.Groups[gid] = g
		return nil
	})
}
func (l *SharedLedger) Running(member, generation, gid string, pid int, started time.Time) error {
	return l.mutate(member, generation, false, func(d *LedgerData) error {
		g, ok := d.Groups[gid]
		if !ok || g.Instance != member || g.State != "launching" || pid < 1 || started.IsZero() {
			return ErrLedgerOwner
		}
		g.State, g.PID, g.StartedAt = "running", pid, started
		d.Groups[gid] = g
		return nil
	})
}

// Release is owner-only. The caller must prove no attempt, a confirmed Route
// rejection, or verified local exit in its private journal, under dispatchMu.
func (l *SharedLedger) Release(member, generation, gid string) error {
	return l.mutate(member, generation, false, func(d *LedgerData) error {
		g, ok := d.Groups[gid]
		if !ok {
			return nil
		}
		if g.Instance != member {
			return ErrLedgerOwner
		}
		delete(d.Groups, gid)
		return nil
	})
}

// CheckGroup validates retained attempt coordinates before a server replay.
// Rebuilt occupancy cannot make a fresh routing attempt until every member is
// imported and enrolled, even when this owner's server is already reachable.
func (l *SharedLedger) CheckGroup(member, generation, gid string) error {
	return l.locked(func() error {
		d, err := l.read()
		if err != nil {
			return err
		}
		if d.Generation != generation {
			return ErrLedgerGeneration
		}
		if err = l.ready(d); err != nil {
			return err
		}
		if group, ok := d.Groups[gid]; !ok || group.Instance != member {
			return ErrLedgerOwner
		}
		return nil
	})
}
func (l *SharedLedger) Rebuild() (string, error) {
	var generation string
	err := l.locked(func() error {
		if _, _, err := l.membership(); err != nil {
			return err
		}
		if old, err := l.root.Read("ledger.json", 1<<20); err == nil {
			id, e := LedgerID()
			if e != nil {
				return e
			}
			if err = l.root.MoveExact("ledger.json", "ledger.bad-"+id+".json", Hash(old)); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		d, err := newLedger(true)
		if err != nil {
			return err
		}
		generation = d.Generation
		return writeLedgerJSON(l.root, "ledger.json", d, true)
	})
	return generation, err
}

// Leave deletes the tombstone last. Confirmed is supplied only by that member's
// owner reconciliation; service removal alone never qualifies.
func (l *SharedLedger) Leave(member, generation string, confirmed bool) error {
	if !confirmed {
		return ErrLedgerOwner
	}
	return l.locked(func() error {
		d, err := l.read()
		if err != nil {
			return err
		}
		if d.Generation != generation {
			return ErrLedgerGeneration
		}
		catalog, members, err := l.membership()
		if err != nil {
			return err
		}
		if catalog.Members[member] == "" {
			return ErrLedgerOwner
		}
		for _, g := range d.Groups {
			if g.Instance == member {
				return ErrLedgerOccupied
			}
		}
		delete(d.Instances, member)
		ready := true
		for id := range catalog.Members {
			if id != member {
				v := d.Instances[id]
				ready = ready && v.Imported && v.Enrolled
			}
		}
		if ready {
			d.Rebuilding = false
		}
		if err = writeLedgerJSON(l.root, "ledger.json", d, false); err != nil {
			return err
		}
		digest := catalog.Members[member]
		delete(catalog.Members, member)
		if err = writeLedgerJSON(l.root, "membership.json", catalog, false); err != nil {
			return err
		}
		// A crash before the final deletion leaves an extra tombstone and blocks.
		for _, m := range members {
			if m.ID == member {
				return l.members.RemoveExact(member+".json", digest)
			}
		}
		return ErrLedgerOwner
	})
}

func (l *SharedLedger) Maximum(member, generation string, maximum int) error {
	if maximum < 0 || maximum > 64 {
		return ErrLedgerUnavailable
	}
	return l.mutate(member, generation, false, func(d *LedgerData) error {
		v := d.Instances[member]
		v.MaximumAgents = maximum
		d.Instances[member] = v
		return nil
	})
}
