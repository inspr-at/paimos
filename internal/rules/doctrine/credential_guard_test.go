// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Every value is assembled from synthetic fixtures, never a real credential.
func publicationCredentialForms() map[string]string {
	sum := sha256.Sum256([]byte("AEON-561 synthetic credential fixture"))
	h := hex.EncodeToString(sum[:])
	return map[string]string{
		"provider":          "sk-proj-" + strings.Repeat("Ab7q", 8),
		"cloud":             "AK" + "IA" + strings.Repeat("AB12", 4),
		"cloud-confusable":  "AK" + "IA" + strings.Repeat("A\ua7b412", 4),
		"cloud-small-caps":  "\u1d00\u1d0b\u026a\u1d00" + strings.Repeat("AB12", 4),
		"cloud-iota":        "AK\u0196A" + strings.Repeat("AB12", 4),
		"cloud-mixed":       "\u1d00\u1d0b\u0196\u1d00" + strings.Repeat("AB12", 4),
		"pem-small-caps":    "-----\u0299EGIN RSA PR\u026aVATE KEY-----\nsynthetic-only\n-----END RSA PRIVATE KEY-----",
		"pem-iota":          "-----BEG\u0196N RSA PR\u0196VATE KEY-----\nsynthetic-only\n-----END RSA PRIVATE KEY-----",
		"jwt-small-caps":    "ey\u1d0a" + strings.Repeat("a", 8) + ".ey\u1d0a" + strings.Repeat("b", 8) + "." + strings.Repeat("c", 8),
		"vendored-provider": "dop" + "_v1_" + h,
		"github":            "gh" + "p_" + strings.Repeat("a1B2", 8),
		"private-key":       "-----BEGIN RSA PRIVATE KEY-----\n" + "synthetic-only\n-----END RSA PRIVATE KEY-----",
		"short-password":    "password=" + strings.Repeat("x", 5),
		"markdown-label":    "**Password:** " + "Synth" + "etic42!",
		"code-label":        "`password`: " + "Synth" + "etic42!",
		"basic":             "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("demo:"+"fake")),
		"bearer":            "Authorization: Bearer " + "abcDEF1234" + "56ghiJKL789",
		"url":               "https://deploy:" + "synthetic" + "@example.test/repo",
	}
}

func requireCredentialRefusal(t *testing.T, err error) {
	t.Helper()
	var refusal *failure
	if !errors.As(err, &refusal) || refusal.Status != 422 || refusal.Code != "credential_text" {
		t.Fatal("expected a generic credential refusal")
	}
	if refusal.Message != "This proposal contains credential-shaped text. Remove credentials before publishing to either repository." {
		t.Fatal("credential refusal must return only the fixed message")
	}
}

func TestPublicationCredentialGuard(t *testing.T) {
	for _, repo := range []string{publicRepository, privateRepository} {
		for name, text := range publicationCredentialForms() {
			t.Run(repo+"/"+name, func(t *testing.T) {
				requireCredentialRefusal(t, New(nil, Options{}).guardPublic(repo, text))
				requireCredentialRefusal(t, New(nil, Options{}).guardPublic(repo, fullwidth(text)))
				requireCredentialRefusal(t, New(nil, Options{}).guardPublic(repo, strings.Join(strings.Split(text, ""), "\u200b")))
				// Latin capital beta survives the public Latin check and NFKC.
				// Keep its existing coverage beside the small-capital forms.
				requireCredentialRefusal(t, New(nil, Options{}).guardPublic(repo, strings.ReplaceAll(text, "B", "\ua7b4")))
			})
		}
	}
}

func TestPublicationCredentialProse(t *testing.T) {
	for _, repo := range []string{publicRepository, privateRepository} {
		for i, text := range []string{
			"Never publish private keys or passwords.",
			"The token: refresh it before retrying.",
			"Use api_key=${API_KEY} in the example.",
			"Authorization: Bearer ${TOKEN}",
			"Password: see the vault entry.",
			"See docs/credential-rotation.md for password guidance.",
			"Run tests before merging. Prüfen vor der Freigabe.",
			"Use an AKIA prefix, a PEM BEGIN header or an eyJ prefix as format names.",
		} {
			if err := New(nil, Options{}).guardPublic(repo, text); err != nil {
				t.Errorf("ordinary prose fixture %d refused: %v", i, err)
			}
		}
	}
	// Keep the existing confusable/skeleton defense in the private route too.
	requireCredentialRefusal(t, New(nil, Options{}).guardPublic(privateRepository, "pаssword="+strings.Repeat("A9", 10)))
}

func TestPublicationCredentialsInUnchangedBlobs(t *testing.T) {
	for _, repo := range []string{publicRepository, privateRepository} {
		for _, location := range []string{"source", "sidecar", "path"} {
			t.Run(repo+"/"+location, func(t *testing.T) {
				path := "docs/AGENTS-KERNEL.md"
				token := "sk-proj-" + strings.Repeat("Ab7q", 8)
				if location == "path" {
					path = token + "/docs/AGENTS-KERNEL.md"
				}
				content := "# Kernel\n\n## Rules\n<!-- aeon-rule: edit -->\n- Run tests.\n<!-- aeon-rule: existing -->\n- Keep notes.\n"
				side := "rules:\n  existing: {en: Keep notes.}\n"
				if location == "source" {
					content = strings.Replace(content, "Keep notes.", token, 1)
				}
				if location == "sidecar" {
					side = strings.Replace(side, "Keep notes.", token, 1)
				}
				files := []File{{Path: path, Content: []byte(content)}, {Path: SidecarPath(path), Content: []byte(side)}}
				rule := Render(repo, fixtureCommit, repo == privateRepository, files)[0].Rules[0]
				in := ProposalInput{Path: path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: strings.Replace(rule.Source, "Run tests.", "Run tests before merging.", 1), Explanation: "Clarify validation."}
				in.TLDR.EN = "Validate before merging."
				_, err := New(nil, Options{}).editRule(Source{Repository: repo, Commit: fixtureCommit}, files, in)
				requireCredentialRefusal(t, err)
			})
		}
	}
}

// preparePublication is shared by the editor and inbox submission. Its client
// is a tripwire: even fetching App metadata counts as an outbound request.
func TestPublicationCredentialsRefusedBeforeForge(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate synthetic App key")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal("write synthetic App key")
	}
	actor := tenant.Principal{TenantID: "56100000-0000-4000-8000-000000000001", Kind: tenant.Person}
	calls := 0
	m := New(nil, Options{CredentialsDir: dir, App: AppConfig{ID: "8", InstallationID: "9", KeyRef: "app-key", TenantID: actor.TenantID, GateLogin: "independent-gate[bot]", DCOAcknowledged: true}, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("synthetic forge tripwire")
	})}})
	for _, repo := range []string{publicRepository, privateRepository} {
		allowCredential(t, dir, "app-key", actor.TenantID, repo)
		source := Source{Repository: repo, Commit: fixtureCommit}
		files := []File{{Path: "docs/AGENTS-KERNEL.md", Content: []byte("# Kernel\n\n## Rules\n<!-- aeon-rule: test -->\n- Run tests.\n")}}
		rule := Render(repo, fixtureCommit, repo == privateRepository, files)[0].Rules[0]
		base := ProposalInput{Path: files[0].Path, RuleKey: rule.Key, RuleSHA: rule.SHA256, Source: rule.Source, Explanation: "Clarify validation."}
		base.TLDR.EN = "Run tests before merging."
		guard := quoteCorpus(guardRule)
		for name, text := range publicationCredentialForms() {
			for _, field := range []string{"source", "tldr.en", "tldr.de", "explanation"} {
				t.Run(repo+"/"+name+"/"+field, func(t *testing.T) {
					in := base
					switch field {
					case "source":
						in.Source = strings.Replace(in.Source, "Run tests.", strings.ReplaceAll(text, "\n", "\n  "), 1)
					case "tldr.en":
						in.TLDR.EN = strings.ReplaceAll(text, "\n", " ")
					case "tldr.de":
						in.TLDR.DE = strings.ReplaceAll(text, "\n", " ")
					case "explanation":
						in.Explanation = text
					}
					calls = 0
					_, _, _, err := m.preparePublication(t.Context(), actor, source, files, guard, in)
					requireCredentialRefusal(t, err)
					if calls != 0 {
						t.Fatal("credential refusal happened after a forge request")
					}
				})
			}
		}
		// A benign proposal reaches the tripwire: no earlier unrelated gate can
		// make the credential cases above pass without exercising this boundary.
		calls = 0
		_, _, _, _ = m.preparePublication(t.Context(), actor, source, files, guard, base)
		if calls != 1 {
			t.Fatal("benign control did not reach the synthetic forge")
		}
	}
}
