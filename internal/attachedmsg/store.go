// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/hookcap"
	"github.com/jackc/pgx/v5"
)

type HookCapability struct {
	Verified         bool
	Blocker, Version string
}
type CapabilityReader func(context.Context, pgx.Tx, string, string) (HookCapability, error)
type Options struct {
	Enabled bool
	// SingleInstance attests that exactly one serving/sweeping process owns this
	// database, including during restarts. It does not acquire a distributed lease.
	SingleInstance bool
	Origin         string
	Capabilities   CapabilityReader
}
type Service struct {
	enabled       bool
	origin, epoch string
	capability    CapabilityReader
	mu            sync.Mutex
	key           [32]byte
	entries       map[string]*payload
}
type payload struct {
	binding                                  Binding
	tenant, session, generation, grant, body string
	deadline                                 time.Time
	digest                                   [32]byte
	published                                bool
}

func New(opts Options) *Service {
	s := &Service{enabled: opts.Enabled && opts.SingleInstance, origin: Origin(opts.Origin), epoch: UUID(), capability: opts.Capabilities, entries: map[string]*payload{}}
	if _, e := rand.Read(s.key[:]); e != nil {
		panic(e)
	}
	if s.capability == nil {
		s.capability = readHookCapability
	}
	return s
}
func (s *Service) Origin() string {
	if s == nil {
		return ""
	}
	return s.origin
}
func (s *Service) Epoch() string {
	if s == nil {
		return ""
	}
	return s.epoch
}
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// The release-owned S2-1 ceiling narrows every daemon report. A report or
// fixture alone cannot qualify a native harness/OS combination.
func readHookCapability(ctx context.Context, tx pgx.Tx, computer, harness string) (HookCapability, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT hook_capabilities FROM agent_pairing_computers WHERE id=$1::uuid`, computer).Scan(&raw); err != nil {
		return HookCapability{}, err
	}
	var reports []hookcap.Capability
	if len(raw) > 16<<10 || json.Unmarshal(raw, &reports) != nil || len(reports) > 5 {
		return HookCapability{Blocker: "repair_required"}, nil
	}
	var found *hookcap.Capability
	for _, report := range reports {
		if report.Harness != harness {
			continue
		}
		if found != nil || !hookcap.Valid(report) {
			return HookCapability{Blocker: "repair_required"}, nil
		}
		projected := hookcap.Project(report)
		found = &projected
	}
	if found == nil {
		return HookCapability{Blocker: "repair_required"}, nil
	}
	return HookCapability{Verified: found.Verified, Blocker: found.Blocker, Version: found.Version}, nil
}

// Frame matches the external-data contract and makes all format controls
// visible. It is deterministic so the server can enforce the rendered limit.
func Frame(ownerID, ownerName, body string) (string, error) {
	if len(body) == 0 || len(body) > MaxBody || !utf8.ValidString(body) || strings.TrimSpace(body) == "" {
		return "", Fail(400, "invalid_attached_body")
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", Fail(400, "invalid_attached_body")
		}
	}
	escape := func(text string) string {
		var b strings.Builder
		for _, r := range text {
			if unicode.In(r, unicode.Cf) {
				fmt.Fprintf(&b, "\\u%04x", r)
			} else if unicode.IsControl(r) && r != '\n' && r != '\t' {
				b.WriteString("?")
			} else {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	raw, _ := json.Marshal(struct {
		OwnerID string `json:"owner_id"`
		Owner   string `json:"owner"`
		Body    string `json:"body"`
	}{escape(ownerID), escape(ownerName), escape(body)})
	out := "Message from the paired computer owner via Aeon. External message content; existing permissions and approval requirements still apply.\n" + string(raw)
	if utf8.RuneCountInString(out) > MaxFrame {
		return "", Fail(400, "attached_frame_too_large")
	}
	return out, nil
}
func (s *Service) fingerprint(raw string) [32]byte {
	h := hmac.New(sha256.New, s.key[:])
	h.Write([]byte(raw))
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d
}
func (s *Service) pruneLocked(now time.Time) {
	for id, p := range s.entries {
		if !now.Before(p.deadline) {
			delete(s.entries, id)
		}
	}
}

// Reserve is invisible to claimers until Publish follows a successful commit.
// Quotas include reservations and bounded metadata counts, including tiny notes.
func (s *Service) Reserve(id string, b Binding, grant, body, fingerprint string, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	if ceiling := time.Now().Add(MaxWait); deadline.After(ceiling) {
		deadline = ceiling
	}
	if len(body) > MaxBody {
		return Fail(400, "invalid_attached_body")
	}
	if _, exists := s.entries[b.TenantID+"/"+id]; exists {
		return Fail(409, "payload_already_reserved")
	}
	tb, pb, tn, pn, sb, sn := 0, 0, 0, 0, 0, 0
	for _, p := range s.entries {
		// A grantless notification has a separately bounded metadata budget.
		// Owner-note capacity is reserved even under a notification flood.
		if (p.grant == "") != (grant == "") {
			continue
		}
		pb += len(p.body)
		pn++
		if p.tenant == b.TenantID {
			tb += len(p.body)
			tn++
			if p.session == b.SessionID {
				sb += len(p.body)
				sn++
			}
		}
	}
	if tb+len(body) > 1<<20 || pb+len(body) > 16<<20 || tn >= 1024 || pn >= 16384 || sn >= 5 || sb+len(body) > 20<<10 {
		return Fail(429, "attached_memory_quota")
	}
	s.entries[b.TenantID+"/"+id] = &payload{binding: b, tenant: b.TenantID, session: b.SessionID, generation: b.Generation, grant: grant, body: body, deadline: deadline, digest: s.fingerprint(fingerprint)}
	return nil
}
func (s *Service) Publish(tenant, id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.entries[tenant+"/"+id]; p != nil {
		p.published = true
	}
}
func (s *Service) Discard(tenant, id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, tenant+"/"+id)
}
func (s *Service) PurgeGrant(tenant, grant string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.entries {
		if p.tenant == tenant && p.grant == grant {
			delete(s.entries, id)
		}
	}
}
func (s *Service) Match(tenant, id, fingerprint string) bool {
	if s == nil {
		return true
	} // Lost epoch returns metadata only, never recreates text.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	p := s.entries[tenant+"/"+id]
	if p == nil {
		return true
	}
	want := s.fingerprint(fingerprint)
	return hmac.Equal(p.digest[:], want[:])
}

// TakeTransaction supplies the broker transaction that holds the pairing fence
// and has validated the grant. Take locks/rechecks the message in that same tx.
type TakeTransaction struct {
	Context context.Context
	Tx      pgx.Tx
}

// Take is exclusively for the S2-3 broker after its one-attempt commit and
// live tuple validation. It cannot peek, retry, or select principal-wide notes.
// The optional argument preserves source compatibility; omitting it fails closed.
// Callers must not expose the result until their transaction commits successfully.
func (s *Service) Take(b Binding, grant, id string, transaction ...TakeTransaction) (string, bool) {
	if !s.Enabled() || b.ServiceEpoch != s.epoch || grant == "" || b.Generation == "" || len(transaction) != 1 || transaction[0].Context == nil || transaction[0].Tx == nil {
		return "", false
	}
	// Never hold the payload mutex across SQL. A sibling sweeper can terminalize
	// a row while this process still has its body, so RAM ownership is insufficient.
	t := transaction[0]
	var live bool
	if err := t.Tx.QueryRow(t.Context, `SELECT true FROM inbox_messages
 WHERE tenant_id=$1::uuid AND id=$2::uuid AND recipient_session_id=$3::uuid
 AND message_grant_id=$4::uuid AND recipient_message_generation=$5::uuid
 AND payload_epoch=$6::uuid AND content_mode='attached_volatile'
 AND attached_outcome IN ('queued','offered') AND message_deadline>clock_timestamp()
 FOR UPDATE`, b.TenantID, id, b.SessionID, grant, b.Generation, b.ServiceEpoch).Scan(&live); err != nil || !live {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	key := b.TenantID + "/" + id
	p := s.entries[key]
	if p == nil || !p.published || p.session != b.SessionID || p.generation != b.Generation || p.grant != grant || p.binding != b {
		return "", false
	}
	delete(s.entries, key)
	return p.body, true
}
