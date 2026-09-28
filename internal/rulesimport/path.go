// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func cleanPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", ErrProhibitedPath
	}
	// Check spelling before cleaning so private-store/../file is also refused.
	if err := pathProhibited(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrProhibitedPath
	}
	if err := pathProhibited(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func pathProhibited(path string) error {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		low := strings.ToLower(part)
		switch low {
		case ".inspr", ".ssh", ".aws", ".gnupg", ".paimos", ".aeon", ".config", ".codex", ".claude", ".cursor", ".local", ".cache", ".azure", ".kube", ".docker", ".grok", ".pi", "secrets", "secret", "credentials", "credentials.json", "auth", "auth.json", "config", "config.json", "transcripts", "transcript", "sessions", "keychains":
			return ErrProhibitedPath
		}
		if low == ".env" || strings.HasPrefix(low, ".env.") || strings.HasPrefix(low, "id_") {
			return ErrProhibitedPath
		}
		for _, ext := range []string{".key", ".pem", ".age", ".gpg", ".p12", ".pfx", ".env", "_rsa", "_ed25519"} {
			if strings.HasSuffix(low, ext) {
				return ErrProhibitedPath
			}
		}
	}
	return nil
}

// validateDoctrinePath runs before ANY open, including opens of directories.
func validateDoctrinePath(path string) (string, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	if _, err = classify(clean, SectionAll, 0); err != nil {
		return "", err
	}
	return clean, nil
}

// readDoctrine uses a pinned descriptor walk, then checks that same descriptor
// before reading. No pathname stat/open race or blocking FIFO read is possible.
func readDoctrine(path string) (string, string, int, error) {
	clean, err := validateDoctrinePath(path)
	if err != nil {
		return "", "", 0, err
	}
	f, err := openNoFollow(clean)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	return readOpenedDoctrine(f)
}

func readOpenedDoctrine(f *os.File) (string, string, int, error) {
	return readOpenedDoctrineWith(f, io.ReadAll)
}

func readOpenedDoctrineWith(f *os.File, read func(io.Reader) ([]byte, error)) (string, string, int, error) {
	info, err := f.Stat()
	if err != nil {
		return "", "", 0, fmt.Errorf("cannot inspect doctrine descriptor")
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, ErrNotRegular
	}
	if info.Size() > MaxFileBytes {
		return "", "", 0, ErrByteBound
	}
	raw, err := read(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return "", "", 0, fmt.Errorf("cannot read doctrine descriptor")
	}
	if len(raw) > MaxFileBytes {
		return "", "", 0, ErrByteBound
	}
	if bytes.ContainsRune(raw, 0) || !utf8.Valid(raw) {
		return "", "", 0, ErrNotText
	}
	sum := sha256.Sum256(raw)
	size := len(raw)
	text := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
	text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
	return string(text), hex.EncodeToString(sum[:]), size, nil
}
