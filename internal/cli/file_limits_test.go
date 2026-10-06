// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBoundedInputReads(t *testing.T) {
	const limit = 8
	for _, size := range []int{0, limit - 1, limit, limit + 1, 32} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			text := strings.Repeat("x", size)
			want := text
			if len(want) > limit+1 {
				want = want[:limit+1]
			}
			r := strings.NewReader(text)
			raw, err := readBounded(r, limit)
			if err != nil || string(raw) != want || r.Len() != size-len(want) {
				t.Fatalf("reader: read %d bytes, %d unread, error %v", len(raw), r.Len(), err)
			}
			path := filepath.Join(t.TempDir(), "input.txt")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			raw, err = readBoundedFile(path, limit)
			if err != nil || string(raw) != want {
				t.Fatalf("file: read %d bytes, error %v", len(raw), err)
			}
		})
	}
	t.Run("read error", func(t *testing.T) {
		want := errors.New("input read failed")
		raw, err := readBounded(iotest.ErrReader(want), limit)
		if !errors.Is(err, want) || len(raw) != 0 {
			t.Fatalf("read failure: got %d bytes, error %v", len(raw), err)
		}
		rt := &runtime{stdin: iotest.ErrReader(want)}
		if text, err := rt.readText("", "-", "description"); text != "" || !errors.Is(err, want) || err.Error() != "read stdin: input read failed" {
			t.Fatalf("text read failure: got %q, error %v", text, err)
		}
		cmd := rt.cmdApply()
		fs := &flagSet{}
		cmd.addFlags(fs)
		if _, err := fs.parse([]string{"--from-file", "-", "--dry-run"}); err != nil {
			t.Fatal(err)
		}
		var exit *exitError
		if err := cmd.run(nil); !errors.As(err, &exit) || exit.code != 1 || exit.msg != want.Error() {
			t.Fatalf("apply read failure: %v", err)
		}
	})
	t.Run("open error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.txt")
		if raw, err := readBoundedFile(path, limit); raw != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("open failure: got %d bytes, error %v", len(raw), err)
		}
	})
}

func TestReadTextByteLimits(t *testing.T) {
	for _, source := range []string{"file", "stdin"} {
		for _, size := range []int{0, (1 << 20) - 1, 1 << 20, (1 << 20) + 1} {
			t.Run(source+"/"+strconv.Itoa(size), func(t *testing.T) {
				text := strings.Repeat("x", size)
				path := "-"
				rt := &runtime{stdin: strings.NewReader(text)}
				if source == "file" {
					path = filepath.Join(t.TempDir(), "text.txt")
					if err := os.WriteFile(path, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				got, err := rt.readText("", path, "description")
				if size > 1<<20 {
					assertFileLimitError(t, err, "--description-file is too long")
					if got != "" {
						t.Fatal("overflow returned partial text")
					}
				} else if err != nil || got != text {
					t.Fatalf("read %d bytes: got %d bytes, error %v", size, len(got), err)
				}
			})
		}
	}
}

func TestApplyPlanByteLimits(t *testing.T) {
	for _, source := range []string{"file", "stdin"} {
		for _, size := range []int{(8 << 20) - 1, 8 << 20, (8 << 20) + 1} {
			t.Run(source+"/"+strconv.Itoa(size), func(t *testing.T) {
				// A YAML comment pads a valid, small plan to the byte boundary.
				text := "project: AEON\n#" + strings.Repeat("x", size-len("project: AEON\n#"))
				path := "-"
				if source == "file" {
					path = filepath.Join(t.TempDir(), "plan.yaml")
					if err := os.WriteFile(path, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				rt := &runtime{stdin: strings.NewReader(text), stdout: io.Discard}
				cmd := rt.cmdApply()
				fs := &flagSet{}
				cmd.addFlags(fs)
				if _, err := fs.parse([]string{"--from-file", path, "--dry-run"}); err != nil {
					t.Fatal(err)
				}
				err := cmd.run(nil)
				if size > 8<<20 {
					assertFileLimitError(t, err, "plan is too large")
				} else if err != nil {
					t.Fatalf("apply %d-byte plan: %v", size, err)
				}
			})
		}
	}
}

func assertFileLimitError(t *testing.T, err error, want string) {
	t.Helper()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 2 || exit.msg != want {
		t.Fatalf("expected usage error %q, got %v", want, err)
	}
}
