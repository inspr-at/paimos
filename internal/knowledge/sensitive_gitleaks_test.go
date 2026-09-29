// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// leakRuleIDs lists the gitleaks rules that report a finding in text.
func leakRuleIDs(text string) []string {
	var ids []string
	leakSpans(text, strings.ToLower(text), func(id string, _, _ int) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	})
	return ids
}

// synth draws n characters from alphabet with a fixed seed, so the forms are
// stable. None is a real credential.
type synth struct{ r *rand.Rand }

const (
	alnum    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	lowerNum = "abcdefghijklmnopqrstuvwxyz0123456789"
	hexLower = "0123456789abcdef"
	digits   = "0123456789"
)

func (s synth) of(alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[s.r.IntN(len(alphabet))]
	}
	return string(b)
}

// providerForms are the DigitalOcean and Shopify shapes the round-3 review
// found stored in nomination excerpts: documented prefixes, lower-case hex.
func providerForms() map[string]string {
	sum := sha256.Sum256([]byte("AEON-288 round-3 synthetic noncredential fixture"))
	h := hex.EncodeToString(sum[:])
	return map[string]string{
		"digitalocean-pat":                 "dop" + "_v1_" + h,
		"digitalocean-access-token":        "doo" + "_v1_" + h,
		"shopify-access-token":             "shp" + "at_" + h[:32],
		"shopify-private-app-access-token": "shp" + "pa_" + h[:32],
	}
}

// ruleSetForms are synthetic tokens for a sample of other providers in the
// vendored rule set, keyed by the rule expected to find them.
func ruleSetForms() map[string]string {
	s := synth{rand.New(rand.NewPCG(288, 4))}
	return map[string]string{
		"anthropic-api-key":             "sk-ant-" + "api03-" + s.of(alnum, 93) + "AA",
		"openai-api-key":                "sk-proj-" + s.of(alnum, 58) + "T3Blbk" + "FJ" + s.of(alnum, 58),
		"stripe-access-token":           "sk_" + "live_" + s.of(alnum, 24),
		"slack-bot-token":               "xox" + "b-" + s.of(digits, 12) + "-" + s.of(digits, 12) + "-" + s.of(alnum, 24),
		"gitlab-pat":                    "glp" + "at-" + s.of(alnum, 20),
		"pypi-upload-token":             "pypi-" + "AgEIcHlwaS5vcmc" + s.of(alnum, 60),
		"sendgrid-api-token":            "SG." + s.of(alnum, 22) + "." + s.of(alnum, 43),
		"twilio-api-key":                "SK" + s.of(hexLower, 32),
		"databricks-api-token":          "dapi" + s.of(hexLower, 32),
		"doppler-api-token":             "dp.pt." + s.of(alnum, 43),
		"dynatrace-api-token":           "dt0c01." + s.of(alnum, 24) + "." + s.of(alnum, 64),
		"linear-api-key":                "lin_" + "api_" + s.of(alnum, 40),
		"postman-api-token":             "PMAK-" + s.of(hexLower, 24) + "-" + s.of(hexLower, 34),
		"pulumi-api-token":              "pul-" + s.of(hexLower, 40),
		"shippo-api-token":              "shippo_" + "live_" + s.of(hexLower, 40),
		"age-secret-key":                "AGE-SECRET-" + "KEY-1" + s.of("QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L", 58),
		"rubygems-api-token":            "rubygems_" + s.of(hexLower, 48),
		"planetscale-api-token":         "pscale_" + "tkn_" + s.of(alnum, 43),
		"prefect-api-token":             "pnu_" + s.of(alnum, 36),
		"readme-api-token":              "rdme_" + s.of(lowerNum, 70),
		"grafana-service-account-token": "glsa_" + s.of(alnum, 32) + "_" + s.of(hexLower, 8),
		"square-access-token":           "sq0" + "atp-" + s.of(alnum, 22),
		"flyio-access-token":            "fo1_" + s.of(alnum, 43),
	}
}

func TestGitleaksRulesCompile(t *testing.T) {
	rules := leakRules().rules
	if gitleaksTag == "" || len(rules) < 200 {
		t.Fatalf("tag %q, %d rules", gitleaksTag, len(rules))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.spec.id] {
			t.Errorf("duplicate rule %s", r.spec.id)
		}
		seen[r.spec.id] = true
		for _, k := range r.spec.keywords {
			if k != strings.ToLower(k) || len(k) < 2 {
				t.Errorf("%s keyword %q is not lower case of two or more bytes", r.spec.id, k)
			}
		}
	}
}

// Each provider form is found by its own gitleaks rule, and the range is
// exactly the token.
func TestSensitiveProviderForms(t *testing.T) {
	forms := ruleSetForms()
	for id, token := range providerForms() {
		forms[id] = token
	}
	for id, token := range forms {
		t.Run(id, func(t *testing.T) {
			text := "Incident: " + token + " was pasted"
			if ids := leakRuleIDs(text); !slices.Contains(ids, id) {
				t.Errorf("rule %s did not fire, got %v", id, ids)
			}
			spans := sensitiveSpans(text)
			if len(spans) != 1 || text[spans[0].start:spans[0].end] != token {
				t.Errorf("spans %v", spans)
			}
		})
	}
}

// Prose and code that mention providers or credential words stay clean,
// including quoted paths after a credential label.
func TestSensitiveGitleaksProseIsClean(t *testing.T) {
	for _, text := range []string{
		"See dist/assets/paimos-hero-CBPb5sBs.jpg",
		"See web/src/components/releases/Release20260929Panel.vue",
		"token: refresh-token.md",
		"password: `docs/credential-rotation.md`",
		`password: "docs/credential-rotation.md"`,
		"secret: 'internal/knowledge/gitleaks/gitleaks.toml'",
		"Rotate the DigitalOcean token and the Shopify admin token after the incident.",
		"Commit 1c1a5df12c1b154eb791539df2b91d60d01d404d fixed the Stripe webhook.",
		"The Slack bot posts to #ops; the key vault name is aeon-prod-vault.",
		"The api_version: 2026-09 of the Shopify admin API is pinned.",
		"Ask the task runner to skip flaky tests.",
		"primary_key = node_id in the migration.",
	} {
		if spans := sensitiveSpans(text); len(spans) > 0 {
			t.Errorf("flagged %q at %v (rules %v)", text, spans, leakRuleIDs(text))
		}
	}
}

// The tagger never stores a provider token in a nomination excerpt, while
// the same incident without one is nominated.
func TestSensitiveProviderFormsNotStored(t *testing.T) {
	f := setup(t)
	control := commentLearningID(f.ticket, addComment(t, f, f.ticket, "Incident: the deploy failed because the provider token expired"))
	forms := providerForms()
	keys := map[string]string{}
	for id, token := range forms {
		keys[id] = commentLearningID(f.ticket, addComment(t, f, f.ticket, "Incident: "+token))
	}
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := nominationOfKey(t, f, control); !ok {
		t.Fatal("the control incident was not nominated")
	}
	for id, key := range keys {
		if _, excerpt, ok := nominationOfKey(t, f, key); ok {
			t.Errorf("%s stored (excerpt contains token: %v)", id, strings.Contains(excerpt, forms[id]))
		}
	}
	w := call(t, f, f.b, "GET", "/api/knowledge/learnings?project_id="+f.project, nil)
	expect(t, w, 200)
	for _, item := range decode[LearningPage](t, w).Items {
		for id, token := range forms {
			if strings.Contains(item.Text, token) {
				t.Errorf("%s visible in the learning inbox", id)
			}
		}
	}
}

// The detector stays fast on a long body: 64 KiB of prose with credential
// words, paths and one token, under 50 ms once the rules are compiled.
func TestSensitiveDetectorSpeed(t *testing.T) {
	leakRules()
	s := synth{rand.New(rand.NewPCG(288, 5))}
	var b strings.Builder
	lines := []string{
		"The access token expired, so the agent asked for a new API key via the vault.",
		"See internal/knowledge/sensitive.go and web/src/components/knowledge/MethodLearningsV2.vue.",
		"password: see the vault entry; secret rotation is tracked in OPS-240.",
		"Commit " + s.of(hexLower, 40) + " moved the auth middleware.",
		"Deploy key and client secret live in 1Password, never in a comment.",
	}
	for i := 0; b.Len() < 64<<10; i++ {
		b.WriteString(lines[i%len(lines)])
		b.WriteByte('\n')
	}
	text := b.String()[:64<<10-80] + " " + providerForms()["digitalocean-pat"] + "\n"
	best := time.Hour
	for range 5 {
		start := time.Now()
		if !looksSensitive(text) {
			t.Fatal("token in a long body not detected")
		}
		best = min(best, time.Since(start))
	}
	t.Logf("64 KiB in %v", best)
	// Hosted CI runners are several times slower than a workstation (62-74 ms
	// there for release 11); the guard is against pathological backtracking,
	// which costs seconds, so CI gets a wider budget.
	budget := 50 * time.Millisecond
	if os.Getenv("CI") != "" {
		budget = 250 * time.Millisecond
	}
	if best > budget {
		t.Fatalf("64 KiB took %v, want < %v", best, budget)
	}
}
