// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliArgs() []string {
	return []string{"--source", "codex", "--model", "gpt-6-luna", "--session-id", testSession, "--source-session-id", "thread-1", "--from-start"}
}
func TestRunRetryAndCheckpoint(t *testing.T) {
	body := wrapDeltas(`{"type":"turn.completed","usage":{"input_tokens":3,"cached_input_tokens":1,"output_tokens":1}}`)
	var first, retry, stderr bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &retry} {
		if err := Run(cliArgs(), strings.NewReader(body), out, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != retry.String() || stderr.Len() != 0 {
		t.Fatal("retry changed output")
	}
	var doc Result
	if err := json.Unmarshal(first.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Reports[0].Provisional || !uuidRE.MatchString(doc.Reports[0].ReportID) {
		t.Fatalf("report: %+v", doc.Reports[0])
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(dir, "checkpoint.json")
	b, _ := json.Marshal(doc.Checkpoint)
	if err := os.WriteFile(checkpoint, b, 0600); err != nil {
		t.Fatal(err)
	}
	var resumed bytes.Buffer
	if err := Run(append(cliArgs(), "--checkpoint-file", checkpoint), strings.NewReader(body), &resumed, &stderr); err != nil {
		t.Fatal(err)
	}
	if first.String() != resumed.String() {
		t.Fatal("checkpoint retry changed output")
	}
}
func TestRunRejectsRemovedUnsafeModesAndBadArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"--submit", "/usr/bin/true"}, {"--prior-file", "MUST_NOT_LEAK"}, {"MUST_NOT_LEAK"}, append(cliArgs(), "--source", "codex")} {
		var out, errOut bytes.Buffer
		err := Run(args, strings.NewReader(""), &out, &errOut)
		var usage *UsageError
		if !errors.As(err, &usage) || strings.Contains(err.Error(), "MUST_NOT_LEAK") || out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("error: %v", err)
		}
	}
}
func checkpointJSON(t *testing.T, n int) []byte {
	t.Helper()
	b, err := json.Marshal(Checkpoint{Binding: strings.Repeat("a", 64), Bytes: n, Digest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCheckpoint(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckpointPathSafetyAndSchema(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "checkpoint.json")
	if err := os.WriteFile(good, checkpointJSON(t, 1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCheckpoint(good); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "data.key", "id_test", ".codex", ".cursor", ".ssh", ".inspr", ".aws", ".gnupg", "secrets", "credentials", "auth.json"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, checkpointJSON(t, 2), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readCheckpoint(path); err == nil {
			t.Fatal("forbidden path accepted")
		}
		if _, err := readCheckpoint(path + "/../checkpoint.json"); err == nil {
			t.Fatal("cleaning erased forbidden component")
		}
	}
	bad := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(bad, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, dir, bad} {
		if _, err := readCheckpoint(path); err == nil {
			t.Fatal("unsafe path or schema accepted")
		}
	}
}
