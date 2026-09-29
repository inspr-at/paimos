// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type terminalLibraryInfo struct {
	dependencies, rpaths []string
}

type terminalLibraryInspector func(context.Context, string, string) (terminalLibraryInfo, error)

type terminalLibraryStamp struct {
	realpath string
	mtime    time.Time
	size     int64
}

type terminalLibraryEntry struct {
	files []string
	// Include aliases and dependency mtimes: a Homebrew opt symlink may change
	// even when the executable itself has not been upgraded.
	observed map[string]terminalLibraryStamp
}

// Owned by a managed run, never shared as process-global mutable state.
type terminalLibraryCache struct {
	mu      sync.Mutex
	entries map[string]terminalLibraryEntry
}

func libraryStamp(path string) (terminalLibraryStamp, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(real) {
		return terminalLibraryStamp{}, errors.New("terminal library path is not physical")
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return terminalLibraryStamp{}, errors.New("terminal library is not a regular file")
	}
	return terminalLibraryStamp{real, info.ModTime(), info.Size()}, nil
}

func (c *terminalLibraryCache) closure(ctx context.Context, workspace, tmp, binary string, inspect terminalLibraryInspector) ([]string, error) {
	// Nix already supplies its complete immutable package closure.
	if strings.HasPrefix(binary, "/nix/store/") {
		return nil, nil
	}
	stamp, err := libraryStamp(binary)
	if err != nil || pathWithin(workspace, stamp.realpath) {
		return nil, errors.New("terminal library executable is not independent of workspace")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[stamp.realpath]; ok {
		valid := true
		for path, previous := range entry.observed {
			current, err := libraryStamp(path)
			if err != nil || current != previous || pathWithin(workspace, current.realpath) {
				valid = false
				break
			}
		}
		if valid {
			return slices.Clone(entry.files), nil
		}
	}
	entry := terminalLibraryEntry{observed: map[string]terminalLibraryStamp{stamp.realpath: stamp}}
	seen := map[string]bool{}
	var visit func(string, []string, int) error
	visit = func(path string, inherited []string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		real := entry.observed[path].realpath
		if seen[real] {
			return nil
		}
		if depth > 64 || len(seen) >= 512 {
			return errors.New("terminal dynamic library closure exceeds bounds")
		}
		seen[real] = true
		info, err := inspect(ctx, tmp, real)
		if err != nil {
			return err
		}
		// dyld searches this image's LC_RPATH entries before its loader chain.
		var rpaths []string
		for _, rpath := range info.rpaths {
			expanded, err := expandTerminalLoaderPath(rpath, path, stamp.realpath)
			if err != nil {
				return err
			}
			rpaths = append(rpaths, expanded)
		}
		rpaths = append(rpaths, inherited...)
		for _, dependency := range info.dependencies {
			candidates := []string{dependency}
			if strings.HasPrefix(dependency, "@rpath/") {
				candidates = nil
				for _, root := range rpaths {
					candidates = append(candidates, filepath.Join(root, strings.TrimPrefix(dependency, "@rpath/")))
				}
			}
			resolved := false
			for _, candidate := range candidates {
				candidate, err = expandTerminalLoaderPath(candidate, path, stamp.realpath)
				if err != nil {
					return err
				}
				// These libraries are covered by the OS runtime grants and often
				// exist only in the dyld shared cache, not as ordinary disk files.
				if pathWithin("/usr/lib", candidate) || pathWithin("/System/Library", candidate) {
					resolved = true
					break
				}
				dep, err := libraryStamp(candidate)
				if err != nil {
					continue
				}
				if pathWithin(workspace, dep.realpath) {
					return errors.New("terminal dynamic library is not independent of workspace")
				}
				entry.observed[candidate] = dep
				// dyld checks the install-name spelling before opening the
				// canonical file. Both grants are literals for the same file;
				// neither exposes the opt directory or adjacent keg contents.
				for _, file := range []string{candidate, dep.realpath} {
					if !slices.Contains(entry.files, file) {
						entry.files = append(entry.files, file)
					}
				}
				if err := visit(candidate, rpaths, depth+1); err != nil {
					return err
				}
				resolved = true
				break
			}
			if !resolved {
				return errors.New("terminal dynamic library dependency could not be resolved")
			}
		}
		return nil
	}
	if err := visit(stamp.realpath, nil, 0); err != nil {
		return nil, err
	}
	if c.entries == nil || len(c.entries) >= 64 {
		c.entries = make(map[string]terminalLibraryEntry)
	}
	c.entries[stamp.realpath] = entry
	return slices.Clone(entry.files), nil
}

func expandTerminalLoaderPath(path, loader, executable string) (string, error) {
	for token, base := range map[string]string{"@loader_path": filepath.Dir(loader), "@executable_path": filepath.Dir(executable)} {
		if path == token || strings.HasPrefix(path, token+"/") {
			path = base + strings.TrimPrefix(path, token)
			break
		}
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", errors.New("terminal dynamic library has an unsupported loader path")
	}
	return filepath.Clean(path), nil
}

func inspectTerminalLibrary(ctx context.Context, tmp, path string) (terminalLibraryInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return terminalLibraryInfo{}, err
	}
	var magic [4]byte
	_, err = io.ReadFull(file, magic[:])
	_ = file.Close()
	// npm is a script; its separately qualified node interpreter owns its
	// dynamic library closure. Never execute a candidate to inspect it.
	if string(magic[:2]) == "#!" {
		return terminalLibraryInfo{}, nil
	}
	if err != nil {
		return terminalLibraryInfo{}, errors.New("terminal executable header unavailable")
	}
	switch magic {
	case [4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
		[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
		[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca},
		[4]byte{0xca, 0xfe, 0xba, 0xbf}, [4]byte{0xbf, 0xba, 0xfe, 0xca}:
	default:
		return terminalLibraryInfo{}, errors.New("terminal executable is not Mach-O or a script")
	}
	query := func(flag string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// otool selects the host slice, including arm64e system executables.
		cmd := exec.CommandContext(ctx, "/usr/bin/otool", flag, path)
		cmd.Dir = tmp
		cmd.Env = terminalEnvironment(tmp, "off", "", "/usr/bin/otool")
		var output terminalLibraryOutput
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("terminal dynamic library inspection failed for %s: %w", path, err)
		}
		return output.buffer.String(), nil
	}
	linked, err := query("-L")
	if err != nil {
		return terminalLibraryInfo{}, err
	}
	commands, err := query("-l")
	if err != nil {
		return terminalLibraryInfo{}, err
	}
	return parseTerminalLibraries(linked, commands), nil
}

type terminalLibraryOutput struct{ buffer bytes.Buffer }

func (b *terminalLibraryOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 1<<20 {
		return 0, errors.New("terminal library inspection exceeds bounds")
	}
	return b.buffer.Write(p)
}

func parseTerminalLibraries(linked, commands string) terminalLibraryInfo {
	var info terminalLibraryInfo
	var command, identity string
	for _, line := range strings.Split(commands, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cmd ") {
			command = strings.TrimPrefix(line, "cmd ")
		}
		if command == "LC_RPATH" && strings.HasPrefix(line, "path ") {
			path, _, _ := strings.Cut(strings.TrimPrefix(line, "path "), " (offset ")
			info.rpaths = append(info.rpaths, path)
		}
		if command == "LC_ID_DYLIB" && strings.HasPrefix(line, "name ") {
			identity, _, _ = strings.Cut(strings.TrimPrefix(line, "name "), " (offset ")
		}
	}
	for _, line := range strings.Split(linked, "\n") {
		path, _, ok := strings.Cut(strings.TrimSpace(line), " (compatibility version ")
		// otool -L includes a dylib's own install name, which is not a load.
		if ok && path != identity {
			info.dependencies = append(info.dependencies, path)
		}
	}
	return info
}
