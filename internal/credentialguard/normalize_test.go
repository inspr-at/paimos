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

func TestCredentialNormalizationPreservesASCII(t *testing.T) {
	const ascii = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-=+/"
	if compatibleText(ascii) != ascii || confusableText(ascii) != ascii {
		t.Fatal("normalization changed ASCII case or token bytes")
	}
	// Latin capital beta is not NFKC folded. Its UTS mapping must retain B,
	// otherwise the uppercase-only cloud-key pattern cannot recognize it.
	if confusableText("\ua7b4") != "B" {
		t.Fatal("confusable mapping lost case")
	}
	for _, text := range []string{
		"The token: refresh it before retrying.",
		"Use api_key=${API_KEY} in the example.",
		"Authorization: Bearer ${TOKEN}",
		"Run tests before merging. Prüfen vor der Freigabe.",
		"See docs/credential-rotation.md for password guidance.",
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
}
