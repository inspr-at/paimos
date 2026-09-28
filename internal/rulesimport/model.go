// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"context"
	"errors"
)

const (
	// AlwaysOnBudget is the UTF-8 byte ceiling for the always-on projection.
	// Packs over this size stay on demand. An always-on projection past the
	// ceiling is refused whole; rule text is not trimmed to fit.
	AlwaysOnBudget = 12000

	// MaxFileBytes is the largest doctrine file this importer will read.
	MaxFileBytes = 256 * 1024
)

// Layer is one of the four ADR-004 layers, highest precedence first.
type Layer string

const (
	LayerCompany Layer = "company"
	LayerProject Layer = "project"
	LayerPerson  Layer = "person"
	LayerAgent   Layer = "agent"
)

// TrustContext is the tenant trust boundary for one plan.
// Personal, private and public-template doctrine never share a plan.
type TrustContext string

const (
	ContextTemplate TrustContext = "template"
	ContextPrivate  TrustContext = "private"
	ContextProject  TrustContext = "project"
	ContextPerson   TrustContext = "person"
)

const (
	SectionAll      = "all"
	SectionPersonal = "personal"
	SectionKernel   = "kernel"
)

const (
	PlacementAlwaysOn = "always_on"
	PlacementOnDemand = "on_demand"
)

const (
	StrengthNormal = "normal"
	StrengthLocked = "locked"
)

var (
	ErrProhibitedPath   = errors.New("prohibited path")
	ErrSymlink          = errors.New("symlink refused")
	ErrByteBound        = errors.New("file exceeds byte bound")
	ErrMixedContext     = errors.New("mixed trust context")
	ErrNotText          = errors.New("file is not utf-8 text")
	ErrNotRegular       = errors.New("not a regular file")
	ErrUnrecognizedFile = errors.New("unrecognized doctrine file")
	ErrDraftUnavailable = errors.New("draft write unavailable")
	ErrPublishRefused   = errors.New("publication refused")
)

// OwnerPolicy is the direct owner instruction that wins over model and review
// names copied into source doctrine. It is a report label, not a company rule write.
const OwnerPolicy = "Direct owner policy of 2026-09-28 wins over stale model and review names in source documents. Reviewer selection stays in the model registry. This importer reports those names and does not import them as rules."

// Request is one explicit import. Paths are the only inputs; nothing is discovered.
type Request struct {
	Context  TrustContext
	Section  string
	Files    []string
	AR1Draft string
}

// SourceFile is the stable identity of one supplied file.
type SourceFile struct {
	Path      string       `json:"path"`
	Base      string       `json:"base"`
	SHA256    string       `json:"sha256"`
	Layer     Layer        `json:"layer"`
	Kind      string       `json:"kind"`
	Trust     TrustContext `json:"trust"`
	Placement string       `json:"placement"`
	Role      string       `json:"role,omitempty"`
	Bytes     int          `json:"bytes"`
}

// SourceRef is the lineage of one rule inside a file.
type SourceRef struct {
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	SHA256     string `json:"sha256"`
	FileSHA256 string `json:"file_sha256"`
}

// Rule is one proposed rule. Identity is authoritative. ID is the replay key.
type Rule struct {
	ID               string      `json:"id"`
	Identity         string      `json:"identity"`
	Layer            Layer       `json:"layer"`
	Set              string      `json:"set"`
	SetTitle         string      `json:"set_title"`
	Text             string      `json:"text"`
	Why              string      `json:"why,omitempty"`
	Details          string      `json:"details,omitempty"`
	Strength         string      `json:"strength"`
	Enabled          bool        `json:"enabled"`
	Expires          string      `json:"expires,omitempty"`
	Source           string      `json:"source,omitempty"`
	Roles            []string    `json:"roles,omitempty"`
	Harnesses        []string    `json:"harnesses,omitempty"`
	Placement        string      `json:"placement"`
	AlwaysOnEligible bool        `json:"always_on_eligible"`
	Sources          []SourceRef `json:"sources"`
	ExplicitID       string      `json:"explicit_id,omitempty"`
}

// Contradiction records same-identity differences. Both rules stay in the proposal.
type Contradiction struct {
	Identity string   `json:"identity"`
	Kind     string   `json:"kind"`
	RuleIDs  []string `json:"rule_ids"`
	Fields   []string `json:"fields"`
	Note     string   `json:"note"`
}

// Duplicate records an identical replay. Every source lineage is kept.
type Duplicate struct {
	Identity string `json:"identity"`
	RuleID   string `json:"rule_id"`
	Sources  int    `json:"sources"`
	Note     string `json:"note"`
}

// HeuristicMatch is a normalized-text suggestion. It is never precedence.
type HeuristicMatch struct {
	Authoritative bool     `json:"authoritative"`
	Kind          string   `json:"kind"`
	RuleIDs       []string `json:"rule_ids"`
	Note          string   `json:"note"`
}

// Unresolved is a choice or exclusion the importer did not guess through.
type Unresolved struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	Note string `json:"note"`
	Text string `json:"text,omitempty"`
}

// AlwaysOnReport is the would-be always-on file. Insert is false when it does
// not fit; bytes still describe the untrimmed projection.
type AlwaysOnReport struct {
	Bytes       int    `json:"bytes"`
	Budget      int    `json:"budget"`
	Insert      bool   `json:"insert"`
	Explanation string `json:"explanation"`
}

// AdapterGap is the unimplemented AR1 draft mapping. Ready stays false until
// a frozen DTO is actually mapped; this package does not invent endpoints.
type AdapterGap struct {
	Ready      bool     `json:"ready"`
	Reason     string   `json:"reason"`
	Expected   []string `json:"expected"`
	DraftSHA   string   `json:"draft_sha256,omitempty"`
	DraftBytes int      `json:"draft_bytes,omitempty"`
}

// Proposal is a local preview. Mode is preview until an authorized draft port accepts it.
type Proposal struct {
	PlanID         string           `json:"plan_id"`
	Context        TrustContext     `json:"context"`
	Section        string           `json:"section"`
	Mode           string           `json:"mode"`
	OwnerPolicy    string           `json:"owner_policy"`
	Files          []SourceFile     `json:"files"`
	Rules          []Rule           `json:"rules"`
	Contradictions []Contradiction  `json:"contradictions"`
	Duplicates     []Duplicate      `json:"duplicates"`
	Heuristics     []HeuristicMatch `json:"heuristics"`
	Unresolved     []Unresolved     `json:"unresolved"`
	AlwaysOn       AlwaysOnReport   `json:"always_on"`
	Adapter        AdapterGap       `json:"adapter"`
}

// DraftRequest is the only write this package can ask for. Publish is refused.
type DraftRequest struct {
	Tenant     string
	Context    TrustContext
	Version    string
	Publish    bool
	Authorized bool
	Proposal   Proposal
}

// DraftWriter is the AR1 draft port. The CLI does not supply one.
type DraftWriter interface {
	WriteDraft(ctx context.Context, req DraftRequest) error
}
