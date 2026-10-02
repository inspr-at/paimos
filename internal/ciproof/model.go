// SPDX-License-Identifier: AGPL-3.0-only

// Package ciproof implements the offline, shadow-only CI proof foundation.
// Its records are diagnostics, never execution admission, signatures or checks.
// The caller must supply reviewed policy pins and revalidated event state from
// outside candidate execution. Authenticated resolution/execution belong to B.
package ciproof

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	ObligationSchema = "aeon.ci.obligation.v1"
	PlanSchema       = "aeon.ci.plan.v1"
	ReceiptSchema    = "aeon.ci.receipt.v1"
	LedgerSchema     = "aeon.ci.shadow-ledger.v1"
)

// Hash uses domain-separated, length-delimited bytes. Structured callers use
// fixed structs or sorted slices; maps are encoded with Go's sorted JSON keys.
func Hash(domain string, parts ...[]byte) string {
	h := sha256.New()
	for _, p := range append([][]byte{[]byte("aeon.ci.v1"), []byte(domain)}, parts...) {
		_ = binary.Write(h, binary.BigEndian, uint64(len(p)))
		_, _ = h.Write(p)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func digest(domain string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	} // Only the closed, JSON-safe types below.
	return Hash(domain, b)
}

var objectID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var digestID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Entry struct {
	Path          string `json:"path"`
	Mode          string `json:"mode"`
	Blob          string `json:"blob"`
	ContentDigest string `json:"content_digest"`
}

type Snapshot struct {
	Commit         string  `json:"commit"`
	Tree           string  `json:"tree"`
	ManifestDigest string  `json:"manifest_digest"`
	Entries        []Entry `json:"entries"`
}

// Binding is already-resolved event data, not a GitHub webhook or authorization.
// No moving refs, branch-name inference, PR-file API or candidate declarations.
type Binding struct {
	RepositoryID int64   `json:"repository_id"`
	Event        string  `json:"event"`
	DeliveryID   string  `json:"delivery_id"`
	Generation   int64   `json:"generation"`
	Base         string  `json:"base"`
	Candidate    string  `json:"candidate"`
	CheckTarget  string  `json:"check_target"`
	SourceHead   string  `json:"source_head,omitempty"`
	PR           int64   `json:"pr,omitempty"`
	GroupID      string  `json:"group_id,omitempty"`
	GroupPRs     []int64 `json:"group_prs,omitempty"`
}

func (b Binding) validate() error {
	if b.RepositoryID <= 0 || b.RepositoryID > 9007199254740991 || b.Generation <= 0 || b.Generation > 9007199254740991 || strings.TrimSpace(b.DeliveryID) == "" || len(b.DeliveryID) > 256 {
		return fmt.Errorf("missing repository, delivery or generation binding")
	}
	for _, id := range []string{b.Base, b.Candidate, b.CheckTarget} {
		if !objectID.MatchString(id) {
			return fmt.Errorf("binding requires immutable object IDs")
		}
	}
	switch b.Event {
	case "pull_request":
		if b.PR <= 0 || !objectID.MatchString(b.SourceHead) || (b.CheckTarget != b.SourceHead && b.CheckTarget != b.Candidate) || b.GroupID != "" || len(b.GroupPRs) != 0 {
			return fmt.Errorf("invalid PR binding")
		}
	case "merge_group":
		if b.GroupID == "" || len(b.GroupID) > 256 || len(b.GroupPRs) == 0 || b.PR != 0 || b.SourceHead != "" || b.CheckTarget != b.Candidate {
			return fmt.Errorf("invalid merge group binding")
		}
		for i, pr := range b.GroupPRs {
			if pr <= 0 || (i > 0 && pr <= b.GroupPRs[i-1]) {
				return fmt.Errorf("group PRs must be positive, sorted and unique")
			}
		}
	case "push", "schedule", "workflow_dispatch":
		if b.CheckTarget != b.Candidate || b.PR != 0 || b.SourceHead != "" || b.GroupID != "" || len(b.GroupPRs) != 0 {
			return fmt.Errorf("invalid full-run binding")
		}
	default:
		return fmt.Errorf("unknown event")
	}
	return nil
}

// Pin is admin-owned input. Computing a digest of candidate bytes does not
// approve them; the diagnostic CLI's digest command cannot create a trust pin.
type Pin struct {
	Commit string `json:"commit"`
	Digest string `json:"digest"`
}

type Obligation struct {
	Schema           string `json:"schema"`
	ID               string `json:"id"`
	Context          string `json:"context"`
	Kind             string `json:"kind"`
	Job              string `json:"job"`
	Shard            int    `json:"shard,omitempty"`
	Package          string `json:"package,omitempty"`
	Test             string `json:"test,omitempty"`
	DefinitionDigest string `json:"definition_digest"`
	InputDigest      string `json:"input_digest"`
	Fingerprint      string `json:"fingerprint"`
	Action           string `json:"action"`
	State            string `json:"state"`
}

type Plan struct {
	Schema            string       `json:"schema"`
	ID                string       `json:"id"`
	Mode              string       `json:"mode"`
	Authority         string       `json:"authority"`
	Binding           Binding      `json:"binding"`
	Base              Snapshot     `json:"base"`
	Candidate         Snapshot     `json:"candidate"`
	Policy            Pin          `json:"policy"`
	InventoryDigest   string       `json:"inventory_digest"`
	EnvironmentDigest string       `json:"environment_digest"`
	RequiredContexts  []string     `json:"required_contexts"`
	Obligations       []Obligation `json:"obligations"`
	Reasons           []string     `json:"reasons"`
}

func planID(p Plan) string { p.ID = ""; return digest("plan", p) }

func sortedUnique(s []string) bool {
	return sort.StringsAreSorted(s) && len(s) > 0 && func() bool {
		for i, x := range s {
			if x == "" || (i > 0 && x == s[i-1]) {
				return false
			}
		}
		return true
	}()
}
