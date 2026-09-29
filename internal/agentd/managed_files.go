// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const managedFileLimit = 1 << 20

var errManagedFile = errors.New("workspace file unavailable: require a regular, single-link file and no symlink components")

type fileReadArgs struct {
	Path   string `json:"file_path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type fileWriteArgs struct {
	Path    string `json:"file_path"`
	Content string `json:"content"`
}
type fileEditArgs struct {
	Path string `json:"file_path"`
	Old  string `json:"old_string"`
	New  string `json:"new_string"`
	All  bool   `json:"replace_all,omitempty"`
}
type fileSearchArgs struct {
	Path    string `json:"path,omitempty"`
	Pattern string `json:"pattern"`
}

func addManagedFileTools(s *mcp.Server, b toolBinding) {
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_read", Description: "Read UTF-8 workspace text (1 MiB maximum). Optional 1-based line offset and limit. Symlinks and hard links are denied."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in fileReadArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() {
				return nil, "", ErrNotOwned
			}
			if in.Offset < 0 || in.Limit < 0 {
				return nil, "", errors.New("invalid line range")
			}
			data, err := readManagedFile(b.workspace, in.Path)
			if err != nil {
				return nil, "", err
			}
			lines := strings.Split(data, "\n")
			start := max(0, in.Offset-1)
			if start >= len(lines) {
				return nil, "", nil
			}
			end := len(lines)
			if in.Limit > 0 && in.Limit < end-start {
				end = start + in.Limit
			}
			return nil, strings.Join(lines[start:end], "\n"), nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_write", Description: "Write UTF-8 workspace text. Creates missing parent directories safely. Symlinks and hard links are denied."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in fileWriteArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() {
				return nil, "", ErrNotOwned
			}
			err := writeManagedFile(b.workspace, in.Path, in.Content, nil)
			return nil, "file written", err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "aeon_edit", Description: "Replace exact text in a workspace file. Unless replace_all is true, old_string must occur exactly once. Symlinks and hard links are denied."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in fileEditArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() {
				return nil, "", ErrNotOwned
			}
			err := writeManagedFile(b.workspace, in.Path, "", &in)
			return nil, "file edited", err
		})
	for _, grep := range []bool{false, true} {
		name, description := "aeon_glob", "List workspace files recursively matching a Go filepath.Match pattern against each relative path or basename. Use * for all files. Symlinks and hard links are denied."
		if grep {
			name, description = "aeon_grep", "Search workspace text recursively with a Go regular expression; returns relative path, line number and matching line. Symlinks and hard links are denied."
		}
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, in fileSearchArgs) (*mcp.CallToolResult, string, error) {
			if !b.active() {
				return nil, "", ErrNotOwned
			}
			out, err := searchManagedFiles(ctx, b.workspace, in, grep)
			return nil, out, err
		})
	}
}

// The root and every path component are opened without following links. No
// checked pathname is subsequently handed to a tool for a second open.
func managedRelative(root, path string) (string, error) {
	if !filepath.IsAbs(root) || path == "" || strings.ContainsRune(path, 0) {
		return "", errManagedFile
	}
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(root, path)
		if err != nil {
			return "", errManagedFile
		}
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return "", errManagedFile
		}
	}
	path = filepath.Clean(path)
	if path != "." && !filepath.IsLocal(path) {
		return "", errManagedFile
	}
	return path, nil
}

func managedReadFD(f *os.File) (string, error) {
	if err := checkManagedRegular(f); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, managedFileLimit+1))
	if err != nil || len(data) > managedFileLimit {
		return "", errors.New("workspace read failed or exceeds 1 MiB")
	}
	return string(data), nil
}
func readManagedFile(root, path string) (string, error) {
	f, err := openManagedFile(root, path, false, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return managedReadFD(f)
}
func writeManagedFile(root, path, content string, edit *fileEditArgs) error {
	if len(content) > managedFileLimit {
		return errors.New("workspace write exceeds 1 MiB")
	}
	f, err := openManagedFile(root, path, true, edit == nil)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeManagedFD(f, content, edit)
}

func writeManagedFD(f *os.File, content string, edit *fileEditArgs) error {
	if len(content) > managedFileLimit {
		return errors.New("workspace write exceeds 1 MiB")
	}
	if edit != nil {
		original, err := managedReadFD(f)
		if err != nil {
			return err
		}
		count := strings.Count(original, edit.Old)
		if edit.Old == "" || count == 0 || (!edit.All && count != 1) {
			return errors.New("edit requires matching unambiguous old_string")
		}
		// Bound the allocation before replacement (a small old_string can
		// otherwise expand a 1 MiB input into gigabytes).
		size := int64(len(original)) + int64(count)*int64(len(edit.New)-len(edit.Old))
		if size > managedFileLimit {
			return errors.New("workspace edit exceeds 1 MiB")
		}
		content = strings.ReplaceAll(original, edit.Old, edit.New)
		if len(content) > managedFileLimit {
			return errors.New("workspace edit exceeds 1 MiB")
		}
	}
	// Validate the opened descriptor, before truncating or reading its contents.
	if err := checkManagedRegular(f); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := io.WriteString(f, content)
	return err
}

func searchManagedFiles(ctx context.Context, root string, in fileSearchArgs, grep bool) (string, error) {
	if in.Path == "" {
		in.Path = "."
	}
	var re *regexp.Regexp
	if grep {
		var err error
		re, err = regexp.Compile(in.Pattern)
		if err != nil {
			return "", errors.New("invalid search expression")
		}
	} else if _, err := filepath.Match(in.Pattern, ""); err != nil {
		return "", errors.New("invalid glob")
	}
	dir, err := openManagedDirectory(root, in.Path)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	var out strings.Builder
	count := 0
	var visit func(*os.File, string, int) error
	visit = func(dir *os.File, prefix string, depth int) error {
		if depth > 64 {
			return errors.New("workspace search depth exceeded")
		}
		entries, err := dir.ReadDir(100001 - count)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 100000 || out.Len() > 64<<10 {
				return errors.New("workspace search bounds exceeded")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Open directly under this descriptor, even if its pathname was renamed.
			f, isDir, err := openManagedChild(dir, entry.Name())
			if err != nil {
				return err
			}
			path := filepath.Join(prefix, entry.Name())
			if isDir {
				err = visit(f, path, depth+1)
			} else if err = checkManagedRegular(f); err == nil {
				if grep {
					var data string
					data, err = managedReadFD(f)
					if err == nil {
						for n, line := range strings.Split(data, "\n") {
							if re.MatchString(line) {
								fmt.Fprintf(&out, "%s:%d:%s\n", path, n+1, line)
								if out.Len() > 64<<10 {
									err = errors.New("workspace search bounds exceeded")
									break
								}
							}
						}
					}
				} else {
					match, _ := filepath.Match(in.Pattern, path)
					base, _ := filepath.Match(in.Pattern, entry.Name())
					if match || base {
						fmt.Fprintln(&out, path)
					}
				}
			}
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(dir, "", 0); err != nil {
		return "", err
	}
	if out.Len() > 64<<10 {
		return "", errors.New("workspace search bounds exceeded")
	}
	return out.String(), nil
}
