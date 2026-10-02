// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux || darwin

package rules

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

var errSafeFile = errors.New("rules files require private regular files and physical, non-secret-store paths; symlinks are refused")

func rulesDir(path string) (int, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return -1, "", errSafeFile
	}
	if strings.Contains(path, "\x00") || strings.Contains(path, `\`) {
		return -1, "", errSafeFile
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return -1, "", errSafeFile
		}
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for _, p := range parts {
		low := strings.ToLower(p)
		if strings.Contains(low, "secret") || strings.HasPrefix(low, "id_") || strings.HasSuffix(low, ".env") || strings.HasSuffix(low, ".key") || strings.HasSuffix(low, ".pem") || strings.HasSuffix(low, ".age") {
			return -1, "", errSafeFile
		}
		switch low {
		case ".inspr", ".ssh", ".config", ".aws", ".gnupg", ".paimos", ".aeon", ".codex", ".claude", ".grok", ".pi", ".cursor", ".gemini", ".opencode", "credentials", "transcripts", ".agent-transcripts":
			return -1, "", errSafeFile
		}
	}
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_DIRECTORY
	dir, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, "", errSafeFile
	}
	if runtime.GOOS == "darwin" && (parts[0] == "tmp" || parts[0] == "var") {
		var target [32]byte
		n, e := unix.Readlinkat(dir, parts[0], target[:])
		if e == nil {
			if string(target[:n]) != "private/"+parts[0] && string(target[:n]) != "/private/"+parts[0] {
				unix.Close(dir)
				return -1, "", errSafeFile
			}
			parts = append([]string{"private"}, parts...)
		}
	}
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(dir, part, flags, 0)
		unix.Close(dir)
		if e != nil {
			return -1, "", errSafeFile
		}
		dir = next
	}
	return dir, parts[len(parts)-1], nil
}

// ReadFile bounds the actual descriptor read and refuses links at every path
// component. Cache and floor artifacts use explicit .json/.txt file names.
func ReadFile(path string, max int) ([]byte, error) {
	if ext := filepath.Ext(path); ext != ".json" && ext != ".txt" {
		return nil, errSafeFile
	}
	dir, name, err := rulesDir(path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errSafeFile
	}
	f := os.NewFile(uintptr(fd), "rules artifact")
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 0 || st.Size() > int64(max) || st.Mode().Perm()&0077 != 0 {
		return nil, errSafeFile
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errSafeFile
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(raw) > max || len(raw) != int(st.Size()) {
		return nil, errSafeFile
	}
	return raw, nil
}

// WriteFile publishes atomically relative to a pinned parent directory. Output
// previews never replace a file; only an explicit .json cache may be replaced.
func WriteFile(path string, raw []byte, replaceCache bool) error {
	ext := filepath.Ext(path)
	if (replaceCache && ext != ".json") || (!replaceCache && ext != ".txt" && ext != ".json") || len(raw) > MaxCacheBytes {
		return errSafeFile
	}
	dir, name, err := rulesDir(path)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if err = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if !replaceCache || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
			return errSafeFile
		}
	} else if err != unix.ENOENT {
		return errSafeFile
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	temp := ".aeon-rules-" + hex.EncodeToString(nonce)
	fd, err := unix.Openat(dir, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errSafeFile
	}
	defer unix.Unlinkat(dir, temp, 0)
	f := os.NewFile(uintptr(fd), "rules artifact")
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if replaceCache {
		err = unix.Renameat(dir, temp, dir, name)
	} else {
		err = unix.Linkat(dir, temp, dir, name, 0)
	}
	if err != nil {
		return errSafeFile
	}
	return unix.Fsync(dir)
}
