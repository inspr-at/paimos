// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package targetidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestProjectEnvironmentIdentityPreservesReleaseAcceptanceBytes(t *testing.T) {
	identity := Identity{
		Kind: KindProjectEnvironment, ProjectID: 7, EnvironmentSymbol: "production-eu1", EnvironmentID: 11,
		URL: "https://private.invalid", HostAlias: "private-host", HostIP: "192.0.2.10",
		CreatedAt: "2026-09-01T10:00:00Z", UpdatedAt: "2026-09-09T10:00:00Z",
	}
	want := []byte(`{"kind":"project_environment","project_id":7,"environment_symbol":"production-eu1","environment_id":11,"url":"https://private.invalid","host_alias":"private-host","host_ip":"192.0.2.10","created_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-09T10:00:00Z"}`)
	raw, err := json.Marshal(identity)
	if err != nil || string(raw) != string(want) {
		t.Fatalf("identity bytes=%s want=%s err=%v", raw, want, err)
	}
	digest := sha256.Sum256(want)
	if got := Digest(identity); got != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("identity digest=%s", got)
	}
}
