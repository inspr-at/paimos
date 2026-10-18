// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/httpapi"
)

const (
	// DraftHeader carries the capability on the assistant's API calls. A header
	// stays out of request lines, so no server or proxy access log records it.
	DraftHeader = "Aeon-Import-Draft"

	draftTTL       = 30 * time.Minute
	createdTTL     = 24 * time.Hour
	handoffsPerMin = 20
	maxLiveDrafts  = 200
	maxLiveBytes   = 64 * MaxPayloadBytes
)

var capabilityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Options configure the module. TenantID is the one import tenant the
// instance configures; empty turns the handoff off. MacKey keys the limiter
// buckets (the server session key); TrustedProxies may name the client.
type Options struct {
	Pool           *pgxpool.Pool
	TenantID       string
	Store          attachments.Store
	MacKey         []byte
	TrustedProxies []netip.Prefix
}

// Module serves the public handoff and the assistant's draft API.
type Module struct {
	pool    *pgxpool.Pool
	tenant  string
	store   attachments.Store
	macKey  []byte
	trusted []netip.Prefix
	// Bounds; tests lower them instead of filling the table.
	perMinute, liveDrafts int
	liveBytes             int64
}

var _ httpapi.Module = (*Module)(nil)
var _ httpapi.PublicModule = (*Module)(nil)

func New(o Options) *Module {
	key := append([]byte(nil), o.MacKey...)
	return &Module{pool: o.Pool, tenant: o.TenantID, store: o.Store, macKey: key, trusted: append([]netip.Prefix(nil), o.TrustedProxies...),
		perMinute: handoffsPerMin, liveDrafts: maxLiveDrafts, liveBytes: maxLiveBytes}
}

// Mount adds the assistant's routes; both need a signed-in person.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/import/project-draft", m.read)
	mux.HandleFunc("POST /api/import/project-draft/create", m.create)
}

// MountPublic adds the handoff START posts to. It runs outside the API
// middleware, so no session cookie is read, renewed or trusted there.
func (m *Module) MountPublic(mux *http.ServeMux) {
	mux.HandleFunc("POST /import/project-draft", m.handoff)
}

// newCapability returns 256 random bits for the person's address and the
// digest that is stored in their place.
func newCapability() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), digest[:], nil
}

// capabilityDigest validates the shape before any database work.
func capabilityDigest(value string) ([]byte, bool) {
	if !capabilityPattern.MatchString(value) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	digest := sha256.Sum256(raw)
	return digest[:], true
}

// bucketKey is an HMAC under the session key, as the portal limiter does: a
// database copy cannot recover the address, and the key is never logged.
func (m *Module) bucketKey(ip string) (string, error) {
	if len(m.macKey) < 32 {
		return "", errors.New("handoff limiter key unavailable")
	}
	mac := hmac.New(sha256.New, m.macKey)
	mac.Write([]byte("start-handoff-limit"))
	mac.Write([]byte{0})
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// clientIP is the transport peer, unless that peer is a trusted proxy: then
// X-Forwarded-For is read from the right, skipping trusted hops, and the
// first other address is the client. Anything unparsable stops the walk.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	if !trustedAddr(peer, trusted) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	if len(hops) > 32 {
		hops = hops[len(hops)-32:]
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = addr.Unmap()
		if !trustedAddr(addr, trusted) {
			return addr.String()
		}
		peer = addr
	}
	return peer.String()
}

func trustedAddr(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
