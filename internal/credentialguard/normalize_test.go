// SPDX-License-Identifier: AGPL-3.0-only

package credentialguard

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Only synthetic values, assembled at runtime and never printed on failure.
func TestContainsUnicodeCredentials(t *testing.T) {
	for name, text := range map[string]string{
		"cloud":    "AK" + "IA" + strings.Repeat("AB12", 4),
		"provider": "sk-proj-" + strings.Repeat("aB7m", 8),
		"basic":    "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("demo:"+"fake")),
		"password": "password=" + strings.Repeat("x", 5),
	} {
		for form, transform := range map[string]func(string) string{
			"raw": func(s string) string { return s },
			"fullwidth": func(s string) string {
				return strings.Map(func(r rune) rune {
					if r >= '!' && r <= '~' {
						return r + 0xfee0
					}
					return r
				}, s)
			},
			"format": func(s string) string { return strings.Join(strings.Split(s, ""), "\u200b") },
			"confusable": func(s string) string {
				return strings.NewReplacer("B", "\ua7b4", "p", "\u0440").Replace(s)
			},
		} {
			t.Run(name+"/"+form, func(t *testing.T) {
				if !Contains(transform(text)) {
					t.Fatal("synthetic credential form not detected")
				}
			})
		}
	}
}

func TestContainsCaseObfuscatedCredentials(t *testing.T) {
	forms := map[string]string{
		"google":   "AI" + "za" + strings.Repeat("ab12", 8),
		"pem":      "-----BEGIN RSA PRIVATE KEY-----\nsynthetic-only\n-----END RSA PRIVATE KEY-----",
		"jwt":      "eyJ" + strings.Repeat("a", 8) + ".eyJ" + strings.Repeat("b", 8) + "." + strings.Repeat("c", 8),
		"age":      "AGE-SECRET-" + "KEY-1" + strings.Repeat("A", 58),
		"telegram": "telegram: " + strings.Repeat("1", 5) + ":A" + strings.Repeat("b", 34),
	}
	for _, prefix := range []string{"AKIA", "ASIA", "ABIA", "ACCA"} {
		forms["cloud-"+prefix] = prefix + strings.Repeat("AB12", 4)
	}
	for name, text := range forms {
		for form, transform := range map[string]func(string) string{
			"small-capitals": strings.NewReplacer("A", "\u1d00", "B", "\u0299", "I", "\u026a", "J", "\u1d0a", "K", "\u1d0b").Replace,
			"capital-iota":   func(s string) string { return strings.ReplaceAll(s, "I", "\u0196") },
			"mixed":          strings.NewReplacer("A", "\u1d00", "I", "\u0196", "J", "\u1d0a", "K", "\u1d0b").Replace,
		} {
			t.Run(name+"/"+form, func(t *testing.T) {
				if !Contains(transform(text)) {
					t.Fatal("synthetic credential form not detected")
				}
			})
		}
	}
	if !Contains("g\u0196pat-" + strings.Repeat("ab12", 4)) {
		t.Fatal("capital iota lost its existing lowercase L interpretation")
	}
}

func TestCredentialNormalizationPreservesASCII(t *testing.T) {
	const ascii = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-=+/"
	if compatibleText(ascii) != ascii || confusableText(ascii) != ascii {
		t.Fatal("normalization changed ASCII case or token bytes")
	}
	// Latin capital beta is not NFKC folded. Keep its UTS mapping's case even
	// though publication patterns now match without case sensitivity.
	if confusableText("\ua7b4") != "B" {
		t.Fatal("confusable mapping lost case")
	}
	for _, text := range []string{
		"The token: refresh it before retrying.",
		"Use api_key=${API_KEY} in the example.",
		"Authorization: Bearer ${TOKEN}",
		"Run tests before merging. Prüfen vor der Freigabe.",
		"See docs/credential-rotation.md for password guidance.",
		"Use an AKIA prefix, a PEM BEGIN header or an eyJ prefix as format names.",
		"Review \u1d00\u1d0b\u026a\u1d00 and AK\u0196A as typography samples.",
	} {
		if Contains(text) {
			t.Fatal("benign prose refused")
		}
	}
}

func TestCredentialRangesKeepOriginalOffsets(t *testing.T) {
	// Contains has additional publication passes; Ranges must not return
	// normalized offsets against the original field used for confirmation.
	text := "Grüße: password=" + strings.Repeat("x", 5)
	got := Ranges("text", text)
	if len(got) != 1 || got[0] != (Range{Field: "text", Start: 16, End: 21}) {
		t.Fatal("original-text credential offsets changed")
	}
	if got := Ranges("text", "ＡＫＩＡ"+strings.Repeat("ＡＢ１２", 4)); len(got) != 0 {
		t.Fatal("confirmation ranges unexpectedly use normalized offsets")
	}
	if got := Ranges("text", "akia"+strings.Repeat("ab12", 4)); len(got) != 0 {
		t.Fatal("confirmation ranges unexpectedly use publication case folding")
	}
}
