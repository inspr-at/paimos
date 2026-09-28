// SPDX-License-Identifier: AGPL-3.0-only
//go:build (linux || darwin) && !aeon_test_rulesimport_unsupported

package rulescompare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/rulesimport"

	"golang.org/x/sys/unix"
)

const fixtureText = "Keep the fixture boundary."
const floorMarker = "FLOOR-LEAK-MARKER"
const whyMarker = "WHY-LEAK-MARKER"
const fixtureSession = "10000000-0000-4000-8000-000000000009"

func TestMissingPublicationReportsHashesWithoutSuccess(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	neighbor := filepath.Join(filepath.Dir(path), "id_rsa")
	if err := os.WriteFile(neighbor, []byte("NEIGHBOR-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	raw := fileSHA(t, path)
	assertLimits(t, report)
	if report.PublishedMergeCompared || report.Merge != nil || len(report.Receipts) != 0 {
		t.Fatal("unpublished inputs were treated as a completed merge comparison")
	}
	if len(report.SuppliedInputs) != 1 || report.SuppliedInputs[0].RawSHA256 != raw || report.SuppliedInputs[0].RawReceipt || report.SuppliedInputs[0].Base != "AGENTS.md" {
		t.Fatalf("supplied %+v", report.SuppliedInputs)
	}
	if !has(report.Blockers, "published_layer_unavailable") || !strings.Contains(report.NextInvocation, "SUPPLIED_MERGE.json") || !strings.Contains(report.NextInvocation, "HARNESS_PROVENANCE.json") {
		t.Fatalf("blockers %+v invocation %s", report.Blockers, report.NextInvocation)
	}
	blob := mustJSON(t, report)
	for _, leaked := range []string{fixtureText, "NEIGHBOR-SECRET", path} {
		if strings.Contains(blob, leaked) {
			t.Fatalf("report leaked %q", leaked)
		}
	}
}

func TestNameAndNormalizedHashesAreNotReceipts(t *testing.T) {
	dir := t.TempDir()
	path := writeAgents(t, dir, fixtureDoc())
	raw := fileSHA(t, path)
	proposal := mustProposal(t, path)
	if len(proposal.Rules) != 1 || len(proposal.Rules[0].Sources) != 1 {
		t.Fatalf("proposal %+v", proposal.Rules)
	}
	normalized := proposal.Rules[0].Sources[0].SHA256
	if normalized == raw {
		t.Fatal("fixture normalized line hash unexpectedly equals the raw file hash")
	}
	other := sha256hex("different bytes")
	nameOnly := provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &other, ByteSize: sizePtr(1)})
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: nameOnly, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	assertLimits(t, report)
	if len(report.Receipts) != 1 || report.Receipts[0].RawBytesReceived || !report.Receipts[0].NameWithoutRawHash || report.Counts.NameOnly != 1 {
		t.Fatalf("name-only receipt %+v", report.Receipts)
	}
	if !hasItem(report.Unresolved, "name_without_raw_hash") {
		t.Fatal("name agreement was not left unresolved")
	}
	norm := provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &normalized, ByteSize: sizePtr(int64(proposal.Files[0].Bytes))})
	report, err = Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: norm, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	assertLimits(t, report)
	if !report.Receipts[0].NormalizedHashOnly || report.Receipts[0].RawBytesReceived || report.Receipts[0].MergedBodyReceived || report.Counts.NormalizedOnly != 1 {
		t.Fatalf("normalized receipt %+v", report.Receipts[0])
	}
}

func TestMalformedDigestAndSizeNeverEstablishRawReceipt(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	raw := fileSHA(t, path)
	size := int64(len(fixtureDoc()))
	cases := []struct {
		name   string
		kind   string
		digest string
		size   *int64
	}{
		{"unknown_kind", "other", raw, &size},
		{"absent_with_digest", "absent", raw, &size},
		{"uppercase", "content", strings.ToUpper(raw), &size},
		{"wrong_size", "content", raw, sizePtr(size + 1)},
		{"missing_size", "content", raw, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: tc.kind, ContentSHA256: &tc.digest, ByteSize: tc.size}
			report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: provenanceJSON(t, item), ExpectedSessionID: fixtureSession})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Receipts) != 1 || report.Receipts[0].RawBytesReceived || report.SuppliedInputs[0].RawReceipt || report.Counts.RawReceipts != 0 {
				t.Fatalf("malformed item became receipt: %+v", report.Receipts)
			}
			if !hasItem(report.Unresolved, "invalid_provenance_digest") && !hasItem(report.Unresolved, "provenance_size_mismatch") {
				t.Fatalf("malformed item has no explicit gap: %+v", report.Unresolved)
			}
		})
	}
}

func TestProposalAndWrongSessionStayUnverified(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	raw := fileSHA(t, path)
	item := harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &raw, ByteSize: sizePtr(int64(len(fixtureDoc())))}
	canonical := provenanceJSON(t, item)
	inputs := [][]byte{
		mustEncode(t, []harness.ProvenanceItem{item}),
		mustEncode(t, map[string]any{"provenance_items": []harness.ProvenanceItem{item}, "provenance_recorded": false}),
		mustEncode(t, map[string]any{"items": []harness.ProvenanceItem{item}}),
		canonical,
	}
	wantEvidence := []string{"unverified_bare_array", "unverified_proposal", "unverified_revision", "unverified_revision"}
	for i, rawInput := range inputs {
		session := fixtureSession
		if i == 3 {
			session = "10000000-0000-4000-8000-000000000099"
		}
		report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: rawInput, ExpectedSessionID: session})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Receipts) != 0 || len(report.UnverifiedComparisons) != 1 || report.UnverifiedComparisons[0].Evidence != wantEvidence[i] || !report.UnverifiedComparisons[0].RawDigestMatches || report.UnverifiedComparisons[0].RawBytesReceived || report.Counts.RawReceipts != 0 || !hasGap(report.MissingRuntimeEvidence, "provenance_unverified_input") {
			t.Fatalf("case %d promoted unverified input: %+v", i, report)
		}
	}
}

func TestIncompleteRevisionMetadataStaysUnverified(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	raw := fileSHA(t, path)
	item := harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &raw, ByteSize: sizePtr(int64(len(fixtureDoc())))}
	input := mustEncode(t, map[string]any{"session_id": fixtureSession, "revision": 1, "set_sha256": strings.Repeat("ab", 32), "items": []harness.ProvenanceItem{item}})
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: input, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Receipts) != 0 || len(report.UnverifiedComparisons) != 1 || report.UnverifiedComparisons[0].RawBytesReceived || !hasGap(report.MissingRuntimeEvidence, "provenance_unverified_input") {
		t.Fatalf("incomplete revision became receipt: %+v", report)
	}
}

func TestExpiredOrInvalidMergeIsOnlySuppliedComparison(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	proposal := mustProposal(t, path)
	var merged rules.Merged
	if err := json.Unmarshal(publishedMerge(t, importIdentity(proposal.Rules[0].Identity), proposal.Rules[0].Text, fileSHA(t, path)), &merged); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	merged.ValidUntil = &past
	for _, m := range []rules.Merged{merged, func() rules.Merged { x := merged; x.ValidUntil = nil; x.Context.Role = "unknown"; return x }()} {
		report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: mustEncode(t, m)})
		if err != nil {
			t.Fatal(err)
		}
		if report.PublishedMergeCompared || !report.SuppliedMergeCompared || report.Merge == nil || report.Merge.Published || report.Merge.MetadataCurrentAndValid || !has(report.Blockers, "merge_metadata_invalid_or_expired") || !has(report.Blockers, "publication_unverified") || report.Limits.RolloutAuthorized {
			t.Fatalf("merge was promoted to publication: %+v", report)
		}
	}
}

func TestRawReceiptAndLineageDoNotVerifyExecution(t *testing.T) {
	dir := t.TempDir()
	path := writeAgents(t, dir, fixtureDoc())
	raw := fileSHA(t, path)
	proposal := mustProposal(t, path)
	importID := importIdentity(proposal.Rules[0].Identity)
	merged := publishedMerge(t, importID, proposal.Rules[0].Text, raw)
	size := int64(proposal.Files[0].Bytes)
	prov := provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &raw, ByteSize: &size})
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: merged, Provenance: prov, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	assertLimits(t, report)
	if report.PublishedMergeCompared || !report.SuppliedMergeCompared || report.Merge == nil || !report.Merge.IntegrityOK || report.Merge.Published || !report.Merge.MetadataCurrentAndValid || report.Merge.Harness != "codex" || report.Merge.SnapshotDigestsReverified {
		t.Fatalf("merge %+v", report.Merge)
	}
	if report.Merge.ComputedBodySHA256 != report.Merge.StatedBodySHA256 || len(report.Merge.Versions) != 1 {
		t.Fatal("version vector")
	}
	if !report.SuppliedInputs[0].RawReceipt || !report.Receipts[0].RawBytesReceived || report.Receipts[0].SizeAgrees == nil || !*report.Receipts[0].SizeAgrees {
		t.Fatalf("receipt %+v supplied %+v", report.Receipts, report.SuppliedInputs)
	}
	if report.Receipts[0].Evidence != "supplied_worker_reported_metadata" || !hasGap(report.MissingRuntimeEvidence, "provenance_origin_unverified") || !has(report.Blockers, "publication_unverified") || !has(report.Blockers, "independent_floor_unavailable") {
		t.Fatalf("source or publication limit missing: %+v %+v", report.Receipts[0], report.Blockers)
	}
	linked := false
	for _, rule := range report.Lineage.Rules {
		if rule.ImportIdentity == importID && rule.AgainstMerge != nil && rule.AgainstMerge.RawLineageLinked && rule.AgainstMerge.TextSHA256Agrees && rule.AgainstMerge.NotExecutionEvidence {
			linked = true
		}
	}
	if !linked || report.Counts.RawLineageLinks != 1 || report.Counts.TextDifferences != 0 {
		t.Fatalf("lineage %+v counts %+v", report.Lineage.Rules, report.Counts)
	}
	if !hasDiff(report.Differences, "not_in_supplied", "safety") {
		t.Fatalf("floor without supplied lineage missing: %+v", report.Differences)
	}
	blob := mustJSON(t, report)
	for _, leaked := range []string{fixtureText, floorMarker, whyMarker, path} {
		if strings.Contains(blob, leaked) {
			t.Fatalf("report leaked %q", leaked)
		}
	}
}

func TestMergedBodyReceiptIsNotAFileReceipt(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	proposal := mustProposal(t, path)
	importID := importIdentity(proposal.Rules[0].Identity)
	raw := fileSHA(t, path)
	merged := publishedMerge(t, importID, proposal.Rules[0].Text, raw)
	var decoded rules.Merged
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatal(err)
	}
	body := decoded.SHA256
	prov := provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &body, ByteSize: sizePtr(int64(decoded.ByteSize))})
	wrapped, err := json.Marshal(map[string]any{"rules": decoded, "execution_verified": false})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: wrapped, Provenance: prov, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	assertLimits(t, report)
	if report.PublishedMergeCompared || !report.SuppliedMergeCompared || !report.Receipts[0].MergedBodyReceived || report.Receipts[0].RawBytesReceived {
		t.Fatalf("body receipt %+v", report.Receipts)
	}
	if report.SuppliedInputs[0].RawReceipt {
		t.Fatal("merged body hash was treated as the instruction file")
	}
}

func TestIdentityWithoutRawRevisionAndTextDifference(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	proposal := mustProposal(t, path)
	importID := importIdentity(proposal.Rules[0].Identity)
	merged := publishedMerge(t, importID, "Changed fixture sentence.", "not-a-raw-file-hash")
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: merged})
	if err != nil {
		t.Fatal(err)
	}
	assertLimits(t, report)
	found := false
	for _, rule := range report.Lineage.Rules {
		if rule.ImportIdentity == importID && rule.AgainstMerge != nil {
			found = true
			if rule.AgainstMerge.RawLineageLinked || rule.AgainstMerge.TextSHA256Agrees || !rule.AgainstMerge.IdentityKeyMatch {
				t.Fatalf("against %+v", rule.AgainstMerge)
			}
		}
	}
	if !found || !hasDiff(report.Differences, "text_differs", importID) || !hasItem(report.Unresolved, "identity_without_raw_revision") {
		t.Fatalf("diff %+v unresolved %+v", report.Differences, report.Unresolved)
	}
	if strings.Contains(mustJSON(t, report), "Changed fixture sentence.") {
		t.Fatal("merged prose was copied into the report")
	}
}

func TestDoctrineIdentityStringIsNotLineage(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	proposal := mustProposal(t, path)
	merged := publishedMerge(t, proposal.Rules[0].Identity, proposal.Rules[0].Text, fileSHA(t, path))
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: merged})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range report.Lineage.Rules {
		if rule.AgainstMerge != nil && (rule.AgainstMerge.IdentityKeyMatch || rule.AgainstMerge.RawLineageLinked) {
			t.Fatal("proposal identity string was treated as the AR1 import key")
		}
	}
}

func TestCorruptMergeWithholdsCoverage(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	proposal := mustProposal(t, path)
	merged := publishedMerge(t, importIdentity(proposal.Rules[0].Identity), proposal.Rules[0].Text, fileSHA(t, path))
	var decoded rules.Merged
	if err := json.Unmarshal(merged, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.SHA256 = strings.Repeat("cd", 32)
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Merged: raw})
	if err != nil {
		t.Fatal(err)
	}
	if report.PublishedMergeCompared || !has(report.Blockers, "merge_integrity") || !hasItem(report.Unresolved, "merge_not_compared") {
		t.Fatalf("blockers %+v", report.Blockers)
	}
	for _, rule := range report.Lineage.Rules {
		if rule.AgainstMerge != nil {
			t.Fatal("corrupt merge still produced coverage")
		}
	}
}

func TestRefusesSecretSymlinkFIFOAndOversize(t *testing.T) {
	ctx := context.Background()
	if _, err := Compare(ctx, Input{Context: rulesimport.ContextProject, Files: []string{filepath.Join(t.TempDir(), ".ssh", "AGENTS.md")}}); !errors.Is(err, rulesimport.ErrProhibitedPath) {
		t.Fatal(err)
	}
	dir := t.TempDir()
	real := writeAgents(t, dir, fixtureDoc())
	link := filepath.Join(dir, "linked")
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(link, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(ctx, Input{Context: rulesimport.ContextProject, Files: []string{filepath.Join(link, "AGENTS.md")}}); !errors.Is(err, rulesimport.ErrSymlink) {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(ctx, Input{Context: rulesimport.ContextProject, Files: []string{fifo}}); !errors.Is(err, rulesimport.ErrNotRegular) {
		t.Fatal(err)
	}
	big := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(big, bytes.Repeat([]byte("a"), rulesimport.MaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(ctx, Input{Context: rulesimport.ContextProject, Files: []string{big}}); !errors.Is(err, rulesimport.ErrByteBound) {
		t.Fatal(err)
	}
}

func TestProvenancePageUsesOnlyTheFirstRevision(t *testing.T) {
	path := writeAgents(t, t.TempDir(), fixtureDoc())
	raw := fileSHA(t, path)
	other := sha256hex("older revision")
	page := map[string]any{
		"session_id": fixtureSession,
		"truncated":  true,
		"revisions": []map[string]any{
			{"id": "10000000-0000-4000-8000-000000000010", "session_id": fixtureSession, "revision": 2, "recorded_by_principal_id": "10000000-0000-4000-8000-000000000011", "recorded_at": time.Now().UTC(), "set_sha256": strings.Repeat("ab", 32), "items": []harness.ProvenanceItem{{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &raw, ByteSize: sizePtr(int64(len(fixtureDoc())))}}},
			{"id": "10000000-0000-4000-8000-000000000012", "session_id": fixtureSession, "revision": 1, "recorded_by_principal_id": "10000000-0000-4000-8000-000000000011", "recorded_at": time.Now().UTC(), "set_sha256": strings.Repeat("cd", 32), "items": []harness.ProvenanceItem{{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &other, ByteSize: sizePtr(1)}}},
		},
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: encoded, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Receipts) != 1 || !report.Receipts[0].RawBytesReceived || !hasGap(report.MissingRuntimeEvidence, "older_provenance_revisions_not_compared") || !hasGap(report.MissingRuntimeEvidence, "provenance_page_truncated") || !hasGap(report.MissingRuntimeEvidence, "provenance_set_digest_not_reverified") {
		t.Fatalf("page receipts %+v gaps %+v", report.Receipts, report.MissingRuntimeEvidence)
	}
	assertLimits(t, report)
}

func TestHardSafetyExcerptIsNotCopied(t *testing.T) {
	body := "## Hard safety\n- Preserve the fixture boundary.\n  Why: tests need a reason.\n"
	path := writeAgents(t, t.TempDir(), body)
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasItem(report.Unresolved, "strength_unspecified") {
		t.Fatalf("unresolved %+v", report.Unresolved)
	}
	if strings.Contains(mustJSON(t, report), "Preserve the fixture boundary.") {
		t.Fatal("unresolved excerpt was copied")
	}
}

func TestBOMRawHashDiffersFromNormalizedBytes(t *testing.T) {
	rawBytes := append([]byte{0xEF, 0xBB, 0xBF}, bytes.ReplaceAll([]byte(fixtureDoc()), []byte("\n"), []byte("\r\n"))...)
	path := writeAgents(t, t.TempDir(), "")
	if err := os.WriteFile(path, rawBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	normalized := bytes.ReplaceAll(bytes.TrimPrefix(rawBytes, []byte{0xEF, 0xBB, 0xBF}), []byte("\r\n"), []byte("\n"))
	normHash := sha256.Sum256(normalized)
	norm := hex.EncodeToString(normHash[:])
	prov := provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &norm, ByteSize: sizePtr(int64(len(normalized)))})
	report, err := Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: prov, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	if report.SuppliedInputs[0].RawSHA256 == norm || report.Receipts[0].RawBytesReceived || report.Receipts[0].MergedBodyReceived {
		t.Fatal("normalized file bytes were accepted as the raw file")
	}
	raw := fileSHA(t, path)
	prov = provenanceJSON(t, harness.ProvenanceItem{Kind: "agents", LogicalName: "AGENTS.md", HashKind: "content", ContentSHA256: &raw, ByteSize: sizePtr(int64(len(rawBytes)))})
	report, err = Compare(context.Background(), Input{Context: rulesimport.ContextProject, Files: []string{path}, Provenance: prov, ExpectedSessionID: fixtureSession})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Receipts[0].RawBytesReceived {
		t.Fatal("raw BOM bytes were not the receipt")
	}
}

func fixtureDoc() string {
	return "## Checks\n<!-- aeon-rule: fixture.check -->\n- " + fixtureText + "\n  Why: tests need a reason.\n  source: AEON-251\n"
}

func writeAgents(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func mustProposal(t *testing.T, path string) rulesimport.Proposal {
	t.Helper()
	p, err := rulesimport.Build(context.Background(), rulesimport.Request{Context: rulesimport.ContextProject, Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func publishedMerge(t *testing.T, identity, text, revision string) []byte {
	t.Helper()
	floor := "- [safety] " + floorMarker + "\n"
	body := "# Aeon session rules\n\n" + floor + "- [" + identity + "] " + text + "\n"
	sum := sha256.Sum256([]byte(body))
	merged := rules.Merged{
		Context:  rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex"},
		Versions: []rules.VersionRef{{SetID: "10000000-0000-4000-8000-000000000005", Version: "260928141500.0.0", SHA256: strings.Repeat("ab", 32)}},
		Version:  "260928141500.0.0", SHA256: hex.EncodeToString(sum[:]), Body: body, ByteSize: len(body), Floor: floor,
		Rules: []rules.Rule{
			{Identity: "safety", Text: floorMarker, Why: whyMarker, Strength: "locked", Enabled: true, Source: rules.Source{Reference: "test:floor"}},
			{Identity: identity, Text: text, Why: whyMarker, Strength: "normal", Enabled: true, Source: rules.Source{Reference: "doctrine:test", Revision: revision, Identity: "fixture.check"}},
		},
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func provenanceJSON(t *testing.T, item harness.ProvenanceItem) []byte {
	t.Helper()
	return mustEncode(t, harness.ProvenanceRevision{ID: "10000000-0000-4000-8000-000000000010", SessionID: fixtureSession, Revision: 1, SetSHA256: strings.Repeat("ab", 32), RecordedBy: "10000000-0000-4000-8000-000000000011", RecordedAt: time.Now().UTC(), Items: []harness.ProvenanceItem{item}})
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sizePtr(n int64) *int64 { return &n }

func assertLimits(t *testing.T, report Report) {
	t.Helper()
	want := Limits{SuppliedFilesAreExpectedInputs: true, SuppliedMetadataIsNotServerProof: true, NameMatchIsNotComparison: true, NormalizedHashIsNotRawBytes: true}
	if report.Schema != Schema || report.Limits != want || report.Limits.ModelLoadVerified || report.Limits.ModelObedienceVerified || report.Limits.ExecutionVerified || report.Limits.WaitingWindow || report.Limits.ActiveInstructionReplaced || report.Limits.RolloutAuthorized {
		t.Fatalf("limits %+v", report.Limits)
	}
	for _, kind := range []string{"model_loaded", "model_obeyed", "execution_verified"} {
		if !hasGap(report.MissingRuntimeEvidence, kind) {
			t.Fatalf("missing %s in %+v", kind, report.MissingRuntimeEvidence)
		}
	}
}

func mustJSON(t *testing.T, report Report) string {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func has(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func hasItem(items []Item, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func hasGap(items []Gap, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiff(items []Difference, kind, identity string) bool {
	for _, item := range items {
		if item.Kind == kind && item.Identity == identity {
			return true
		}
	}
	return false
}
