// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type repeatingReader struct{ remaining int64 }

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	return int(n), nil
}
func TestRawResponseInclusiveCap(t *testing.T) {
	for _, n := range []int64{32, 33} {
		body := &repeatingReader{remaining: n}
		raw, err := readRawResponse(&http.Response{StatusCode: 200, Body: io.NopCloser(body)}, 32)
		if n == 32 && (err != nil || len(raw) != 32) {
			t.Fatal("exact limit refused")
		}
		if n == 33 && (err == nil || raw != nil) {
			t.Fatal("oversized response returned a successful prefix")
		}
	}
}
func TestCurlRejectsResponseOverCapWithoutOutput(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, &repeatingReader{remaining: maxRawResponseBytes + 1})
	}))
	defer server.Close()
	t.Setenv("AEON_URL", server.URL)
	t.Setenv("AEON_API_KEY", testKey)
	code, out, _ := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing.yaml"), "curl", "/api/bytes"}, "")
	if code == 0 || out != "" {
		t.Fatal("oversized curl response escaped as a successful prefix")
	}
}
func TestAttachmentLookupIsOneRequest(t *testing.T) {
	for _, nodes := range []int{1, 2000} {
		t.Run(fmt.Sprint(nodes), func(t *testing.T) {
			isolate(t)
			id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/api/nodes" {
					items := make([]apiNode, nodes)
					for i := range items {
						items[i].ID = fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", i)
					}
					_ = json.NewEncoder(w).Encode(nodePage{Items: items})
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/nodes/") {
					attachments := []attachmentView{}
					if strings.Contains(r.URL.Path, fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nodes-1)) {
						attachments = append(attachments, attachmentView{ID: id, Name: "file", Size: 4})
					}
					_ = json.NewEncoder(w).Encode(attachments)
					return
				}
				if r.URL.Path != "/api/attachments/"+id {
					http.Error(w, "unexpected tenant walk", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(attachmentView{ID: id, Name: "file", Size: 4})
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			code, _, err := runCLI([]string{"aeon", "--config", filepath.Join(t.TempDir(), "missing.yaml"), "--json", "attach", "get", id}, "")
			if code != 0 || calls != 1 {
				t.Fatalf("metadata lookup: code=%d requests=%d error=%s", code, calls, err)
			}
		})
	}
}
func TestAttachmentDownloadPublishesOnlyVerifiedBytes(t *testing.T) {
	for _, content := range []string{"abc", "abcd", "abcde"} {
		t.Run(fmt.Sprint(len(content)), func(t *testing.T) {
			isolate(t)
			id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/content") {
					_, _ = io.WriteString(w, content)
					return
				}
				_ = json.NewEncoder(w).Encode(attachmentView{ID: id, Name: "file", Size: 4})
			}))
			defer server.Close()
			t.Setenv("AEON_URL", server.URL)
			t.Setenv("AEON_API_KEY", testKey)
			dir := t.TempDir()
			destination := filepath.Join(dir, "download")
			if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := runCLI([]string{"aeon", "--config", filepath.Join(dir, "missing.yaml"), "attach", "get", id, "--download", destination}, "")
			data, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if len(content) == 4 {
				if code != 0 || !bytes.Equal(data, []byte(content)) {
					t.Fatalf("valid download failed: %s", errOut)
				}
			} else {
				if code == 0 || string(data) != "original" || strings.Contains(out, "downloaded") {
					t.Fatal("partial or oversized download was published")
				}
			}
			files, err := filepath.Glob(filepath.Join(dir, ".aeon-download-*"))
			if err != nil || len(files) != 0 {
				t.Fatal("download spool leaked")
			}
			code, out, _ = runCLI([]string{"aeon", "--config", filepath.Join(dir, "missing.yaml"), "attach", "get", id, "--download", "-"}, "")
			if len(content) != 4 && (code == 0 || out != "") {
				t.Fatal("invalid bytes escaped to stdout")
			}
		})
	}
}
