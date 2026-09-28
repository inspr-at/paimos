// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var prohibitedComponents = map[string]bool{
	".inspr":  true,
	".ssh":    true,
	".aws":    true,
	".gnupg":  true,
	".paimos": true,
	".aeon":   true,
	".env":    true,
}

func cleanPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: empty path", ErrProhibitedPath)
	}
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(wd, path)
	}
	return filepath.Clean(path), nil
}

func pathProhibited(path string) error {
	base := filepath.Base(path)
	low := strings.ToLower(base)
	switch {
	case low == ".env" || strings.HasPrefix(low, ".env."):
		return fmt.Errorf("%w: %s", ErrProhibitedPath, base)
	case low == "credentials" || low == "credentials.json" || low == "secrets" || low == "secrets.json" || low == "auth.json":
		return fmt.Errorf("%w: %s", ErrProhibitedPath, base)
	case low == "id_rsa" || low == "id_ed25519":
		return fmt.Errorf("%w: %s", ErrProhibitedPath, base)
	}
	for _, ext := range []string{".key", ".pem", ".age", ".p12", ".pfx", ".env"} {
		if strings.HasSuffix(low, ext) {
			return fmt.Errorf("%w: %s", ErrProhibitedPath, base)
		}
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if prohibitedComponents[strings.ToLower(component)] {
			return fmt.Errorf("%w: component %s", ErrProhibitedPath, component)
		}
	}
	return nil
}

// readDoctrine reads one explicit file. Symlinks are refused. The size check
// happens before the bytes are retained.
func readDoctrine(path string) (string, string, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return "", "", err
	}
	if err := pathProhibited(clean); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("%w: %s", ErrSymlink, info.Name())
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: %s", ErrNotRegular, info.Name())
	}
	if info.Size() > MaxFileBytes {
		return "", "", fmt.Errorf("%w: %s is %d bytes, limit is %d", ErrByteBound, info.Name(), info.Size(), MaxFileBytes)
	}
	f, err := openNoFollow(clean)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(raw) > MaxFileBytes {
		return "", "", fmt.Errorf("%w: %s exceeds %d bytes", ErrByteBound, info.Name(), MaxFileBytes)
	}
	if bytes.Contains(raw, []byte{0}) || !utf8.Valid(raw) {
		return "", "", fmt.Errorf("%w: %s", ErrNotText, info.Name())
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	if !utf8.Valid(raw) {
		return "", "", fmt.Errorf("%w: %s", ErrNotText, info.Name())
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

func openNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, filepath.Base(path))
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
