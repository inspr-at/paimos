// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import "testing"

func TestMarkdownActionTextAndLineage(t *testing.T) {
	cases := []struct {
		name, body, text, topic, heading string
	}{
		{
			name:    "nested emphasis",
			body:    "# Doc\n\n## Git\n\n- **_Never_** force-push.\n  Why: shared history.\n",
			text:    "**_Never_** force-push.",
			topic:   "force-push",
			heading: "Doc / Git",
		},
		{
			name:    "intraword underscores",
			body:    "# Doc\n\n## Files\n\n- Never delete foo__bar.\n  Why: keep the name.\n",
			text:    "Never delete foo__bar.",
			topic:   "delete foo__bar",
			heading: "Doc / Files",
		},
		{
			name:    "single backtick",
			body:    "# Doc\n\n## Files\n\n- Never delete `foo*bar`.\n  Why: keep the name.\n",
			text:    "Never delete `foo*bar`.",
			topic:   "delete foo*bar",
			heading: "Doc / Files",
		},
		{
			name:    "double backtick",
			body:    "# Doc\n\n## Files\n\n- Never delete ``foo*bar``.\n  Why: keep the name.\n",
			text:    "Never delete ``foo*bar``.",
			topic:   "delete foo*bar",
			heading: "Doc / Files",
		},
		{
			name:    "triple backtick",
			body:    "# Doc\n\n## Files\n\n- Never delete ```foo*bar```.\n  Why: keep the name.\n",
			text:    "Never delete ```foo*bar```.",
			topic:   "delete foo*bar",
			heading: "Doc / Files",
		},
		{
			name:    "heading inline code",
			body:    "# `Repo` rules\n\n## Keep `foo*bar`\n\n- Never delete `foo*bar`.\n  Why: keep the name.\n",
			text:    "Never delete `foo*bar`.",
			topic:   "delete foo*bar",
			heading: "Repo rules / Keep foo*bar",
		},
		{
			name:    "empty section",
			body:    "# Doc\n\n## Empty\n\n## Git\n\n- Never force-push.\n  Why: shared history.\n",
			text:    "Never force-push.",
			topic:   "force-push",
			heading: "Doc / Git",
		},
		{
			name:    "directly under title",
			body:    "# Doc\n\n- Never force-push.\n  Why: shared history.\n\n## Empty\n",
			text:    "Never force-push.",
			topic:   "force-push",
			heading: "Doc / ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustBuild(t, Request{Context: ContextProject, Files: []string{
				writeDoc(t, t.TempDir(), "AGENTS.md", tc.body),
			}})
			if len(got.Rules) != 1 {
				t.Fatalf("rules %d", len(got.Rules))
			}
			rule, ok := findText(got, tc.text)
			if !ok {
				t.Fatalf("missing %q", tc.text)
			}
			topic, _, ok := directiveTopic(rule)
			if !ok || topic != tc.topic {
				t.Fatalf("action %q ok=%v", topic, ok)
			}
			if rule.Sources[0].HeadingPath != tc.heading {
				t.Fatalf("lineage %q", rule.Sources[0].HeadingPath)
			}
		})
	}
}

func TestEmptyATXClosingHashesEndPersonalSection(t *testing.T) {
	const kernelText = "Keep commits small."
	const personalText = "Prefer short replies."
	for _, line := range []string{"## ##", "## #", "##\t##", "## ##  "} {
		t.Run(line, func(t *testing.T) {
			body := "# Doc\n\n## Personal section\n\n- " + personalText + "\n\n" + line + "\n\n- " + kernelText + "\n"
			if got := indexATXHeadings(body)[7]; got.level != 2 || got.title != "" {
				t.Fatalf("empty heading line %q indexed %+v", line, got)
			}
			path := writeDoc(t, t.TempDir(), "AGENTS.md", body)
			kernel := mustBuild(t, Request{Context: ContextProject, Section: SectionKernel, Files: []string{path}})
			if len(kernel.Rules) != 1 || kernel.Rules[0].Text != kernelText || kernel.Rules[0].Sources[0].HeadingPath != "Doc / " {
				t.Fatalf("kernel section %+v", kernel.Rules)
			}
			personal := mustBuild(t, Request{Context: ContextPerson, Section: SectionPersonal, Files: []string{path}})
			if len(personal.Rules) != 1 || personal.Rules[0].Text != personalText || personal.Rules[0].Sources[0].HeadingPath != "Doc / Personal section" {
				t.Fatalf("personal section %+v", personal.Rules)
			}
		})
	}
}
