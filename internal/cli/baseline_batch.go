// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type builtReceipt struct {
	IdempotencyKey                       string `json:"idempotency_key"`
	ExpectedAttemptID                    int64  `json:"expected_attempt_id"`
	ExpectedPlanRevision                 int64  `json:"expected_plan_revision"`
	ExpectedImplementationExecution      int64  `json:"expected_implementation_execution"`
	ExpectedImplementationAuthorityEpoch int64  `json:"expected_implementation_authority_epoch"`
	ExpectedAccountKey                   string `json:"expected_account_key,omitempty"`
	ExpectedRuntimeGeneration            string `json:"expected_runtime_generation,omitempty"`
	Commit                               string `json:"commit"`
	OCIConfigDigest                      string `json:"oci_config_digest"`
	ReleaseManifestDigest                string `json:"release_manifest_digest"`
	ReleaseManifestCoordinate            string `json:"release_manifest_coordinate"`
	OCIIndexDigest                       string `json:"oci_index_digest,omitempty"`
	VersionScheme                        string `json:"version_scheme"`
	ReleaseChannel                       string `json:"release_channel"`
	ReleaseSequence                      int64  `json:"release_sequence"`
	Version                              string `json:"version"`
	QADigest                             string `json:"qa_digest"`
}

var (
	builtIDRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{7,79}$`)
	builtCommitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	builtDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	builtCoordRE  = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}:[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,189}$`)
	builtSymbolRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	builtVerRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

func (rt *runtime) cmdBaselineBatch() *Command {
	return &Command{Name: "baseline-batch", Short: "Report typed baseline-batch built evidence", Use: "baseline-batch <report-built>", subs: []*Command{rt.cmdBaselineReportBuilt()}}
}

func (rt *runtime) cmdBaselineReportBuilt() *Command {
	var project, batchID, receiptFile string
	var attempt, plan, execution, epoch, sequence int
	var dryRun bool
	var value builtReceipt
	return &Command{Name: "report-built", Short: "Record typed built-artifact and scoped QA evidence", Use: "baseline-batch report-built --project KEY --batch-id ID [--receipt-file PATH | identity flags]", addFlags: func(fs *flagSet) {
		fs.string(&project, "project", 'p', "project key or id (required)")
		fs.string(&batchID, "batch-id", 0, "classic baseline batch id")
		fs.string(&receiptFile, "receipt-file", 0, "JSON receipt file, or - for stdin")
		fs.string(&value.IdempotencyKey, "idempotency-key", 0, "idempotency key")
		fs.int(&attempt, "expected-attempt-id", "expected delivery attempt")
		fs.int(&plan, "expected-plan-revision", "expected plan revision")
		fs.int(&execution, "expected-implementation-execution", "expected implementation execution")
		fs.int(&epoch, "expected-implementation-authority-epoch", "expected implementation epoch")
		fs.string(&value.ExpectedAccountKey, "expected-account-key", 0, "selected account key")
		fs.string(&value.ExpectedRuntimeGeneration, "expected-runtime-generation", 0, "selected runtime generation")
		fs.string(&value.Commit, "commit", 0, "source commit")
		fs.string(&value.OCIConfigDigest, "oci-config-digest", 0, "OCI config digest")
		fs.string(&value.ReleaseManifestDigest, "release-manifest-digest", 0, "release-set digest")
		fs.string(&value.ReleaseManifestCoordinate, "release-coordinate", 0, "release-set coordinate")
		fs.string(&value.OCIIndexDigest, "oci-index-digest", 0, "optional OCI index digest")
		fs.string(&value.VersionScheme, "scheme", 0, "version scheme")
		fs.string(&value.ReleaseChannel, "channel", 0, "release channel")
		fs.int(&sequence, "sequence", "release sequence")
		fs.string(&value.Version, "version", 0, "release version")
		fs.string(&value.QADigest, "qa-digest", 0, "scoped QA digest")
		fs.bool(&dryRun, "dry-run", 0, "print request without writing")
	}, run: func([]string) error {
		if strings.TrimSpace(project) == "" {
			return usagef("--project is required")
		}
		id, err := strconv.ParseInt(batchID, 10, 64)
		if err != nil || id < 1 {
			return usagef("--batch-id must be a positive integer")
		}
		if receiptFile != "" {
			if value != (builtReceipt{}) || attempt != 0 || plan != 0 || execution != 0 || epoch != 0 || sequence != 0 {
				return usagef("use either --receipt-file or explicit identity flags, not both")
			}
			raw, err := rt.readText("", receiptFile, "receipt")
			if err != nil {
				return err
			}
			value, err = decodeBuiltReceipt([]byte(raw))
			if err != nil {
				return usagef("invalid receipt JSON")
			}
		} else {
			value.ExpectedAttemptID = int64(attempt)
			value.ExpectedPlanRevision = int64(plan)
			value.ExpectedImplementationExecution = int64(execution)
			value.ExpectedImplementationAuthorityEpoch = int64(epoch)
			value.ReleaseSequence = int64(sequence)
		}
		if err := validateBuiltReceipt(value); err != nil {
			return err
		}
		classicPath := fmt.Sprintf("/api/projects/%s/baseline-batches/batches/%d/built-receipt", strings.TrimSpace(project), id)
		if dryRun {
			return rt.printJSON(map[string]any{"method": http.MethodPost, "path": classicPath, "body": value})
		}
		proj, err := rt.projectNode(project)
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/api/projects/%s/baseline-batches/batches/%d/built-receipt", url.PathEscape(proj.ID), id)
		var batch map[string]any
		if err := rt.do(http.MethodPost, path, value, &batch); err != nil {
			return err
		}
		if rt.jsonOut {
			return rt.printJSON(batch)
		}
		next := ""
		if progress, ok := batch["progress"].(map[string]any); ok {
			next, _ = progress["next_action"].(string)
		}
		fmt.Fprintf(rt.stdout, "batch %d recorded built receipt (next %s)\n", id, next)
		return nil
	}}
}

func decodeBuiltReceipt(raw []byte) (builtReceipt, error) {
	var out builtReceipt
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return out, fmt.Errorf("receipt must be an object")
	}
	allowed := map[string]bool{}
	for _, key := range []string{"idempotency_key", "expected_attempt_id", "expected_plan_revision", "expected_implementation_execution", "expected_implementation_authority_epoch", "expected_account_key", "expected_runtime_generation", "commit", "oci_config_digest", "release_manifest_digest", "release_manifest_coordinate", "oci_index_digest", "version_scheme", "release_channel", "release_sequence", "version", "qa_digest"} {
		allowed[key] = true
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out, err
		}
		key, ok := tok.(string)
		if !ok || !allowed[key] || seen[key] {
			return out, fmt.Errorf("unknown or duplicate field")
		}
		seen[key] = true
		var unused json.RawMessage
		if err := dec.Decode(&unused); err != nil {
			return out, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return out, err
	}
	if _, err := dec.Token(); !errorsIsEOF(err) {
		return out, fmt.Errorf("extra JSON")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

func errorsIsEOF(err error) bool { return err == io.EOF }

func validateBuiltReceipt(v builtReceipt) error {
	digest := func(value string) bool { return builtDigestRE.MatchString(strings.TrimPrefix(value, "sha256:")) }
	if !builtIDRE.MatchString(v.IdempotencyKey) || v.ExpectedAttemptID < 1 || v.ExpectedPlanRevision < 1 ||
		v.ExpectedImplementationExecution < 0 || v.ExpectedImplementationAuthorityEpoch < 0 ||
		(v.ExpectedImplementationExecution == 0) != (v.ExpectedImplementationAuthorityEpoch == 0) ||
		!builtCommitRE.MatchString(v.Commit) || !digest(v.OCIConfigDigest) || !digest(v.ReleaseManifestDigest) ||
		(v.OCIIndexDigest != "" && !digest(v.OCIIndexDigest)) || !digest(v.QADigest) ||
		!builtCoordRE.MatchString(v.ReleaseManifestCoordinate) || !builtSymbolRE.MatchString(v.ReleaseChannel) ||
		!builtVerRE.MatchString(v.Version) || v.ReleaseSequence < 0 ||
		v.VersionScheme != "legacy" && v.VersionScheme != "inspr-calendar-v1" && v.VersionScheme != "inspr-calendar-v2" {
		return usagef("invalid built receipt")
	}
	return nil
}
