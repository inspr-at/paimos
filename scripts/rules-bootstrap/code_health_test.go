// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeHealthDoctrineProposals(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "code-health-doctrine-proposals.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proposals struct {
		Instructions []string `json:"submission_instructions"`
		Drafts       []struct {
			Rule    int             `json:"rule"`
			TLDRDE  string          `json:"tldr_de"`
			InboxID json.RawMessage `json:"inbox_id"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal(raw, &proposals); err != nil {
		t.Fatal(err)
	}
	t.Run("bilingual submission", func(t *testing.T) {
		var submit, warning string
		for _, instruction := range proposals.Instructions {
			if strings.Contains(instruction, "doctrine propose --repo") {
				submit = instruction
			}
			if strings.Contains(instruction, "Passing --tldr without --tldr-de") {
				warning = instruction
			}
		}
		if !strings.Contains(submit, "--tldr TLDR --tldr-de TLDR_DE") {
			t.Error("submit command must pass both --tldr and --tldr-de")
		}
		if !strings.Contains(warning, "stores an empty German line") {
			t.Error("instructions must warn that --tldr without --tldr-de stores an empty German line")
		}
	})
	t.Run("pending German drafts", func(t *testing.T) {
		if len(proposals.Drafts) != 7 {
			t.Fatalf("want seven code-health drafts, got %d", len(proposals.Drafts))
		}
		for i, draft := range proposals.Drafts {
			t.Run(fmt.Sprintf("rule %d", i+1), func(t *testing.T) {
				if draft.Rule != i+1 {
					t.Errorf("want rule %d, got %d", i+1, draft.Rule)
				}
				if strings.TrimSpace(draft.TLDRDE) == "" {
					t.Error("draft must retain a non-empty tldr_de")
				}
				if !bytes.Equal(bytes.TrimSpace(draft.InboxID), []byte("null")) {
					t.Error("draft inbox_id must be explicitly null until a real submission response")
				}
			})
		}
	})
}

func TestCodeHealthIntroNamesLayoutCompanion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(raw), "## Code health (AEON-574)\n\n")
	if !found {
		t.Fatal("missing AEON-574 code-health section")
	}
	intro, _, found := strings.Cut(section, "\n\n1.")
	if !found {
		t.Fatal("missing numbered code-health rules after the introduction")
	}
	for _, phrase := range []string{"Rules 1–6", "AEON-545", "rule 7", "companion layout rule", "AEON-541"} {
		if !strings.Contains(intro, phrase) {
			t.Errorf("code-health introduction must name %q", phrase)
		}
	}
}
