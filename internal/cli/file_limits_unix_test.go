// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package cli

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFileLimitsStopReadingBeforeEOF(t *testing.T) {
	for _, command := range []string{"text", "apply"} {
		t.Run(command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.fifo")
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			limit := int64(1 << 20)
			want := "--description-file is too long"
			if command == "apply" {
				limit = 8 << 20
				want = "plan is too large"
			}
			// Keep the file open after writing the overflow byte. A reader that
			// waits for EOF instead of enforcing its byte bound will hang.
			release := make(chan struct{})
			defer close(release)
			written := make(chan error, 1)
			go func() {
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					written <- err
					return
				}
				defer f.Close()
				_, err = io.CopyN(f, fileLimitFillReader{}, limit+1)
				written <- err
				<-release
			}()
			returned := make(chan error, 1)
			go func() {
				rt := &runtime{stdout: io.Discard}
				if command == "text" {
					_, err := rt.readText("", path, "description")
					returned <- err
					return
				}
				cmd := rt.cmdApply()
				fs := &flagSet{}
				cmd.addFlags(fs)
				if _, err := fs.parse([]string{"--from-file", path, "--dry-run"}); err != nil {
					returned <- err
					return
				}
				returned <- cmd.run(nil)
			}()
			select {
			case err := <-written:
				if err != nil {
					t.Fatalf("write overflow byte: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("file reader hung before consuming the overflow byte")
			}
			select {
			case err := <-returned:
				assertFileLimitError(t, err, want)
			case <-time.After(10 * time.Second):
				t.Fatal("file reader waited for EOF after consuming the overflow byte")
			}
		})
	}
}

type fileLimitFillReader struct{}

func (fileLimitFillReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
