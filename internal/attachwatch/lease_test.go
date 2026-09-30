// SPDX-License-Identifier: AGPL-3.0-only
package attachwatch

import "testing"

func TestLeaseSnapshotHasNoTranscriptAndDigestBindsMode(t *testing.T) {
	s := Snapshot{Mode: ModeLease, Host: "host", Harness: "codex", Process: Process{PID: 12, UID: 501, Started: "123:456", Executable: "/bin/codex", CWD: "/work/repo"}, Platform: "darwin"}
	if !s.Valid() {
		t.Fatal("metadata snapshot rejected")
	}
	for _, change := range []func(*Snapshot){
		func(s *Snapshot) { s.Transcript = "/any/file" }, func(s *Snapshot) { s.FileID = "1:2" },
		func(s *Snapshot) { s.Mode = "unrecognized" }, func(s *Snapshot) { s.Mode = "" },
	} {
		bad := s
		change(&bad)
		if bad.Valid() {
			t.Fatal("invalid content mode accepted")
		}
		if bad.Digest() == s.Digest() {
			t.Fatal("mode/content not bound")
		}
	}
}
