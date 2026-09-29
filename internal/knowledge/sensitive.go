// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

// Method learnings copy source text (ticket titles, comment bodies, review
// summaries) into nominations, changelog lines and rule drafts, which can be
// read more widely than the source. Text that looks like it holds a
// credential is never copied. The check is deliberately conservative: a false
// positive only means a candidate is not nominated automatically, while a
// miss copies a secret. The word "password" or "token" alone is not flagged;
// a value assigned to one is.
var sensitivePatterns = []*regexp.Regexp{
	// Provider keys and tokens by prefix.
	regexp.MustCompile(`(^|[^A-Za-z0-9])(sk-(proj-|ant-|live-|test-)?[A-Za-z0-9_-]{16,}|sk_(live|test)_[A-Za-z0-9]{16,}|rk_(live|test)_[A-Za-z0-9]{16,}|xai-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|xox[abeoprs]-[A-Za-z0-9-]{10,}|ya29\.[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{30,}|aeon_[A-Za-z0-9_]{16,}|npm_[A-Za-z0-9]{30,}|hf_[A-Za-z0-9]{30,})`),
	// AWS access key ids.
	regexp.MustCompile(`(^|[^A-Za-z0-9])(AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}([^A-Za-z0-9]|$)`),
	// JSON web tokens.
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// PEM blocks: private keys, certificates, anything armoured.
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+-----`),
	// Authorization header values.
	regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(bearer|basic)\s+[A-Za-z0-9._~+/=-]{16,}`),
	// Credentials in a URL.
	regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`),
	// A value assigned to a credential-like name: password=..., "api_key": "...".
	regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(passw(or)?d|pwd|passphrase|secret|client[_-]?secret|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|api[_-]?key|apikey|access[_-]?key|secret[_-]?key|private[_-]?key)["']?\s*[:=]\s*["']?[^\s"',;]{6,}`),
}

// opaqueRun is a long run of token characters, the shape of a random key.
var opaqueRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)

// looksSensitive reports whether text appears to contain a credential.
func looksSensitive(text string) bool {
	if text == "" {
		return false
	}
	for _, re := range sensitivePatterns {
		if re.MatchString(text) {
			return true
		}
	}
	for _, run := range opaqueRun.FindAllString(text, -1) {
		if highEntropy(run) {
			return true
		}
	}
	return false
}

// highEntropy flags a mixed-case alphanumeric run with random-looking
// characters. Hex digests, UUIDs and paths lack upper case, lower case and
// digits together, so commit ids and file paths are not flagged.
func highEntropy(run string) bool {
	trimmed := strings.Trim(run, "=/-_+")
	if len(trimmed) < 32 {
		return false
	}
	var upper, lower, digit bool
	counts := map[rune]int{}
	for _, r := range trimmed {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
		counts[r]++
	}
	if !upper || !lower || !digit {
		return false
	}
	n := float64(len(trimmed))
	entropy := 0.0
	for _, c := range counts {
		p := float64(c) / n
		entropy -= p * math.Log2(p)
	}
	return entropy >= 4.0
}

// sourceHash is the revision of the text a nomination's excerpt came from.
// A stored excerpt is served only while its source still hashes the same.
func sourceHash(material string) string {
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
