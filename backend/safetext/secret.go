// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package safetext owns the single secret-like text predicate shared by HTTP
// ingestion, delivery projections, and SQLite backstops.
package safetext

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer[ \t\f\v\r\n\x{00A0}\x{2003}\x{202F}\x{200B}]+(?:[A-Za-z0-9._~+/=-][ \t\f\v\r\n\x{00A0}\x{2003}\x{202F}\x{200B}]*){8,}`),
	regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(api[_-]?key|token|secret|password|passwd|credential)[ \t\f\v\r\n\x{00A0}\x{2003}\x{202F}\x{200B}]*[:=]`),
	regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(api[_-]?key|token|secret|password|passwd|credential)[ \t\f\v\r\n\x{00A0}\x{2003}\x{202F}\x{200B}]*[:/_-](?:[A-Za-z0-9._~+/=-][ \t\f\v\r\n\x{00A0}\x{2003}\x{202F}\x{200B}]*){8,}`),
	regexp.MustCompile(`(?i)\bsk[-_](?:live|test|proj)[-_][A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])sk[-_][A-Za-z0-9_-]{20,}(?:$|[^A-Za-z0-9_])`),
	regexp.MustCompile(`(?i)\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`),
	regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)https?://[^\s/:@]+:[^\s/@]+@`),
}

// ContainsSecretLike is deliberately conservative. Control characters are
// included because legacy/direct-seeded text must fail closed at projection
// even when it predates the normal bounded-text validators.
func ContainsSecretLike(value string) bool {
	if strings.ContainsAny(value, "\x00\r\n") {
		return true
	}
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
