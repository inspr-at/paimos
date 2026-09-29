// SPDX-License-Identifier: AGPL-3.0-only

package doctrine

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	credentialRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tokenPattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,255}$`)
)

// ErrCredential is a credential reference that cannot be used. Its message
// names the reference only, never a file's content.
var ErrCredential = errors.New("credential unavailable")

// Credentials resolves a credential reference to a read-only token. Aeon
// stores only the reference (a name such as doctrine-private-read). The token
// is a host-provisioned file named after it in Dir, the server's
// AEON_DOCTRINE_CREDENTIALS_DIR, read at fetch time and held for that fetch.
type Credentials struct{ Dir string }

// Token reads the token for ref. The first line of the file is the token.
func (c Credentials) Token(ref string) (string, error) {
	file, err := c.file(ref)
	if err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", credentialFail(ref, "is not provisioned on this server")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return "", credentialFail(ref, "cannot be read")
	}
	token := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	if !tokenPattern.MatchString(token) {
		return "", credentialFail(ref, "does not hold a token")
	}
	return token, nil
}

// Present reports whether ref names a readable file, without reading it.
func (c Credentials) Present(ref string) bool {
	file, err := c.file(ref)
	if err != nil {
		return false
	}
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}

func (c Credentials) file(ref string) (string, error) {
	if !credentialRefPattern.MatchString(ref) {
		return "", credentialFail(ref, "is not a valid reference")
	}
	if c.Dir == "" || !filepath.IsAbs(c.Dir) {
		return "", credentialFail(ref, "cannot be resolved: this server has no credential directory")
	}
	return filepath.Join(c.Dir, ref), nil
}

type credentialError struct{ msg string }

func (e *credentialError) Error() string { return e.msg }
func (e *credentialError) Unwrap() error { return ErrCredential }

func credentialFail(ref, why string) error {
	if !credentialRefPattern.MatchString(ref) {
		ref = "reference"
	}
	return &credentialError{msg: "credential " + ref + " " + why}
}
