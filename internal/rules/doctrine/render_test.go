// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"errors"
	"strings"
	"testing"
)

const (
	fixtureRepo   = "inspr-at/fixture-doctrine"
	fixtureCommit = "1111111111111111111111111111111111111111"
	nextCommit    = "2222222222222222222222222222222222222222"
)

// The kernel uses CRLF on purpose: displayed text must be the exact bytes.
const fixtureKernel = "# AGENTS — Kernel\r\n\r\n## Hard safety\r\n\r\n### Secrets\r\n\r\n<!-- aeon-rule: no-env-dump -->\r\n- 🔴 **NEVER** run `env`.\r\n  Why: it prints secrets.\r\n\r\n- Rotate a leaked secret.\r\n\r\n## Git\r\n\r\n- Small commits.\r\n\r\n## Git\r\n\r\n- Ticket in every message.\r\n"

const fixtureSidecar = `file: {en: The rules no agent may break, de: "Die Regeln, die kein Agent brechen darf"}
sets:
  hard-safety/secrets: {en: Secrets never reach a transcript.}
rules:
  no-env-dump: {en: Never print the environment., basis: 0000000000000000}
  gone-rule: {en: This rule was removed.}
`

func fixtureFiles() map[string]string {
	return map[string]string{
		"docs/AGENTS-KERNEL.md":         fixtureKernel,
		"docs/AGENTS-KERNEL.tldr.yaml":  fixtureSidecar,
		"docs/AGENTS-DOMAIN-DEV.md":     "# Dev\n\n## Tests\n\n- Tests are part of done.\n",
		"docs/AGENTS-INDEX.md":          "# Index\n\n- not a rule file\n",
		"docs/AGENTS-PROFILE-MARKUS.md": "# Profile\n\n- Personal.\n",
		"README.md":                     "# Readme\n",
	}
}

func TestFetchSelectsDoctrineAndSidecars(t *testing.T) {
	fake, client := newFakeGitHub(t)
	fake.commit(fixtureRepo, fixtureCommit, fixtureFiles(), "v260922101217.0.0")
	gh := &GitHub{Client: client}
	commit, err := gh.Commit(t.Context(), fixtureRepo, "v260922101217.0.0")
	if err != nil || commit.SHA != fixtureCommit || commit.CommittedAt == nil {
		t.Fatalf("commit %+v %v", commit, err)
	}
	files, skipped, err := Fetch(t.Context(), gh, fixtureRepo, fixtureCommit, DefaultPaths)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		if BlobSHA(f.Content) != f.BlobSHA {
			t.Fatalf("%s blob sha", f.Path)
		}
	}
	if strings.Join(paths, ",") != "docs/AGENTS-DOMAIN-DEV.md,docs/AGENTS-KERNEL.md,docs/AGENTS-KERNEL.tldr.yaml" {
		t.Fatalf("paths %v", paths)
	}
	if len(skipped) != 1 || skipped[0].Path != "docs/AGENTS-INDEX.md" || skipped[0].Reason != "not a doctrine rule file" {
		t.Fatalf("skipped %+v", skipped)
	}
	// An exact path selects a personal profile; a wildcard never does.
	files, _, err = Fetch(t.Context(), gh, fixtureRepo, fixtureCommit, []string{"docs/AGENTS-PROFILE-MARKUS.md"})
	if err != nil || len(files) != 1 {
		t.Fatalf("exact profile %v %v", files, err)
	}
}

func TestRenderShowsExactBytesAnchorsAndTLDRs(t *testing.T) {
	var files []File
	for path, content := range fixtureFiles() {
		if strings.HasPrefix(path, "docs/AGENTS-KERNEL") {
			files = append(files, File{Path: path, BlobSHA: BlobSHA([]byte(content)), Content: []byte(content)})
		}
	}
	views := Render(fixtureRepo, fixtureCommit, false, files)
	if len(views) != 1 {
		t.Fatalf("views %+v", views)
	}
	kernel := views[0]
	if kernel.Kind != "kernel" || kernel.Layer != "company" || kernel.Problem != "" || kernel.URL != "https://github.com/"+fixtureRepo+"/blob/"+fixtureCommit+"/docs/AGENTS-KERNEL.md" {
		t.Fatalf("file %+v", kernel)
	}
	if kernel.TLDR == nil || kernel.TLDR.DE != "Die Regeln, die kein Agent brechen darf" {
		t.Fatalf("file tldr %+v", kernel.TLDR)
	}
	if len(kernel.Rules) != 4 {
		t.Fatalf("rules %+v", kernel.Rules)
	}
	env := kernel.Rules[0]
	wantSource := "<!-- aeon-rule: no-env-dump -->\r\n- 🔴 **NEVER** run `env`.\r\n  Why: it prints secrets.\r\n"
	if env.Key != "no-env-dump" || env.Source != wantSource || env.StartLine != 7 || env.EndLine != 9 || env.Strength != "locked" {
		t.Fatalf("env rule %+v", env)
	}
	if !strings.Contains(fixtureKernel, env.Source) || env.Anchor != "secrets" || env.Identity != fixtureRepo+"/docs/AGENTS-KERNEL.md#secrets" {
		t.Fatalf("env identity %+v", env)
	}
	if env.URL != "https://github.com/"+fixtureRepo+"/blob/"+fixtureCommit+"/docs/AGENTS-KERNEL.md?plain=1#L7-L9" {
		t.Fatalf("url %s", env.URL)
	}
	if env.TLDR == nil || env.TLDR.EN != "Never print the environment." || !env.TLDR.Check {
		t.Fatalf("a basis that no longer matches must ask for a check: %+v", env.TLDR)
	}
	if rotate := kernel.Rules[1]; rotate.TLDR != nil || rotate.URL[len(rotate.URL)-4:] != "#L11" {
		t.Fatalf("rotate %+v", rotate)
	}
	// GitHub numbers a repeated heading.
	if kernel.Rules[2].Anchor != "git" || kernel.Rules[3].Anchor != "git-1" {
		t.Fatalf("anchors %s %s", kernel.Rules[2].Anchor, kernel.Rules[3].Anchor)
	}
	if len(kernel.Sets) != 2 || kernel.Sets[0].Set != "hard-safety/secrets" || kernel.Sets[0].TLDR == nil || kernel.Sets[0].TLDR.Check {
		t.Fatalf("sets %+v", kernel.Sets)
	}
	if kernel.Sidecar == nil || kernel.Sidecar.Problem != "" || strings.Join(kernel.Sidecar.Unmatched, ",") != "rules.gone-rule" {
		t.Fatalf("sidecar %+v", kernel.Sidecar)
	}
}

func TestSidecarProblemsKeepRules(t *testing.T) {
	for name, sidecar := range map[string]string{
		"unknown field": "rules:\n  a: {en: x, fr: y}\n",
		"no english":    "rules:\n  a: {de: nur deutsch}\n",
		"two lines":     "file: {en: \"one\\ntwo\"}\n",
		"bad basis":     "rules:\n  a: {en: x, basis: XYZ}\n",
		"not yaml":      "rules: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			views := Render(fixtureRepo, fixtureCommit, false, []File{
				{Path: "docs/AGENTS-CORE.md", Content: []byte("# Core\n\n- One rule.\n")},
				{Path: "docs/AGENTS-CORE.tldr.yaml", Content: []byte(sidecar)},
			})
			if len(views) != 1 || len(views[0].Rules) != 1 || views[0].Sidecar == nil || views[0].Sidecar.Problem == "" || views[0].TLDR != nil {
				t.Fatalf("%+v", views)
			}
		})
	}
}

func TestRenderReportsUnindexableFile(t *testing.T) {
	views := Render(fixtureRepo, fixtureCommit, false, []File{{Path: "docs/AGENTS-CORE.md", Content: []byte{0xff, 0xfe}}})
	if len(views) != 1 || views[0].Problem != "not UTF-8 text" || len(views[0].Rules) != 0 {
		t.Fatalf("%+v", views)
	}
}

func TestRawLinesKeepTerminators(t *testing.T) {
	got := rawLines([]byte("a\r\nb\rc\nd"))
	if strings.Join(got, "|") != "a\r\n|b\r|c\n|d" {
		t.Fatalf("%q", got)
	}
}

func TestGitHubRefusesTamperedBlob(t *testing.T) {
	fake, client := newFakeGitHub(t)
	fake.commit(fixtureRepo, fixtureCommit, map[string]string{"docs/AGENTS-CORE.md": "# Core\n"})
	gh := &GitHub{Client: client}
	if _, err := gh.Blob(t.Context(), fixtureRepo, BlobSHA([]byte("# Other\n")), 10); !errors.Is(err, ErrGit) {
		t.Fatalf("missing blob err = %v", err)
	}
	fake.down = true
	_, err := gh.Commit(t.Context(), fixtureRepo, fixtureCommit)
	if !errors.Is(err, ErrGit) || !strings.Contains(err.Error(), "502") {
		t.Fatalf("down err = %v", err)
	}
	if _, err := gh.Commit(t.Context(), "not a repo", "main"); !errors.Is(err, ErrGit) {
		t.Fatalf("invalid repo err = %v", err)
	}
}

func TestValidPatterns(t *testing.T) {
	for _, ok := range [][]string{DefaultPaths, {"AGENTS.md", "docs/AGENTS-*.md"}} {
		if !ValidPatterns(ok) {
			t.Fatalf("%v rejected", ok)
		}
	}
	for _, bad := range [][]string{nil, {"../x.md"}, {"/abs.md"}, {"docs/"}, {"a.md", "a.md"}, {"docs/[.md"}, {"a b.md"}} {
		if ValidPatterns(bad) {
			t.Fatalf("%v accepted", bad)
		}
	}
}
