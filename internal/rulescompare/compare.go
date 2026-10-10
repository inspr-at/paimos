// SPDX-License-Identifier: AGPL-3.0-only

package rulescompare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"
	"github.com/inspr-at/paimos/internal/workorders"
)

const Schema = "aeon.instruction-comparison.v1"

// MaxFiles bounds one explicit invocation. Nothing is discovered beyond this list.
const MaxFiles = 32

// Input is one explicit comparison. Nil merged or provenance bytes mean that
// document was not supplied. They are never inferred from names.
type Input struct {
	Context           rulesimport.TrustContext
	Section           string
	Files             []string
	Merged            []byte
	Provenance        []byte
	ExpectedSessionID string
}

// Limits are constant facts about this report. They do not change when hashes agree.
type Limits struct {
	SuppliedFilesAreExpectedInputs   bool `json:"supplied_files_are_expected_inputs"`
	ReceiptProvesRecordedBytesOnly   bool `json:"receipt_proves_recorded_bytes_only"`
	SuppliedMetadataIsNotServerProof bool `json:"supplied_metadata_is_not_server_proof"`
	NameMatchIsNotComparison         bool `json:"name_match_is_not_comparison"`
	NormalizedHashIsNotRawBytes      bool `json:"normalized_hash_is_not_raw_bytes"`
	ModelLoadVerified                bool `json:"model_load_verified"`
	ModelObedienceVerified           bool `json:"model_obedience_verified"`
	ExecutionVerified                bool `json:"execution_verified"`
	WaitingWindow                    bool `json:"waiting_window"`
	ActiveInstructionReplaced        bool `json:"active_instruction_replaced"`
	RolloutAuthorized                bool `json:"rollout_authorized"`
}

// Report is the one-time check. It contains hashes, keys and statuses, not instruction prose.
type Report struct {
	Schema                 string       `json:"schema"`
	Limits                 Limits       `json:"limits"`
	SuppliedTrustContext   string       `json:"supplied_trust_context"`
	Section                string       `json:"section"`
	PublishedMergeCompared bool         `json:"published_merge_compared"`
	SuppliedMergeCompared  bool         `json:"supplied_merge_compared"`
	Blockers               []string     `json:"blockers"`
	MissingRuntimeEvidence []Gap        `json:"missing_runtime_evidence"`
	SuppliedInputs         []Supplied   `json:"supplied_inputs"`
	Merge                  *MergeView   `json:"merge"`
	Receipts               []Receipt    `json:"receipts"`
	UnverifiedComparisons  []Receipt    `json:"unverified_comparisons"`
	Lineage                LineageView  `json:"lineage"`
	Differences            []Difference `json:"differences"`
	Unresolved             []Item       `json:"unresolved"`
	Counts                 Counts       `json:"counts"`
	NextInvocation         string       `json:"next_invocation"`
}

// Gap is evidence this command cannot produce.
type Gap struct {
	Kind string `json:"kind"`
	Note string `json:"note"`
}

// Supplied is one operator-named file after the descriptor-safe read.
type Supplied struct {
	Base               string `json:"base"`
	Kind               string `json:"kind"`
	Layer              string `json:"layer"`
	Trust              string `json:"trust"`
	RawSHA256          string `json:"raw_sha256"`
	Bytes              int    `json:"bytes"`
	HarnessLogicalName string `json:"harness_logical_name,omitempty"`
	RawReceipt         bool   `json:"raw_receipt"`
}

// MergeView is the AR1 merge identity. Body and floor text are not copied.
type MergeView struct {
	Context                   rules.Context      `json:"context"`
	Harness                   string             `json:"harness"`
	Role                      string             `json:"role"`
	Version                   string             `json:"version"`
	Versions                  []rules.VersionRef `json:"versions"`
	StatedBodySHA256          string             `json:"stated_body_sha256"`
	ComputedBodySHA256        string             `json:"computed_body_sha256"`
	ByteSize                  int                `json:"byte_size"`
	ComputedByteSize          int                `json:"computed_byte_size"`
	IntegrityOK               bool               `json:"integrity_ok"`
	FloorPresent              bool               `json:"floor_present"`
	FloorInBody               bool               `json:"floor_in_body"`
	Published                 bool               `json:"published"`
	MetadataCurrentAndValid   bool               `json:"metadata_current_and_valid"`
	SnapshotDigestsReverified bool               `json:"snapshot_digests_reverified"`
}

// Receipt compares one item in supplied, session-bound revision metadata.
type Receipt struct {
	Evidence           string   `json:"evidence"`
	LogicalName        string   `json:"logical_name"`
	Kind               string   `json:"kind"`
	HashKind           string   `json:"hash_kind"`
	ContentSHA256      string   `json:"content_sha256,omitempty"`
	ByteSize           *int64   `json:"byte_size,omitempty"`
	Version            string   `json:"version,omitempty"`
	RawBytesReceived   bool     `json:"raw_bytes_received"`
	RawDigestMatches   bool     `json:"raw_digest_matches_supplied"`
	MergedBodyReceived bool     `json:"merged_body_received"`
	NormalizedHashOnly bool     `json:"normalized_hash_only"`
	NameWithoutRawHash bool     `json:"name_without_raw_hash"`
	SizeAgrees         *bool    `json:"size_agrees,omitempty"`
	SuppliedBases      []string `json:"supplied_bases,omitempty"`
	Note               string   `json:"note"`
}

// LineageView is source linkage from the explicit files. Range hashes are not file receipts.
type LineageView struct {
	Rules      []LineageRule `json:"rules"`
	Heuristics int           `json:"heuristics"`
	Note       string        `json:"note"`
}

// LineageRule is one proposed identity and its raw file hashes.
type LineageRule struct {
	ProposalIdentity string          `json:"proposal_identity"`
	ImportIdentity   string          `json:"import_identity"`
	TextSHA256       string          `json:"text_sha256,omitempty"`
	Sources          []LineageSource `json:"sources"`
	AgainstMerge     *AgainstMerge   `json:"against_merge,omitempty"`
}

// LineageSource identifies normalized parsing lines. FileSHA256 is the raw file.
type LineageSource struct {
	Base                 string `json:"base"`
	StartLine            int    `json:"start_line"`
	EndLine              int    `json:"end_line"`
	NormalizedLineSHA256 string `json:"normalized_line_sha256"`
	FileSHA256           string `json:"file_sha256"`
	NotFileReceipt       bool   `json:"not_file_receipt"`
}

// AgainstMerge is present only when an intact merged document was compared.
type AgainstMerge struct {
	MergedIdentity       string `json:"merged_identity,omitempty"`
	IdentityKeyMatch     bool   `json:"identity_key_match"`
	RawLineageLinked     bool   `json:"raw_lineage_linked"`
	TextSHA256Agrees     bool   `json:"text_sha256_agrees"`
	NotExecutionEvidence bool   `json:"not_execution_evidence"`
}

// Difference is a disagreement between supplied lineage and the merged rules.
type Difference struct {
	Kind               string `json:"kind"`
	Identity           string `json:"identity,omitempty"`
	SuppliedTextSHA256 string `json:"supplied_text_sha256,omitempty"`
	MergedTextSHA256   string `json:"merged_text_sha256,omitempty"`
}

// Item is an unresolved choice or a comparison that must not be treated as success.
type Item struct {
	Kind string `json:"kind"`
	Base string `json:"base,omitempty"`
	Line int    `json:"line,omitempty"`
	Note string `json:"note"`
}

// Counts summarize the rows. They are not a pass mark.
type Counts struct {
	SuppliedFiles   int `json:"supplied_files"`
	RawReceipts     int `json:"raw_receipts"`
	NameOnly        int `json:"name_only"`
	NormalizedOnly  int `json:"normalized_only"`
	RawLineageLinks int `json:"raw_lineage_links"`
	TextDifferences int `json:"text_differences"`
	Unresolved      int `json:"unresolved"`
}

type provMeta struct {
	supplied  bool
	recorded  bool
	sessionID string
	kind      string
	items     []harness.ProvenanceItem
	revisions int
	truncated bool
	setSHA    string
}

type mergeMeta struct {
	supplied bool
	merged   rules.Merged
	computed string
	view     MergeView
}

type groupedRule struct {
	identity      string
	importID      string
	textSHA       map[string]struct{}
	fileSHA       map[string]struct{}
	sources       []LineageSource
	contradictory bool
}

// Compare reads the named doctrine files and writes the report. It does not
// contact an API, sleep, or write instruction files.
func Compare(ctx context.Context, in Input) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if len(in.Files) == 0 {
		return Report{}, errors.New("at least one explicit file is required")
	}
	if len(in.Files) > MaxFiles {
		return Report{}, fmt.Errorf("at most %d explicit files", MaxFiles)
	}
	section := in.Section
	if section == "" {
		section = rulesimport.SectionAll
	}
	proposal, err := rulesimport.Build(ctx, rulesimport.Request{Context: in.Context, Section: section, Files: in.Files})
	if err != nil {
		return Report{}, err
	}
	report := newReport(in.Context, section, in.Files)
	for _, file := range proposal.Files {
		item := Supplied{
			Base: file.Base, Kind: file.Kind, Layer: string(file.Layer), Trust: string(file.Trust),
			RawSHA256: file.SHA256, Bytes: file.Bytes,
		}
		if logical, ok := harnessLogical(file.Base); ok {
			item.HarnessLogicalName = logical
		}
		report.SuppliedInputs = append(report.SuppliedInputs, item)
	}
	slices.SortFunc(report.SuppliedInputs, func(a, b Supplied) int { return strings.Compare(a.Base+a.RawSHA256, b.Base+b.RawSHA256) })

	groups := groupProposal(proposal)
	normalized := map[string]struct{}{}
	for _, g := range groups {
		for _, src := range g.sources {
			normalized[src.NormalizedLineSHA256] = struct{}{}
		}
	}
	merge, err := inspectMerge(in.Merged)
	if err != nil {
		return Report{}, err
	}
	prov, err := inspectProvenance(in.Provenance)
	if err != nil {
		return Report{}, err
	}
	if prov.supplied {
		if !workorders.UUID(in.ExpectedSessionID) || strings.ToLower(in.ExpectedSessionID) != in.ExpectedSessionID || prov.sessionID != in.ExpectedSessionID {
			prov.recorded = false
			report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "provenance_session_unbound", Note: "supply --session with the matching canonical session ID before attributing provenance to a session"})
		}
		if !prov.recorded {
			report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "provenance_unverified_input", Note: "proposal, incomplete or session-unbound provenance JSON is only an unverified digest comparison, not a recorded session receipt"})
		}
	}
	if !merge.supplied {
		report.Blockers = append(report.Blockers, "published_layer_unavailable")
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "published_merge_not_supplied", Note: "no merge metadata was supplied; snapshot publication and current rules are unverified"})
	} else {
		view := merge.view
		report.Merge = &view
		if !view.IntegrityOK {
			report.Blockers = append(report.Blockers, "merge_integrity")
		}
		if !view.MetadataCurrentAndValid {
			report.Blockers = append(report.Blockers, "merge_metadata_invalid_or_expired")
		}
		report.Blockers = append(report.Blockers, "publication_unverified", "independent_floor_unavailable")
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "publication_not_verified", Note: "supplied merge metadata and its vector do not prove snapshot publication or a trusted floor"})
		if !versionVectorWellFormed(view.Versions) {
			report.Blockers = append(report.Blockers, "version_vector_malformed")
		}
		if view.Context.Harness == "" || view.Context.Role == "" || view.Context.TenantID == "" || view.Context.ProjectID == "" {
			report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "harness_context_incomplete", Note: "merged context does not name tenant, project, role and harness"})
		}
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "snapshot_digests_not_reverified", Note: "version-vector digests are recorded as supplied; snapshot bytes were not in this input"})
	}
	if !prov.supplied {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "harness_receipt_not_supplied", Note: "no AEON-219 provenance document was supplied, so receipt of these bytes is unknown"})
	} else if len(prov.items) == 0 {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "harness_receipt_empty", Note: "the supplied provenance document has no items"})
	}
	if prov.recorded {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "provenance_origin_unverified", Note: "offline JSON is supplied worker-reported metadata; API origin and runtime observation were not verified"})
	}
	if prov.truncated {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "provenance_page_truncated", Note: "older provenance revisions were not in the supplied page"})
	}
	if prov.revisions > 1 {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "older_provenance_revisions_not_compared", Note: fmt.Sprintf("compared the first of %d revisions; older revisions are not a match", prov.revisions)})
	}
	if prov.setSHA != "" {
		report.MissingRuntimeEvidence = append(report.MissingRuntimeEvidence, Gap{Kind: "provenance_set_digest_not_reverified", Note: "the recorded provenance set digest was not recomputed"})
	}

	bodySHA := ""
	bodySize := 0
	if merge.supplied && merge.view.IntegrityOK {
		bodySHA = merge.computed
		bodySize = len(merge.merged.Body)
	}
	for _, item := range prov.items {
		comparison := classifyReceipt(item, report.SuppliedInputs, normalized, bodySHA, bodySize)
		if prov.recorded {
			comparison.Note = joinNote(comparison.Note, "supplied worker-reported metadata; API origin not observed")
			report.Receipts = append(report.Receipts, comparison)
		} else {
			comparison.Evidence = "unverified_" + prov.kind
			comparison.RawBytesReceived = false
			comparison.MergedBodyReceived = false
			if comparison.RawDigestMatches {
				comparison.Note = "supplied digest equals raw file bytes, but unverified input establishes no recorded session receipt"
			} else {
				comparison.Note = joinNote(comparison.Note, "unverified input; no recorded session receipt established")
			}
			report.UnverifiedComparisons = append(report.UnverifiedComparisons, comparison)
		}
	}
	type rawIdentity struct {
		digest string
		size   int64
	}
	received := map[rawIdentity]bool{}
	for _, receipt := range report.Receipts {
		if receipt.RawBytesReceived && receipt.ByteSize != nil {
			received[rawIdentity{receipt.ContentSHA256, *receipt.ByteSize}] = true
		}
	}
	for i := range report.SuppliedInputs {
		input := &report.SuppliedInputs[i]
		input.RawReceipt = received[rawIdentity{input.RawSHA256, int64(input.Bytes)}]
	}

	report.Lineage = lineageView(groups, merge)
	report.Lineage.Heuristics = len(proposal.Heuristics)
	report.Differences, report.Unresolved = append(report.Differences, differences(groups, merge)...), append(report.Unresolved, unresolvedItems(proposal, groups, merge, report.Receipts)...)
	slices.Sort(report.Blockers)
	report.Blockers = unique(report.Blockers)
	sortGaps(report.MissingRuntimeEvidence)
	sortItems(report.Unresolved)
	sortDiffs(report.Differences)
	report.SuppliedMergeCompared = merge.supplied && merge.view.IntegrityOK
	report.Counts = countReport(report)
	report.NextInvocation = nextInvocation(in.Files, in.Context, section, merge.supplied, prov.supplied)
	return report, nil
}

func newReport(trust rulesimport.TrustContext, section string, files []string) Report {
	return Report{
		Schema: Schema,
		Limits: Limits{
			SuppliedFilesAreExpectedInputs:   true,
			SuppliedMetadataIsNotServerProof: true,
			NameMatchIsNotComparison:         true,
			NormalizedHashIsNotRawBytes:      true,
		},
		SuppliedTrustContext:   string(trust),
		Section:                section,
		Blockers:               []string{},
		MissingRuntimeEvidence: modelGaps(),
		SuppliedInputs:         []Supplied{},
		Receipts:               []Receipt{},
		UnverifiedComparisons:  []Receipt{},
		Differences:            []Difference{},
		Unresolved:             []Item{},
		NextInvocation:         nextInvocation(files, trust, section, false, false),
	}
}

func modelGaps() []Gap {
	return []Gap{
		{Kind: "model_loaded", Note: "no runtime evidence that a model loaded the supplied bytes"},
		{Kind: "model_obeyed", Note: "no runtime evidence that a model obeyed the supplied instructions"},
		{Kind: "execution_verified", Note: "receipt and merge output do not verify harness execution"},
	}
}

func harnessLogical(base string) (string, bool) {
	switch base {
	case "AGENTS.md", "CLAUDE.md":
		return base, true
	default:
		return "", false
	}
}

func groupProposal(p rulesimport.Proposal) []groupedRule {
	contradictory := map[string]bool{}
	for _, c := range p.Contradictions {
		contradictory[c.Identity] = true
	}
	order := []string{}
	byID := map[string]*groupedRule{}
	for _, rule := range p.Rules {
		g := byID[rule.Identity]
		if g == nil {
			g = &groupedRule{
				identity: rule.Identity, importID: importIdentity(rule.Identity),
				textSHA: map[string]struct{}{}, fileSHA: map[string]struct{}{},
				contradictory: contradictory[rule.Identity],
			}
			byID[rule.Identity] = g
			order = append(order, rule.Identity)
		}
		g.textSHA[sha256hex(rule.Text)] = struct{}{}
		for _, src := range rule.Sources {
			g.fileSHA[src.FileSHA256] = struct{}{}
			g.sources = append(g.sources, LineageSource{
				Base: filepath.Base(src.Path), StartLine: src.StartLine, EndLine: src.EndLine,
				NormalizedLineSHA256: src.SHA256, FileSHA256: src.FileSHA256, NotFileReceipt: true,
			})
		}
	}
	out := make([]groupedRule, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	slices.SortFunc(out, func(a, b groupedRule) int { return strings.Compare(a.identity, b.identity) })
	return out
}

func importIdentity(proposalIdentity string) string {
	return "import-" + sha256hex(proposalIdentity)
}

func lineageView(groups []groupedRule, merge mergeMeta) LineageView {
	view := LineageView{Rules: []LineageRule{}, Note: "normalized_line_sha256 covers parsing lines after BOM removal and LF normalization; it is not a raw file receipt and not merge coverage"}
	linked := map[string]rules.Rule{}
	if merge.supplied && merge.view.IntegrityOK {
		for _, rule := range merge.merged.Rules {
			linked[rule.Identity] = rule
		}
	}
	for _, g := range groups {
		row := LineageRule{ProposalIdentity: g.identity, ImportIdentity: g.importID, Sources: append([]LineageSource{}, g.sources...)}
		if len(g.textSHA) == 1 {
			for sum := range g.textSHA {
				row.TextSHA256 = sum
			}
		}
		if merge.supplied && merge.view.IntegrityOK {
			row.AgainstMerge = againstMerge(g, linked)
		}
		view.Rules = append(view.Rules, row)
	}
	return view
}

func againstMerge(g groupedRule, linked map[string]rules.Rule) *AgainstMerge {
	rule, ok := linked[g.importID]
	out := &AgainstMerge{NotExecutionEvidence: true, IdentityKeyMatch: ok}
	if !ok {
		return out
	}
	out.MergedIdentity = rule.Identity
	_, raw := g.fileSHA[rule.Source.Revision]
	out.RawLineageLinked = raw && !g.contradictory && len(g.textSHA) > 0
	if len(g.textSHA) == 1 && !g.contradictory {
		_, out.TextSHA256Agrees = g.textSHA[sha256hex(rule.Text)]
	}
	return out
}

func differences(groups []groupedRule, merge mergeMeta) []Difference {
	if !merge.supplied || !merge.view.IntegrityOK {
		return nil
	}
	seen := map[string]bool{}
	var out []Difference
	for _, g := range groups {
		seen[g.importID] = true
		rule, ok := findMerged(merge.merged.Rules, g.importID)
		if !ok {
			out = append(out, Difference{Kind: "not_in_merge", Identity: g.importID, SuppliedTextSHA256: singleText(g)})
			continue
		}
		if g.contradictory || len(g.textSHA) != 1 {
			continue
		}
		got := sha256hex(rule.Text)
		if _, agree := g.textSHA[got]; !agree {
			out = append(out, Difference{Kind: "text_differs", Identity: g.importID, SuppliedTextSHA256: singleText(g), MergedTextSHA256: got})
		}
	}
	for _, rule := range merge.merged.Rules {
		if seen[rule.Identity] {
			continue
		}
		out = append(out, Difference{Kind: "not_in_supplied", Identity: rule.Identity, MergedTextSHA256: sha256hex(rule.Text)})
	}
	return out
}

func findMerged(ruleset []rules.Rule, importID string) (rules.Rule, bool) {
	for _, rule := range ruleset {
		if rule.Identity == importID {
			return rule, true
		}
	}
	return rules.Rule{}, false
}

func singleText(g groupedRule) string {
	if len(g.textSHA) != 1 {
		return ""
	}
	for sum := range g.textSHA {
		return sum
	}
	return ""
}

func unresolvedItems(p rulesimport.Proposal, groups []groupedRule, merge mergeMeta, receipts []Receipt) []Item {
	var out []Item
	for _, item := range p.Unresolved {
		note := strings.ReplaceAll(item.Note, "source text is retained without applying precedence", "the source excerpt was not copied into this comparison")
		note = strings.ReplaceAll(note, "source text retained", "the source excerpt was not copied into this comparison")
		if item.Text != "" && !strings.Contains(note, "not copied into this comparison") {
			note += "; the source excerpt was not copied into this comparison"
		}
		out = append(out, Item{Kind: item.Kind, Base: filepath.Base(item.Path), Line: item.Line, Note: note})
	}
	for _, item := range p.Contradictions {
		out = append(out, Item{Kind: "source_contradiction", Note: item.Identity + " differs in " + strings.Join(item.Fields, ",") + "; neither side was chosen"})
	}
	if len(p.Heuristics) > 0 {
		out = append(out, Item{Kind: "normalized_text_heuristic", Note: "normalized text suggestions are not coverage and were not applied"})
	}
	if !p.AlwaysOn.Insert {
		out = append(out, Item{Kind: "always_on_budget", Note: fmt.Sprintf("always-on projection is %d bytes, budget %d; it was not trimmed or inserted", p.AlwaysOn.Bytes, p.AlwaysOn.Budget)})
	}
	for _, g := range groups {
		if !g.contradictory {
			continue
		}
		out = append(out, Item{Kind: "source_contradiction", Note: g.identity + " has contradictory supplied text; no winner was selected"})
	}
	if !merge.supplied || !merge.view.IntegrityOK {
		out = append(out, Item{Kind: "merge_not_compared", Note: "lineage was not treated as merge coverage without an intact merged document"})
		return append(out, receiptUnresolved(receipts)...)
	}
	for _, g := range groups {
		rule, ok := findMerged(merge.merged.Rules, g.importID)
		if !ok {
			continue
		}
		if _, raw := g.fileSHA[rule.Source.Revision]; raw {
			continue
		}
		if _, norm := normalizedOf(g, rule.Source.Revision); norm {
			out = append(out, Item{Kind: "normalized_revision_only", Note: g.importID + " revision equals a normalized line hash, not a supplied raw file hash"})
			continue
		}
		out = append(out, Item{Kind: "identity_without_raw_revision", Note: g.importID + " identity key matches and its revision is not a supplied raw file hash; multi-source lineage digests are not recomputed"})
	}
	for _, rule := range merge.merged.Rules {
		if findGroup(groups, rule.Identity) != nil {
			continue
		}
		if _, raw := rawFile(groups, rule.Source.Revision); raw {
			out = append(out, Item{Kind: "revision_without_identity", Note: rule.Identity + " revision equals a supplied raw file hash but the import identity does not match"})
		}
	}
	return append(out, receiptUnresolved(receipts)...)
}

func normalizedOf(g groupedRule, revision string) (struct{}, bool) {
	for _, src := range g.sources {
		if src.NormalizedLineSHA256 == revision && src.FileSHA256 != revision {
			return struct{}{}, true
		}
	}
	return struct{}{}, false
}

func rawFile(groups []groupedRule, revision string) (struct{}, bool) {
	for _, g := range groups {
		if _, ok := g.fileSHA[revision]; ok {
			return struct{}{}, true
		}
	}
	return struct{}{}, false
}

func findGroup(groups []groupedRule, importID string) *groupedRule {
	for i := range groups {
		if groups[i].importID == importID {
			return &groups[i]
		}
	}
	return nil
}

func receiptUnresolved(receipts []Receipt) []Item {
	var out []Item
	for _, receipt := range receipts {
		switch {
		case receipt.HashKind == "invalid":
			out = append(out, Item{Kind: "invalid_provenance_digest", Note: receipt.LogicalName + " provenance hash kind or digest is invalid"})
		case receipt.RawDigestMatches && (receipt.SizeAgrees == nil || !*receipt.SizeAgrees):
			out = append(out, Item{Kind: "provenance_size_mismatch", Note: receipt.LogicalName + " digest matches but the recorded size is absent or contradictory"})
		case receipt.NormalizedHashOnly:
			out = append(out, Item{Kind: "normalized_hash_only", Note: receipt.LogicalName + " provenance digest equals a normalized line hash, not supplied raw bytes"})
		case receipt.NameWithoutRawHash && !receipt.RawBytesReceived:
			out = append(out, Item{Kind: "name_without_raw_hash", Note: receipt.LogicalName + " logical name agrees with a supplied file and the raw hashes differ"})
		case receipt.HashKind == "absent" || receipt.ContentSHA256 == "":
			out = append(out, Item{Kind: "digest_absent", Note: receipt.LogicalName + " provenance item has no content digest"})
		case !receipt.RawBytesReceived && !receipt.MergedBodyReceived:
			out = append(out, Item{Kind: "unrelated_receipt", Note: receipt.LogicalName + " provenance digest equals neither supplied raw bytes nor the merged body"})
		}
	}
	return out
}

func classifyReceipt(item harness.ProvenanceItem, supplied []Supplied, normalized map[string]struct{}, bodySHA string, bodySize int) Receipt {
	out := Receipt{Evidence: "supplied_worker_reported_metadata", LogicalName: item.LogicalName, Kind: item.Kind, HashKind: item.HashKind, ByteSize: item.ByteSize, SuppliedBases: []string{}}
	if item.Version != nil {
		out.Version = *item.Version
	}
	if item.ContentSHA256 != nil {
		out.ContentSHA256 = *item.ContentSHA256
	}
	if (out.HashKind != "content" || !sha256Pattern(out.ContentSHA256)) && !(out.HashKind == "absent" && item.ContentSHA256 == nil) {
		out.HashKind = "invalid"
		out.Note = "hash kind and digest are invalid or contradictory; this is not receipt evidence"
		out.ContentSHA256 = ""
		return out
	}
	if out.HashKind == "absent" {
		out.Note = "no content digest was recorded"
		return out
	}
	var sized *bool
	for _, file := range supplied {
		if file.RawSHA256 == out.ContentSHA256 && out.ContentSHA256 != "" {
			out.RawDigestMatches = true
			out.SuppliedBases = append(out.SuppliedBases, file.Base)
			agrees := item.ByteSize != nil && *item.ByteSize == int64(file.Bytes)
			if item.ByteSize == nil {
				out.Note = "raw hash matches supplied bytes; recorded size is absent, so receipt is invalid"
			} else if !agrees {
				value := false
				sized = &value
				out.Note = "raw hash matches supplied bytes but recorded size contradicts it; receipt is invalid"
			} else if sized == nil {
				value := true
				sized = &value
			}
		}
		if file.HarnessLogicalName != "" && file.HarnessLogicalName == item.LogicalName && file.RawSHA256 != out.ContentSHA256 {
			out.NameWithoutRawHash = true
		}
	}
	slices.Sort(out.SuppliedBases)
	out.SizeAgrees = sized
	out.RawBytesReceived = out.RawDigestMatches && sized != nil && *sized
	if bodySHA != "" && out.ContentSHA256 == bodySHA && item.ByteSize != nil && *item.ByteSize == int64(bodySize) {
		out.MergedBodyReceived = true
	}
	if _, ok := normalized[out.ContentSHA256]; ok && out.ContentSHA256 != "" && !out.RawBytesReceived && !out.MergedBodyReceived {
		out.NormalizedHashOnly = true
	}
	switch {
	case out.RawDigestMatches && !out.RawBytesReceived:
		// Preserve the missing or contradictory size diagnosis.
	case out.RawBytesReceived && out.MergedBodyReceived:
		out.Note = joinNote(out.Note, "provenance digest equals supplied raw bytes and the merged body; this is receipt, not model load or obedience")
	case out.RawBytesReceived && out.NameWithoutRawHash:
		out.Note = joinNote(out.Note, "raw bytes match a supplied file; a same logical name on another file is not that receipt")
	case out.RawBytesReceived && out.Note == "":
		out.Note = "provenance digest equals supplied raw bytes; this records receipt, not model load or obedience"
	case out.MergedBodyReceived && !out.RawBytesReceived:
		out.Note = "provenance digest equals the merged body, not a supplied instruction file"
	case out.NormalizedHashOnly:
		out.Note = "provenance digest equals a normalized line hash; that is not a raw file receipt"
	case out.NameWithoutRawHash:
		out.Note = "logical name matches a supplied file and the raw hashes differ"
	case out.HashKind == "absent" || out.ContentSHA256 == "":
		out.Note = "no content digest was recorded"
	default:
		out.Note = "provenance digest equals neither supplied raw bytes nor the merged body"
	}
	return out
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func sha256Pattern(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func inspectMerge(raw []byte) (mergeMeta, error) {
	if raw == nil {
		return mergeMeta{}, nil
	}
	if len(raw) == 0 || len(raw) > rules.MaxCacheBytes {
		return mergeMeta{}, errors.New("merged document is empty or exceeds the byte bound")
	}
	body, err := mergedPayload(raw)
	if err != nil {
		return mergeMeta{}, err
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var merged rules.Merged
	if err = dec.Decode(&merged); err != nil {
		return mergeMeta{}, errors.New("merged document is not an AR1 merged rules object")
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return mergeMeta{}, errors.New("trailing merged data")
	}
	computed := sha256hex(merged.Body)
	floorPresent := strings.TrimSpace(merged.Floor) != ""
	floorOK := floorInBody(merged.Body, merged.Floor)
	integrity := computed == merged.SHA256 && merged.ByteSize == len(merged.Body) && utf8OK(merged.Body)
	current := rules.ValidateMerged(merged, merged.Context, time.Now()) == nil && versionVectorWellFormed(merged.Versions) && merged.Version == latestVersion(merged.Versions) && !strings.ContainsRune(merged.Body, 0)
	return mergeMeta{supplied: true, merged: merged, computed: computed, view: MergeView{
		Context: merged.Context, Harness: merged.Context.Harness, Role: merged.Context.Role,
		Version: merged.Version, Versions: append([]rules.VersionRef{}, merged.Versions...),
		StatedBodySHA256: merged.SHA256, ComputedBodySHA256: computed,
		ByteSize: merged.ByteSize, ComputedByteSize: len(merged.Body),
		IntegrityOK: integrity, FloorPresent: floorPresent, FloorInBody: floorOK, MetadataCurrentAndValid: current,
	}}, nil
}

func mergedPayload(raw []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var probe map[string]json.RawMessage
	if err := dec.Decode(&probe); err != nil {
		return nil, errors.New("merged document is not JSON")
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return nil, errors.New("trailing merged data")
	}
	if probe["sha256"] != nil && probe["body"] != nil {
		return raw, nil
	}
	if probe["rules"] != nil && probe["body"] == nil {
		return probe["rules"], nil
	}
	if probe["bundle"] != nil {
		return probe["bundle"], nil
	}
	return nil, errors.New("merged document does not contain AR1 merged rules")
}

func versionVectorWellFormed(versions []rules.VersionRef) bool {
	if len(versions) == 0 {
		return false
	}
	for _, v := range versions {
		if !releasehistory.ValidVersion(v.Version) || !sha256Pattern(v.SHA256) || v.SetID == "" {
			return false
		}
	}
	return true
}

func latestVersion(versions []rules.VersionRef) string {
	latest := ""
	for _, v := range versions {
		if v.Version > latest {
			latest = v.Version
		}
	}
	return latest
}

func floorInBody(body, floor string) bool {
	if strings.TrimSpace(floor) == "" {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(floor), "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains("\n"+body, "\n"+line+"\n") {
			return false
		}
	}
	return true
}

func utf8OK(s string) bool {
	return strings.ToValidUTF8(s, "") == s && !strings.ContainsRune(s, 0)
}

func inspectProvenance(raw []byte) (provMeta, error) {
	if raw == nil {
		return provMeta{}, nil
	}
	if len(raw) == 0 || len(raw) > rules.MaxCacheBytes {
		return provMeta{}, errors.New("provenance document is empty or exceeds the byte bound")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var probe any
	if err := dec.Decode(&probe); err != nil {
		return provMeta{}, errors.New("provenance document is not JSON")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return provMeta{}, errors.New("trailing provenance data")
	}
	meta := provMeta{supplied: true}
	switch probe.(type) {
	case []any:
		items, err := decodeItems(json.RawMessage(raw))
		if err != nil {
			return provMeta{}, err
		}
		meta.items = items
		meta.kind = "bare_array"
	case map[string]any:
		if err := fillProvenanceObject(raw, &meta); err != nil {
			return provMeta{}, err
		}
	default:
		return provMeta{}, errors.New("provenance document is not an object or array")
	}
	if len(meta.items) > harnessMaxItems() {
		return provMeta{}, errors.New("provenance document exceeds the item bound")
	}
	return meta, nil
}

func harnessMaxItems() int { return 16 } // matches harness maxProvenanceItems

func fillProvenanceObject(raw []byte, meta *provMeta) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return errors.New("provenance document is not JSON")
	}
	if probe["truncated"] != nil {
		var truncated bool
		if err := json.Unmarshal(probe["truncated"], &truncated); err != nil {
			return errors.New("provenance truncated flag is invalid")
		}
		meta.truncated = truncated
	}
	if probe["provenance_recorded"] != nil {
		var recorded bool
		if err := json.Unmarshal(probe["provenance_recorded"], &recorded); err != nil {
			return errors.New("provenance recorded flag is invalid")
		}
		if !recorded {
			meta.kind = "proposal"
		}
	}
	switch {
	case probe["provenance_items"] != nil:
		meta.kind = "proposal"
		return decodeInto(&meta.items, probe["provenance_items"])
	case probe["revisions"] != nil:
		pageMetadata := probe["session_id"] != nil && probe["truncated"] != nil
		var page harness.ProvenancePage
		if err := json.Unmarshal(raw, &page); err != nil || len(page.Revisions) == 0 {
			return errors.New("provenance revisions are missing")
		}
		proposed := meta.kind == "proposal"
		meta.kind = "revision_page"
		meta.revisions = len(page.Revisions)
		meta.items = page.Revisions[0].Items
		meta.setSHA = page.Revisions[0].SetSHA256
		meta.sessionID = page.SessionID
		meta.recorded = pageMetadata && validProvenanceRevision(page.Revisions[0]) && page.SessionID == page.Revisions[0].SessionID
		previous := page.Revisions[0].Revision
		for _, rev := range page.Revisions[1:] {
			if !validProvenanceRevision(rev) || rev.SessionID != page.SessionID || rev.Revision >= previous {
				meta.recorded = false
			}
			previous = rev.Revision
		}
		if proposed {
			meta.recorded = false
			meta.kind = "proposal"
		}
		return nil
	case probe["items"] != nil:
		var rev harness.ProvenanceRevision
		if err := json.Unmarshal(raw, &rev); err != nil {
			return errors.New("provenance revision is invalid")
		}
		proposed := meta.kind == "proposal"
		if !proposed {
			meta.kind = "revision"
		}
		meta.items, meta.setSHA, meta.sessionID = rev.Items, rev.SetSHA256, rev.SessionID
		meta.recorded = validProvenanceRevision(rev) && !proposed
		return nil
	default:
		return errors.New("provenance items were not in the document")
	}
}

func validProvenanceRevision(rev harness.ProvenanceRevision) bool {
	return workorders.UUID(rev.ID) && workorders.UUID(rev.SessionID) && workorders.UUID(rev.RecordedBy) &&
		strings.ToLower(rev.ID) == rev.ID && strings.ToLower(rev.SessionID) == rev.SessionID && strings.ToLower(rev.RecordedBy) == rev.RecordedBy &&
		rev.Revision > 0 && sha256Pattern(rev.SetSHA256) && !rev.RecordedAt.IsZero() && !rev.RecordedAt.After(time.Now().Add(time.Minute)) &&
		len(rev.Items) > 0 && len(rev.Items) <= harnessMaxItems()
}

func decodeInto(dest *[]harness.ProvenanceItem, raw json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return errors.New("provenance items are invalid")
	}
	return nil
}

func decodeItems(raw json.RawMessage) ([]harness.ProvenanceItem, error) {
	var items []harness.ProvenanceItem
	if err := decodeInto(&items, raw); err != nil {
		return nil, err
	}
	return items, nil
}

func countReport(report Report) Counts {
	c := Counts{SuppliedFiles: len(report.SuppliedInputs), Unresolved: len(report.Unresolved)}
	for _, receipt := range report.Receipts {
		if receipt.RawBytesReceived {
			c.RawReceipts++
		}
		if receipt.NameWithoutRawHash && !receipt.RawBytesReceived {
			c.NameOnly++
		}
		if receipt.NormalizedHashOnly {
			c.NormalizedOnly++
		}
	}
	for _, rule := range report.Lineage.Rules {
		if rule.AgainstMerge != nil && rule.AgainstMerge.RawLineageLinked {
			c.RawLineageLinks++
		}
	}
	for _, diff := range report.Differences {
		if diff.Kind == "text_differs" {
			c.TextDifferences++
		}
	}
	return c
}

func nextInvocation(files []string, trust rulesimport.TrustContext, section string, merged, provenance bool) string {
	var b strings.Builder
	b.WriteString("aeon rules-compare --context ")
	b.WriteString(string(trust))
	if section != "" && section != rulesimport.SectionAll {
		b.WriteString(" --section ")
		b.WriteString(section)
	}
	names := append([]string{}, files...)
	slices.Sort(names)
	for _, file := range names {
		b.WriteString(" --file ")
		b.WriteString(filepath.Base(file))
	}
	if merged {
		b.WriteString(" --merged MERGED.json")
	} else {
		b.WriteString(" --merged SUPPLIED_MERGE.json")
	}
	if provenance {
		b.WriteString(" --provenance PROVENANCE.json")
	} else {
		b.WriteString(" --provenance HARNESS_PROVENANCE.json")
	}
	b.WriteString(" --session SESSION_UUID")
	b.WriteString(" --out report.json")
	return b.String()
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func unique(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := in[:1]
	for _, item := range in[1:] {
		if item != out[len(out)-1] {
			out = append(out, item)
		}
	}
	return out
}

func sortGaps(items []Gap) {
	slices.SortFunc(items, func(a, b Gap) int { return strings.Compare(a.Kind+a.Note, b.Kind+b.Note) })
}

func sortItems(items []Item) {
	slices.SortFunc(items, func(a, b Item) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Base+a.Note, b.Base+b.Note)
	})
}

func sortDiffs(items []Difference) {
	slices.SortFunc(items, func(a, b Difference) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Identity, b.Identity)
	})
}
