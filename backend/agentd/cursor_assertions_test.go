// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"strings"
	"testing"
)

func assertNoCursorPublicContent(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	body := string(raw)
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("public payload leaked %q: %s", secret, body)
		}
	}
}
