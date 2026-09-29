// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/inspr-at/paimos/internal/workorders"
	"gopkg.in/yaml.v3"
)

// Mirrors inspr-modules/scripts/leak-guard.sh (read 2026-09-29), plus
// general contact/credential shapes. No matched text ever leaves this guard.
// No allowlist: a proposal cannot grant itself a public-surface exception.
var publicLeaks = regexp.MustCompile(`(?i)markus@|@barta\.|[a-z0-9.-]+\.cm\b|~/\.inspr/secrets|(?:api[_-]?key|token|password|secret)["' ]*[:=]["' ]*[A-Za-z0-9/+=_-]{16,}|hsb[0-9]|csb[0-9]|mbp[0-9]{4}|agm[0-9]|dsc[0-9]|imac0|pm\.barta|paimos\.agm|hs\.barta|[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}|-----BEGIN .*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]+|gh[pousr]_[A-Za-z0-9_]{16,}|inspr-doctrine-private|/Users/|/home/`)

// Credentials are forbidden in either repository; private routing permits
// operator identity, never credential values.
var credentialLeaks = regexp.MustCompile(`(?i)(?:api[_-]?key|token|password|secret)["' ]*[:=]["' ]*[A-Za-z0-9/+=_-]{16,}|-----BEGIN .*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]+|gh[pousr]_[A-Za-z0-9_]{16,}`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ProposalInput struct {
	RequestID string `json:"request_id"`
	SourceID  string `json:"source_id"`
	Path      string `json:"path"`
	RuleKey   string `json:"rule_key"`
	RuleSHA   string `json:"rule_sha256"`
	Source    string `json:"source"`
	TLDR      struct {
		EN string `json:"en"`
		DE string `json:"de"`
	} `json:"tldr"`
	Explanation string `json:"explanation"`
}

func (in ProposalInput) validate() error {
	if !workorders.UUID(in.RequestID) || strings.ToLower(in.RequestID) != in.RequestID || !workorders.UUID(in.SourceID) || strings.ToLower(in.SourceID) != in.SourceID || !digestPattern.MatchString(in.RuleSHA) || len(in.Path) > 300 || len(in.RuleKey) > 200 || in.RuleKey == "" {
		return fail(400, "invalid_request", "Name the source, rule digest and a canonical request UUID.")
	}
	if !utf8.ValidString(in.Source+in.Explanation) || strings.ContainsAny(in.Source+in.Explanation, "\x00") || strings.TrimSpace(in.Source) == "" || len(in.Source) > 8000 || strings.TrimSpace(in.Explanation) == "" || len(in.Explanation) > 2000 {
		return fail(400, "invalid_request", "Provide a rule of at most 8000 bytes and an explanation of at most 2000 bytes.")
	}
	if (sidecarText{EN: in.TLDR.EN, DE: in.TLDR.DE}).problem() != "" {
		return fail(400, "invalid_request", "The TL;DR needs an English line; each language is at most 300 bytes.")
	}
	return nil
}

func inputDigest(in ProposalInput) string {
	b, _ := json.Marshal(in)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writableSource(s Source) bool {
	return s.Repository == publicRepository && s.Visibility == "public" || s.Repository == privateRepository && s.Visibility == "private"
}

func guardPublic(repository string, texts ...string) error {
	for _, text := range texts {
		if credentialLeaks.MatchString(normalizeProposalText(text)) {
			return fail(422, "credential_text", "This proposal contains credential-shaped text. Remove credentials before publishing to either repository.")
		}
	}
	if repository != publicRepository {
		return nil
	}
	for _, text := range texts {
		if publicLeaks.MatchString(normalizeProposalText(text)) {
			return fail(422, "public_identity", "This public proposal contains identity-bearing or credential-shaped text. Generalise it, or propose the private rule in inspr-doctrine-private. Nothing was published.")
		}
	}
	return nil
}

// editRule changes exactly one indexed rule and its sidecar entry. Parse both
// sides, preserve every other rule, and reject malformed sidecars (no clobber).
func editRule(s Source, files []File, in ProposalInput) (map[string]string, error) {
	var target FileView
	for _, v := range Render(s.Repository, s.Commit, s.Visibility == "private", files) {
		if v.Path == in.Path {
			target = v
		}
	}
	if target.Path == "" || target.Problem != "" {
		return nil, fail(409, "stale_rule", "The selected doctrine file is not indexed at this pin.")
	}
	var old RuleView
	idx := -1
	keyCount := 0
	for i, r := range target.Rules {
		if r.Key == in.RuleKey {
			keyCount++
		}
		if r.Key == in.RuleKey && r.SHA256 == in.RuleSHA {
			old = r
			idx = i
		}
	}
	if keyCount > 1 {
		return nil, fail(409, "ambiguous_rule", "This rule key appears more than once; fix its identity in git first.")
	}
	if idx < 0 {
		return nil, fail(409, "stale_rule", "The rule changed; reload it before proposing.")
	}
	var original []byte
	var side []byte
	for _, f := range files {
		if f.Path == in.Path {
			original = f.Content
		}
		if f.Path == SidecarPath(in.Path) {
			side = f.Content
		}
	}
	ending := "\n"
	if strings.Contains(old.Source, "\r\n") {
		ending = "\r\n"
	}
	replacement := strings.ReplaceAll(strings.ReplaceAll(in.Source, "\r\n", "\n"), "\r", "\n")
	replacement = strings.TrimRight(replacement, "\n") + "\n"
	if ending != "\n" {
		replacement = strings.ReplaceAll(replacement, "\n", ending)
	}
	lines := rawLines(original)
	content := strings.Join(lines[:old.StartLine-1], "") + replacement + strings.Join(lines[old.EndLine:], "")
	if len(content) > 256<<10 {
		return nil, fail(400, "invalid_request", "The changed file exceeds the doctrine size limit.")
	}
	rendered := Render(s.Repository, s.Commit, s.Visibility == "private", []File{{Path: in.Path, Content: []byte(content)}})
	if len(rendered) != 1 || rendered[0].Problem != "" || len(rendered[0].Rules) != len(target.Rules) {
		return nil, fail(400, "invalid_request", "Keep exactly one rule in the same section, with its existing marker.")
	}
	next := rendered[0].Rules[idx]
	if next.Set != old.Set || next.Source != replacement || (!strings.HasPrefix(old.Key, "t-") && next.Key != old.Key) {
		return nil, fail(400, "invalid_request", "Keep the rule section and explicit marker unchanged.")
	}
	for i, r := range rendered[0].Rules {
		if i != idx && r.Key == next.Key {
			return nil, fail(400, "ambiguous_rule", "The edit would share a TL;DR key with another rule; keep rule identities distinct.")
		}
	}
	for i, r := range target.Rules {
		if i != idx && r.Source != rendered[0].Rules[i].Source {
			return nil, fail(400, "invalid_request", "An edit may change only its selected rule.")
		}
	}
	sc := sidecarFile{}
	if len(side) > 0 {
		d := yaml.NewDecoder(bytes.NewReader(side))
		d.KnownFields(true)
		var extra any
		if d.Decode(&sc) != nil || d.Decode(&extra) != io.EOF || target.Sidecar == nil || target.Sidecar.Problem != "" {
			return nil, fail(409, "invalid_sidecar", "Fix the existing TL;DR sidecar in git before proposing here.")
		}
	}
	if sc.Rules == nil {
		sc.Rules = map[string]sidecarText{}
	}
	delete(sc.Rules, old.Key)
	sc.Rules[next.Key] = sidecarText{EN: strings.TrimSpace(in.TLDR.EN), DE: strings.TrimSpace(in.TLDR.DE), Basis: textBasis(next.Text)}
	encoded, err := yaml.Marshal(sc)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxSidecarBytes {
		return nil, fail(400, "invalid_request", "The TL;DR sidecar exceeds its size limit.")
	}
	if err := guardPublic(s.Repository, in.Source, in.TLDR.EN, in.TLDR.DE, in.Explanation); err != nil {
		return nil, err
	}
	return map[string]string{in.Path: content, SidecarPath(in.Path): string(encoded)}, nil
}

// Normalize only for comparison; the proposed git bytes remain unchanged.
func normalizeProposalText(text string) string {
	text = norm.NFKC.String(text)
	text = strings.Map(func(r rune) rune {
		// Cf includes zero-width joiners/spaces, bidi controls and soft hyphens.
		// Also remove invisible combining selectors and the grapheme joiner.
		if unicode.Is(unicode.Cf, r) || r == '\u034f' || r >= '\ufe00' && r <= '\ufe0f' || r >= '\U000e0100' && r <= '\U000e01ef' {
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(cases.Fold().String(text)), " ")
}

func proposalWords(text string) []string {
	return strings.FieldsFunc(normalizeProposalText(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// Reject whole private entries (including short TLDRs), any eight-word run,
// or >=60% of a private entry's unique four-word shingles with >=8 words
// covered. The latter catches lightly edited quotes without requiring an
// exact whole-rule match. Error messages never identify the private match.
func guardPrivateQuotes(privateTexts []string, texts ...string) error {
	for _, proposed := range texts {
		words := proposalWords(proposed)
		joined := " " + strings.Join(words, " ") + " "
		shingles := map[string]bool{}
		for i := 0; i+4 <= len(words); i++ {
			shingles[strings.Join(words[i:i+4], " ")] = true
		}
		for _, private := range privateTexts {
			pw := proposalWords(private)
			if len(pw) == 0 {
				continue
			}
			blocked := strings.Contains(joined, " "+strings.Join(pw, " ")+" ")
			for i := 0; !blocked && i+8 <= len(pw); i++ {
				blocked = strings.Contains(joined, " "+strings.Join(pw[i:i+8], " ")+" ")
			}
			unique := map[string]bool{}
			matches := 0
			for i := 0; i+4 <= len(pw); i++ {
				key := strings.Join(pw[i:i+4], " ")
				if !unique[key] {
					unique[key] = true
					if shingles[key] {
						matches++
					}
				}
			}
			if blocked || matches >= 5 && matches*100 >= len(unique)*60 {
				return fail(422, "private_doctrine", "This public proposal quotes private doctrine. Generalise the changed text or propose it in the private repository. Nothing was published.")
			}
		}
	}
	return nil
}
