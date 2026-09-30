// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
)

type AccountSignals struct {
	ReadingSupport   string `json:"reading_support"`
	QuotaFingerprint string `json:"quota_fingerprint"`
}

// A tenant secret, a vendor namespace and the exact verified account ID are
// required. Emails, local account keys/paths and tokens are never substitutes.
func QuotaFingerprint(key []byte, vendor, id string) string {
	if len(key) != 32 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(id) {
		return ""
	}
	switch vendor {
	case Codex, Claude, Grok, Cursor:
	default:
		return ""
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte("aeon-quota-v1\x00" + vendor + "\x00" + id))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Remote) QuotaKey(ctx context.Context, id string) ([]byte, error) {
	var v struct {
		Key []byte `json:"key"`
	}
	if r.Client.Do(ctx, "POST", "/api/agent-accounts/"+url.PathEscape(id)+"/quota-key", nil, &v) != nil || len(v.Key) != 32 {
		return nil, errors.New("quota key unavailable")
	}
	return v.Key, nil
}
func (r *Remote) PublishAccountSignals(ctx context.Context, id string, v AccountSignals) error {
	return r.Client.Do(ctx, "PUT", "/api/agent-accounts/"+url.PathEscape(id)+"/signals", v, nil)
}
func (a *CodexAdapter) quotaIdentity(key string) string {
	v, _ := a.quotaIDs.Load(key)
	id, _ := v.(string)
	return id
}
func (a *ClaudeAdapter) quotaIdentity(key string) string {
	v, _ := a.quotaIdentities().Load(key)
	id, _ := v.(string)
	return id
}
func (a *GrokAdapter) quotaIdentity(key string) string {
	v, _ := a.quotaIDs.Load(key)
	id, _ := v.(string)
	return id
}
func (a *CursorAdapter) quotaIdentity(key string) string { return a.Identities[key] }

func (s *Supervisor) publishSignals(ctx context.Context) {
	api, ok := s.api.(interface {
		QuotaKey(context.Context, string) ([]byte, error)
		PublishAccountSignals(context.Context, string, AccountSignals) error
	})
	if !ok {
		return
	}
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	s.mu.Unlock()
	for _, a := range accounts {
		if !s.dispatchAllowed(a.ID) {
			continue
		}
		s.mu.Lock()
		adapter := s.adapters[a.Harness]
		verified := s.probedAccounts[a.ID] && !s.blockedAccounts[a.ID]
		s.mu.Unlock()
		if !verified {
			continue
		}
		v := AccountSignals{ReadingSupport: "none"}
		if a.Harness == Codex {
			v.ReadingSupport = "every_5_min"
		}
		if a.Harness == Claude {
			v.ReadingSupport = "first_run"
			if c, ok := adapter.(interface{ CanCaptureCapacity(string) bool }); ok && c.CanCaptureCapacity(a.Key) {
				v.ReadingSupport = "every_5_min"
			}
			s.statuslineMu.Lock()
			if s.statuslineEnabled[a.ID] {
				v.ReadingSupport = "statusline"
			}
			s.statuslineMu.Unlock()
		}
		if a.Harness == Grok {
			if c, ok := adapter.(interface{ CanCaptureCapacity(string) bool }); ok && c.CanCaptureCapacity(a.Key) {
				v.ReadingSupport = "every_5_min"
			}
		}
		if identity, ok := adapter.(interface{ quotaIdentity(string) string }); ok {
			id := identity.quotaIdentity(a.Key)
			if id != "" {
				if len(s.quotaKey) == 0 {
					key, err := api.QuotaKey(ctx, a.ID)
					if err == nil {
						s.quotaKey = key
					}
				}
				v.QuotaFingerprint = QuotaFingerprint(s.quotaKey, a.Harness, id)
			}
		}
		if s.signalsPublished == nil {
			s.signalsPublished = map[string]AccountSignals{}
		}
		if old, ok := s.signalsPublished[a.ID]; ok && old == v {
			continue
		}
		if api.PublishAccountSignals(ctx, a.ID, v) == nil {
			s.signalsPublished[a.ID] = v
		}
	}
}
