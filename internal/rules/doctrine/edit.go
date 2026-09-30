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

	"golang.org/x/text/unicode/norm"

	"github.com/inspr-at/paimos/internal/workorders"
	"gopkg.in/yaml.v3"
)

// Every literal passes through the same normalizer as the corpus and input.
// Regex syntax and character classes are not normalization input: UTS #39 also
// maps ASCII (m -> rn, 1 -> l, 0 -> O), so raw identity regexes are incorrect.
func identityLiteral(s string) string { return regexp.QuoteMeta(normalizeProposalText(s)) }

func credentialPattern() string {
	labels := []string{"api_key", "api-key", "apikey", "token", "password", "secret"}
	for i := range labels {
		labels[i] = identityLiteral(labels[i])
	}
	return `(?:` + strings.Join(labels, "|") + `)["' ]*[:=]["' ]*[a-z0-9/+=_-]{16,}|` +
		identityLiteral("-----BEGIN ") + `.*` + identityLiteral("PRIVATE KEY-----") + `|` +
		identityLiteral("github_pat_") + `[a-z0-9_]+|` + identityLiteral("gh") + `[` + normalizeProposalText("pousr") + `]_[a-z0-9_]{16,}`
}

func publicIdentityPattern() string {
	patterns := []string{credentialPattern(), `[a-z0-9.-]+\.` + identityLiteral("cm") + `\b`, `[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`}
	for _, literal := range []string{"markus@", "@barta.", "~/.inspr/secrets", "pm.barta", "paimos.agm", "hs.barta", "/Users/", "/home/"} {
		patterns = append(patterns, identityLiteral(literal))
	}
	// Whitespace and Unicode dash punctuation are separator variants; soft
	// hyphens and all other default ignorables have already been removed.
	patterns = append(patterns, identityLiteral("inspr")+`[^a-z0-9]`+identityLiteral("doctrine")+`[^a-z0-9]`+identityLiteral("private"))
	digits := `[` + regexp.QuoteMeta(normalizeProposalText("0123456789")) + `]`
	for _, host := range []string{"hsb", "csb", "agm", "dsc"} {
		patterns = append(patterns, identityLiteral(host)+`[^a-z0-9]{0,3}`+digits)
	}
	patterns = append(patterns, identityLiteral("mbp")+`[^a-z0-9]{0,3}`+digits+`{4}`, identityLiteral("imac")+`[^a-z0-9]{0,3}`+identityLiteral("0"))
	return strings.Join(patterns, "|")
}

// Credentials are forbidden in either repository; private routing permits
// operator identity, never credential values. No proposal-controlled exceptions.
var publicLeaks = regexp.MustCompile(publicIdentityPattern())
var credentialLeaks = regexp.MustCompile(credentialPattern())
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ProposalInput struct {
	automatic bool   // internal job only; cannot be supplied by an HTTP caller
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
	if in.automatic {
		b = append(b, []byte("\nAEON-378-draft")...)
	}
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
		if ambiguousPublicText(text) {
			return fail(422, "ambiguous_text", "This public proposal contains a character whose Unicode compatibility and visual forms disagree. Use ordinary Latin text. Nothing was published.")
		}
		if !latinPublicText(text) {
			return fail(422, "non_latin", "Public proposals may use only Latin letters, including German umlauts and ß, and ASCII digits. Nothing was published.")
		}
		if publicLeaks.MatchString(normalizeProposalText(text)) {
			return fail(422, "public_identity", "This public proposal contains identity-bearing or credential-shaped text. Generalise it, or propose the private rule in inspr-doctrine-private. Nothing was published.")
		}
	}
	return nil
}

// latinPublicText allows Latin letters, including German umlauts and ß, and
// ASCII digits. It judges the compatibility form only, so a superscript, a
// vulgar fraction or an information symbol whose NFKC form is Latin or ASCII
// is accepted. Other letters and digits are refused so a lookalike alphabet
// cannot carry a private quotation into a public proposal.
func latinPublicText(text string) bool {
	text = norm.NFKC.String(text)
	text = strings.Map(func(r rune) rune {
		if isIgnoredFormat(r) {
			return -1
		}
		return r
	}, text)
	for _, r := range text {
		if !unicode.Is(unicode.Latin, r) && !unicode.Is(unicode.Inherited, r) && !unicode.Is(unicode.Common, r) {
			return false
		}
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
			return false
		}
		if unicode.IsNumber(r) && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// editRule changes exactly one indexed rule and its sidecar entry. Parse both
// sides, preserve every other rule, and reject malformed sidecars (no clobber).
func editRule(s Source, files []File, in ProposalInput) (map[string]string, error) {
	changed, _, _, err := editRuleViews(s, files, in)
	return changed, err
}

// editRuleViews is editRule that also returns the rule before and after.
func editRuleViews(s Source, files []File, in ProposalInput) (map[string]string, RuleView, RuleView, error) {
	var target FileView
	for _, v := range Render(s.Repository, s.Commit, s.Visibility == "private", files) {
		if v.Path == in.Path {
			target = v
		}
	}
	if target.Path == "" || target.Problem != "" {
		return nil, RuleView{}, RuleView{}, fail(409, "stale_rule", "The selected doctrine file is not indexed at this pin.")
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
		return nil, RuleView{}, RuleView{}, fail(409, "ambiguous_rule", "This rule key appears more than once; fix its identity in git first.")
	}
	if idx < 0 {
		return nil, RuleView{}, RuleView{}, fail(409, "stale_rule", "The rule changed; reload it before proposing.")
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
		return nil, RuleView{}, RuleView{}, fail(400, "invalid_request", "The changed file exceeds the doctrine size limit.")
	}
	rendered := Render(s.Repository, s.Commit, s.Visibility == "private", []File{{Path: in.Path, Content: []byte(content)}})
	if len(rendered) != 1 || rendered[0].Problem != "" || len(rendered[0].Rules) != len(target.Rules) {
		return nil, RuleView{}, RuleView{}, fail(400, "invalid_request", "Keep exactly one rule in the same section, with its existing marker.")
	}
	next := rendered[0].Rules[idx]
	if next.Set != old.Set || next.Source != replacement || (!strings.HasPrefix(old.Key, "t-") && next.Key != old.Key) {
		return nil, RuleView{}, RuleView{}, fail(400, "invalid_request", "Keep the rule section and explicit marker unchanged.")
	}
	for i, r := range rendered[0].Rules {
		if i != idx && r.Key == next.Key {
			return nil, RuleView{}, RuleView{}, fail(400, "ambiguous_rule", "The edit would share a TL;DR key with another rule; keep rule identities distinct.")
		}
	}
	for i, r := range target.Rules {
		if i != idx && r.Source != rendered[0].Rules[i].Source {
			return nil, RuleView{}, RuleView{}, fail(400, "invalid_request", "An edit may change only its selected rule.")
		}
	}
	sc := sidecarFile{}
	if len(side) > 0 {
		d := yaml.NewDecoder(bytes.NewReader(side))
		d.KnownFields(true)
		var extra any
		if d.Decode(&sc) != nil || d.Decode(&extra) != io.EOF || target.Sidecar == nil || target.Sidecar.Problem != "" {
			return nil, RuleView{}, RuleView{}, fail(409, "invalid_sidecar", "Fix the existing TL;DR sidecar in git before proposing here.")
		}
	}
	if sc.Rules == nil {
		sc.Rules = map[string]sidecarText{}
	}
	delete(sc.Rules, old.Key)
	sc.Rules[next.Key] = sidecarText{EN: strings.TrimSpace(in.TLDR.EN), DE: strings.TrimSpace(in.TLDR.DE), Basis: textBasis(next.Text)}
	encoded, err := yaml.Marshal(sc)
	if err != nil {
		return nil, RuleView{}, RuleView{}, err
	}
	if len(encoded) > MaxSidecarBytes {
		return nil, RuleView{}, RuleView{}, fail(400, "invalid_request", "The TL;DR sidecar exceeds its size limit.")
	}
	if err := guardPublic(s.Repository, in.Source, in.TLDR.EN, in.TLDR.DE, in.Explanation); err != nil {
		return nil, RuleView{}, RuleView{}, err
	}
	return map[string]string{in.Path: content, SidecarPath(in.Path): string(encoded)}, old, next, nil
}
