// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContradictionReport is the local markdown and JSON contradictions artifact.
// It does not publish rules and it does not drop either side of a conflict.
type ContradictionReport struct {
	PlanID         string          `json:"plan_id"`
	Contradictions []Contradiction `json:"contradictions"`
}

// NewReport copies the proposal's contradictions. An empty list is a real report.
func NewReport(p Proposal) ContradictionReport {
	items := append([]Contradiction(nil), p.Contradictions...)
	if items == nil {
		items = []Contradiction{}
	}
	return ContradictionReport{PlanID: p.PlanID, Contradictions: items}
}

// JSON is indented and ends with a newline.
func (r ContradictionReport) JSON() ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Markdown is a stable reading of the same contradictions as JSON.
func (r ContradictionReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Doctrine contradictions\n\nPlan: `%s`\n\nContradictions: %d\n", r.PlanID, len(r.Contradictions))
	if len(r.Contradictions) == 0 {
		b.WriteString("\nNo same-identity differences and no conflicting directives across layers.\n")
		return b.String()
	}
	for i, item := range r.Contradictions {
		fmt.Fprintf(&b, "\n## %d. %s\n\n", i+1, item.Kind)
		if item.Topic != "" {
			fmt.Fprintf(&b, "- Topic: `%s`\n", item.Topic)
		}
		if item.Identity != "" && item.Identity != item.Topic {
			fmt.Fprintf(&b, "- Identity: `%s`\n", item.Identity)
		}
		if len(item.Layers) > 0 {
			fmt.Fprintf(&b, "- Layers: %s\n", strings.Join(item.Layers, ", "))
		}
		if len(item.Headings) > 0 {
			fmt.Fprintf(&b, "- Headings: %s\n", strings.Join(item.Headings, "; "))
		}
		if len(item.Fields) > 0 {
			fmt.Fprintf(&b, "- Fields: %s\n", strings.Join(item.Fields, ", "))
		}
		fmt.Fprintf(&b, "- Rules: %s\n", strings.Join(item.RuleIDs, ", "))
		fmt.Fprintf(&b, "- Note: %s\n", item.Note)
	}
	return b.String()
}

// WriteReport creates contradictions.md and contradictions.json in dir.
// Existing files are refused. The directory is created when it is missing.
// Secret-store path components are refused by the same path fence as doctrine reads.
func WriteReport(dir string, p Proposal) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("report directory is required")
	}
	clean, err := cleanPath(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: report directory must be a real directory", ErrNotRegular)
	}
	report := NewReport(p)
	md := []byte(report.Markdown())
	js, err := report.JSON()
	if err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(clean, "contradictions.md"), md); err != nil {
		return err
	}
	if err := writeNewFile(filepath.Join(clean, "contradictions.json"), js); err != nil {
		return err
	}
	return nil
}

func writeNewFile(path string, body []byte) error {
	if err := pathProhibited(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
