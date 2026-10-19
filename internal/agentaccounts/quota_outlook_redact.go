// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
)

// redactOutlook applies the privacy policy seen at response time. The handler
// may have built the outlook under an older policy. A denied account that
// contributed to the pool, the burn or the projection loses those joint
// results: they cannot be rebuilt exactly without the readings, and leaving
// them would keep a private quota recoverable. The pool mean of whoever is
// still pooled is exact. A no-op returns the original bytes.
func redactOutlook(body []byte, policy accountprivacy.Policy) ([]byte, error) {
	var out quotaOutlook
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if !outlookDisclosesDenied(out, policy) {
		return body, nil
	}
	for i := range out.Groups {
		redactOutlookGroup(&out.Groups[i], policy, out.GeneratedAt)
	}
	return json.Marshal(out)
}

func outlookDisclosed(a outlookAccount) bool {
	return a.Pooled || a.Forecast != nil || a.UsedPercent != nil || a.ReadAt != nil || a.LimitPercent != nil
}

func outlookDisclosesDenied(out quotaOutlook, policy accountprivacy.Policy) bool {
	for _, g := range out.Groups {
		for _, a := range g.Accounts {
			if !policy[a.AccountID] && outlookDisclosed(a) {
				return true
			}
		}
	}
	return false
}

// outlookContributed is true when this row's quota is inside a joint figure,
// not only on its own row. A held account that never entered the pool does
// not take the pool down with it.
func outlookContributed(g outlookGroup, index int, id string) bool {
	a := g.Accounts[index]
	if a.Pooled || a.Forecast != nil {
		return true
	}
	if g.Headline.AccountID == id && g.Headline.Kind != "" && g.Headline.Kind != "not_reported" {
		return true
	}
	for _, h := range g.History {
		if index < len(h.Burn) && h.Burn[index] != 0 {
			return true
		}
	}
	for _, h := range g.Projected {
		if index < len(h.Burn) && h.Burn[index] != 0 {
			return true
		}
	}
	for _, r := range g.PastRefills {
		if r.AccountID == id {
			return true
		}
	}
	if g.Current != nil {
		for _, r := range g.Current.Refills {
			if r.AccountID == id {
				return true
			}
		}
	}
	if g.Safe != nil {
		for _, r := range g.Safe.Refills {
			if r.AccountID == id {
				return true
			}
		}
	}
	return false
}

func privateOutlookAccount(a *outlookAccount) {
	a.State = "private"
	a.Freshness = "unknown"
	a.Pooled = false
	a.UsedPercent = nil
	a.LimitPercent = nil
	a.ResetsAt = nil
	a.ReadAt = nil
	a.ReadingAgeSeconds = nil
	a.Forecast = nil
}

func redactOutlookGroup(g *outlookGroup, policy accountprivacy.Policy, generatedAt time.Time) {
	type mark struct {
		index       int
		id          string
		contributed bool
	}
	var denied []mark
	for i, a := range g.Accounts {
		if policy[a.AccountID] || !outlookDisclosed(a) {
			continue
		}
		denied = append(denied, mark{i, a.AccountID, outlookContributed(*g, i, a.AccountID)})
	}
	if len(denied) == 0 {
		return
	}
	withhold := false
	deniedAt := map[int]bool{}
	deniedID := map[string]bool{}
	for _, m := range denied {
		if m.contributed {
			withhold = true
		}
		deniedAt[m.index] = true
		deniedID[m.id] = true
		privateOutlookAccount(&g.Accounts[m.index])
	}
	if withhold {
		for hi := range g.History {
			touched := false
			for i := range g.History[hi].Burn {
				if deniedAt[i] {
					g.History[hi].Burn[i] = 0
					touched = true
				}
			}
			// empty=true would say the private account was at its limit.
			if touched {
				g.History[hi].Empty = false
			}
		}
		kept := make([]outlookPastRefill, 0)
		for _, r := range g.PastRefills {
			if !deniedID[r.AccountID] {
				kept = append(kept, r)
			}
		}
		g.PastRefills = kept
		g.PastLine = []outlookPoint{}
		g.Current = nil
		g.Safe = nil
		g.Projected = []outlookHour{}
		g.Pace = outlookPace{}
		g.Verdict = "unknown"
		g.Headline = outlookHeadline{Kind: "unknown"}
		for i := range g.Accounts {
			if deniedAt[i] {
				continue
			}
			if g.Accounts[i].State == "active" {
				g.Accounts[i].State = "ready"
				if g.Accounts[i].UsedPercent != nil && g.Accounts[i].LimitPercent != nil && *g.Accounts[i].UsedPercent >= *g.Accounts[i].LimitPercent-1e-9 {
					g.Accounts[i].State = "full"
				}
			}
			g.Accounts[i].Forecast = nil
		}
		g.Pool = rebuiltOutlookPool(g.Accounts, generatedAt)
	}
	anyHeld, anyReady := false, false
	for _, a := range g.Accounts {
		switch a.State {
		case "held":
			anyHeld = true
		case "ready", "full", "active":
			anyReady = true
		}
	}
	switch {
	case anyHeld && !anyReady:
		g.State = "held"
		g.Headline = outlookHeadline{Kind: "held"}
		g.Pool = nil
	case !anyReady && !anyHeld:
		g.State = "not_reported"
		g.Headline = outlookHeadline{Kind: "not_reported"}
		g.History = []outlookHour{}
		g.Pool = nil
	default:
		g.State = "measured"
	}
}

func rebuiltOutlookPool(accounts []outlookAccount, generatedAt time.Time) *outlookPool {
	// buildGroup puts the merged supply measurement on each pooled row;
	// enrollment-local readings on non-carrying aliases never enter this mean.
	left, n := 0.0, 0
	sampled := time.Time{}
	for _, a := range accounts {
		if !a.Pooled || a.UsedPercent == nil {
			continue
		}
		left += 100 - *a.UsedPercent
		n++
		if a.ReadAt != nil && a.ReadAt.After(sampled) {
			sampled = *a.ReadAt
		}
	}
	if n == 0 {
		return nil
	}
	left /= float64(n)
	age := int64(generatedAt.Sub(sampled) / time.Second)
	if age < 0 {
		age = 0
	}
	return &outlookPool{Accounts: n, UsedPercent: round1(100 - left), LeftPercent: round1(left), SampledAt: sampled, SampledAgeSec: age}
}
