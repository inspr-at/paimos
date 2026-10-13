// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Risk: intermediary rewrites (including same-size corruption) must never be
// published as a successful download, and valid files must retain every byte.
func TestAttachmentDownloadIntegrity(t *testing.T) {
	const id = "41ea1228-e717-4e40-8025-cb212f05773e"
	metadataPath := "/api/attachments/" + id
	// Match the AIT attachment's exact size and missing final newline.
	body := append([]byte("<!doctype html>"), bytes.Repeat([]byte("x"), 183795-len("<!doctype html>"))...)
	withNewline := append(bytes.Clone(body), '\n')
	corrupt := bytes.Clone(body)
	corrupt[len(corrupt)-1] = 'y'
	for _, tc := range []struct {
		name     string
		original []byte
		response []byte
		hash     string
		wantErr  string
	}{
		{name: "ait-no-final-newline", original: body, response: body},
		{name: "final-newline", original: withNewline, response: withNewline},
		{name: "empty"},
		{name: "extra-newline", original: body, response: withNewline, wantErr: "got 183796 bytes, expected 183795"},
		{name: "short-body", original: body, response: body[:len(body)-1], wantErr: "got 183794 bytes, expected 183795"},
		{name: "same-size-corruption", original: body, response: corrupt, wantErr: "attachment download sha256 mismatch"},
		{name: "malformed-hash", original: body, response: body, hash: "invalid", wantErr: "invalid attachment sha256"},
		{name: "non-hex-hash", original: body, response: body, hash: strings.Repeat("g", 64), wantErr: "invalid attachment sha256"},
	} {
		for _, mode := range []string{"file", "stdout"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				isolate(t)
				sum := sha256.Sum256(tc.original)
				digest := hex.EncodeToString(sum[:])
				if tc.hash != "" {
					digest = tc.hash
				}
				meta := attachmentView{ID: id, Name: "design.html", ContentType: "text/html", Size: int64(len(tc.original)), SHA256: digest}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case metadataPath:
						_ = json.NewEncoder(w).Encode(meta)
					case metadataPath + "/content":
						w.Header().Set("Content-Length", strconv.Itoa(len(tc.response)))
						_, _ = w.Write(tc.response)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				t.Setenv("AEON_URL", srv.URL)
				t.Setenv("AEON_API_KEY", testKey)
				dir := t.TempDir()
				destination := filepath.Join(dir, "design.html")
				if mode == "stdout" {
					destination = "-"
				} else if err := os.WriteFile(destination, []byte("keep existing bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				code, out, stderr := runCLI([]string{"aeon", "--config", filepath.Join(dir, "missing"), "attach", "get", id, "--download", destination}, "")
				var downloaded []byte
				if mode == "stdout" {
					downloaded = []byte(out)
				} else {
					var err error
					downloaded, err = os.ReadFile(destination)
					if err != nil {
						t.Fatal(err)
					}
				}
				if tc.wantErr != "" {
					if code != 1 || out != "" || !strings.Contains(stderr, tc.wantErr) {
						t.Fatalf("failed download: code %d, stdout %d bytes, stderr %q; want %q", code, len(out), stderr, tc.wantErr)
					}
					if mode == "file" && string(downloaded) != "keep existing bytes" {
						t.Fatal("failed verification overwrote the destination")
					}
				} else if code != 0 || stderr != "" || !bytes.Equal(downloaded, tc.original) {
					t.Fatalf("download changed bytes: code %d, stderr %q, got %d bytes, want %d", code, stderr, len(downloaded), len(tc.original))
				}
				spools, err := filepath.Glob(filepath.Join(dir, ".aeon-download-*"))
				if err != nil || len(spools) != 0 {
					t.Fatalf("download spool leaked: %v, %v", spools, err)
				}
			})
		}
	}
}

// Risk: fetching a known attachment must not enumerate the tenant before
// downloading, or report success and overwrite a file after a partial read.
func TestAttachmentGetCLIEndToEnd(t *testing.T) {
	const id = "41ea1228-e717-4e40-8025-cb212f05773e"
	metadataPath := "/api/attachments/" + id
	contentPath := metadataPath + "/content"
	text := bytes.Repeat([]byte("design notes\n"), 854)
	for _, tc := range []struct {
		name, contentType, mode string
		body                    []byte
	}{
		{"notes.txt", "text/plain", "file", text},
		{"design.zip", "application/zip", "file", append([]byte{'P', 'K', 3, 4, 0, 255}, bytes.Repeat([]byte{0, 255, 128, 10}, 975000)...)},
		{"design.html", "text/html", "json-file", []byte("<!doctype html><script>alert('inert download')</script>")},
		{"stdout.txt", "text/plain", "stdout", text},
		{"metadata.txt", "text/plain", "metadata", text},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			var mu sync.Mutex
			var paths []string
			meta := attachmentView{ID: id, NodeID: transcriptEntryID, Name: tc.name, ContentType: tc.contentType, Size: int64(len(tc.body)), CreatedAt: "2026-10-07T12:00:00Z"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testKey {
					t.Error("download must use the selected instance's agent authentication")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case metadataPath:
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(meta)
				case contentPath:
					w.Header().Set("Content-Type", tc.contentType)
					w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
					_, _ = w.Write(tc.body)
				default:
					t.Errorf("attachment lookup enumerated unrelated records: %s", r.URL.Path)
					http.Error(w, "unrelated records must not be read", http.StatusInternalServerError)
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			path := filepath.Join(t.TempDir(), tc.name)
			args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "attach", "get", id}
			if tc.mode == "metadata" || tc.mode == "json-file" {
				args = append(args, "--json")
			}
			if tc.mode == "stdout" {
				args = append(args, "--download", "-")
			} else if tc.mode != "metadata" {
				args = append(args, "--download", path)
			}
			code, out, stderr := runCLI(args, "")
			if code != 0 || stderr != "" {
				t.Fatalf("download exit %d, stderr %q", code, stderr)
			}
			wantPaths := []string{metadataPath}
			if tc.mode != "metadata" {
				wantPaths = append(wantPaths, contentPath)
			}
			mu.Lock()
			gotPaths := append([]string(nil), paths...)
			mu.Unlock()
			if !reflect.DeepEqual(gotPaths, wantPaths) {
				t.Fatalf("requests %v, want %v", gotPaths, wantPaths)
			}
			if tc.mode == "stdout" {
				if !bytes.Equal([]byte(out), tc.body) {
					t.Fatal("stdout did not preserve the exact attachment bytes")
				}
			} else if tc.mode == "metadata" {
				var got attachmentView
				if json.Unmarshal([]byte(out), &got) != nil || got != meta {
					t.Fatalf("metadata output %q", out)
				}
			} else {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, tc.body) {
					t.Fatalf("downloaded bytes differ: %v", err)
				}
				if tc.mode == "json-file" {
					var got struct {
						Attachment   attachmentView `json:"attachment"`
						DownloadedTo string         `json:"downloaded_to"`
					}
					if json.Unmarshal([]byte(out), &got) != nil || got.Attachment != meta || got.DownloadedTo != path {
						t.Fatalf("JSON download output %q", out)
					}
				} else if !strings.Contains(out, "downloaded "+tc.name) {
					t.Fatalf("missing download confirmation: %q", out)
				}
			}
		})
	}

	for _, failure := range []string{"metadata-403", "metadata-404", "content-403", "content-404", "truncated", "short-chunked", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			isolate(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == metadataPath && !strings.HasPrefix(failure, "metadata-") {
					_ = json.NewEncoder(w).Encode(attachmentView{ID: id, Name: "notes.txt", Size: int64(len(text))})
					return
				}
				if strings.HasSuffix(failure, "403") || strings.HasSuffix(failure, "404") {
					status := http.StatusForbidden
					if strings.HasSuffix(failure, "404") {
						status = http.StatusNotFound
					}
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":%q}`, failure)
					return
				}
				if r.URL.Path != contentPath {
					http.Error(w, "unexpected route", http.StatusInternalServerError)
					return
				}
				if failure == "truncated" {
					w.Header().Set("Content-Length", strconv.Itoa(len(text)))
				}
				_, _ = w.Write(text[:10])
				w.(http.Flusher).Flush()
				if failure == "cancelled" {
					close(started)
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			t.Setenv("AEON_URL", srv.URL)
			t.Setenv("AEON_API_KEY", testKey)
			path := filepath.Join(t.TempDir(), "existing.txt")
			if err := os.WriteFile(path, []byte("keep existing bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			rt := &runtime{requestContext: ctx, program: "aeon", stdin: strings.NewReader(""), stdout: &out, stderr: &stderr}
			args := []string{"aeon", "--config", filepath.Join(t.TempDir(), "missing"), "attach", "get", id, "--download", path}
			done := make(chan error, 1)
			go func() {
				done <- rt.execute(args)
			}()
			if failure == "cancelled" {
				select {
				case <-started:
					cancel()
				case <-time.After(10 * time.Second):
					cancel()
					t.Fatal("download never reached the cancellation barrier")
				}
			}
			select {
			case err := <-done:
				exit, ok := err.(*exitError)
				if !ok || exit.code != 1 || out.Len() != 0 {
					t.Fatalf("failed download reported success: %v, stdout %q", err, out.String())
				}
				want := failure
				if failure == "truncated" {
					want = "unexpected EOF"
				} else if failure == "short-chunked" {
					want = "incomplete attachment download"
				} else if failure == "cancelled" {
					want = "context canceled"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("wrong failure: %v, want %s", err, want)
				}
			case <-time.After(10 * time.Second):
				cancel()
				t.Fatal("failed download hung")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "keep existing bytes" {
				t.Fatalf("failed download overwrote existing file: %q, %v", got, err)
			}
		})
	}
}
