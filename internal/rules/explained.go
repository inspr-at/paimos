// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Explained is one session file as people read it (AEON-314): every served
// rule with the set and layer it comes from and its explanation, the bytes each
// layer takes against the workspace budget, and the exact file. It is never
// sent to agents and records no served manifest: agents receive only Merged.
type Explained struct {
	Context  Context         `json:"context"`
	Version  string          `json:"version"`
	SHA256   string          `json:"sha256"`
	Body     string          `json:"body"`
	ByteSize int             `json:"byte_size"`
	Budget   Budget          `json:"budget"`
	Usage    LayerBytes      `json:"usage"`
	Sets     []ExplainedSet  `json:"sets"`
	Rules    []ExplainedRule `json:"rules"`
	// Problem is what session start would refuse with now: the budget or a
	// missing company floor. The file is still shown so people can fix it.
	Problem *Error `json:"problem,omitempty"`
}

// ExplainedSet is one set that contributes rules to the file, in precedence order.
type ExplainedSet struct {
	SetID   string `json:"set_id"`
	Name    string `json:"name"`
	Scope   Scope  `json:"scope"`
	Version string `json:"version"`
	TLDR    *TLDR  `json:"tldr,omitempty"`
	Bytes   int    `json:"bytes"`
}

// ExplainedRule is one line of the file next to its explanation.
type ExplainedRule struct {
	Identity string `json:"identity"`
	Text     string `json:"text"`
	// Line is the exact line in the file, without its newline.
	Line     string `json:"line"`
	SetID    string `json:"set_id"`
	Layer    string `json:"layer"`
	Strength string `json:"strength"`
	TLDR     *TLDR  `json:"tldr,omitempty"`
	Bytes    int    `json:"bytes"`
}

// Explain renders the file for c and explains it under budget b.
func Explain(c Context, snapshots []Snapshot, now time.Time, b Budget) (Explained, error) {
	r, err := render(c, snapshots, now, false, nil)
	if err != nil {
		return Explained{}, err
	}
	out := Explained{Context: c, Version: r.Version, SHA256: r.SHA256, Body: r.Body, ByteSize: r.ByteSize, Budget: b, Usage: r.usage, Sets: []ExplainedSet{}, Rules: []ExplainedRule{}}
	var e *Error
	if err = b.enforce(r.Merged, r.usage); errors.As(err, &e) {
		out.Problem = e
	} else if r.Floor == "" {
		out.Problem = &Error{Status: 409, Code: "floor_missing", Message: "publish an applicable locked company safety floor before using session rules"}
	}
	sets := map[string]int{}
	for _, served := range r.Rules {
		s := r.from[served.Identity]
		var tldr *TLDR
		for _, original := range s.Rules {
			if original.Identity == served.Identity {
				tldr = checked(original.TLDR, RuleBasis(original))
				break
			}
		}
		line := ruleLine(served)
		out.Rules = append(out.Rules, ExplainedRule{Identity: served.Identity, Text: served.Text, Line: strings.TrimSuffix(line, "\n"), SetID: s.SetID, Layer: s.Scope.Layer, Strength: served.Strength, TLDR: tldr, Bytes: len(line)})
		i, seen := sets[s.SetID]
		if !seen {
			i = len(out.Sets)
			sets[s.SetID] = i
			out.Sets = append(out.Sets, ExplainedSet{SetID: s.SetID, Name: s.Name, Scope: s.Scope, Version: s.Version, TLDR: checked(s.TLDR, SetBasis(s.Rules))})
		}
		out.Sets[i].Bytes += len(line)
	}
	slices.SortStableFunc(out.Sets, func(a, b ExplainedSet) int {
		if d := a.Scope.rank() - b.Scope.rank(); d != 0 {
			return d
		}
		return strings.Compare(a.SetID, b.SetID)
	})
	return out, nil
}

// explained answers GET /api/rules/explained with the same selectors and
// authorization as the merged file.
func (m *Module) explained(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	c, snapshots, err := m.mergeInputs(r, tx, p)
	if err != nil {
		return nil, err
	}
	limits, err := LoadBudget(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	return Explain(c, snapshots, time.Now().UTC(), limits)
}
