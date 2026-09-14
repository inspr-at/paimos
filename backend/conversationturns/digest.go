// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package conversationturns

import "crypto/sha256"

func sha256Domain(domain string, body []byte) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	return h.Sum(nil)
}

func sha256Text(body string) string {
	sum := sha256.Sum256([]byte(body))
	const alphabet = "0123456789abcdef"
	out := make([]byte, 64)
	for i, value := range sum {
		out[i*2] = alphabet[value>>4]
		out[i*2+1] = alphabet[value&15]
	}
	return string(out)
}
