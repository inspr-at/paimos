// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

// AEON — agent runtime primitives
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package grokprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const grokUserInfoURL = "https://auth.x.ai/oauth2/userinfo"

func Variant(variant string) (string, string, error) {
	switch variant {
	case "npm-grok-1.0.30":
		return "grok-native", "d53b6e543e482716236748914331db50145c696ac7af91f1ebdedcf5654cfecb", nil
	case "source-xai-grok-pager-1.0.32":
		return "xai-grok-pager", "6294a6bc10304e3d3b5896194277f6d24fca643b0605d650ca39bad5b40a6d14", nil
	default:
		return "", "", errors.New("native Grok variant unsupported")
	}
}

func safePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\x00\r\n\"\\")
}
func inside(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// VerifyBinding retains the native adapter's exact binary, path and scratch
// checks. The current pinned artifacts qualify macOS arm64 only.
func VerifyBinding(b Binding, workspace string) (string, string, error) {
	if runtime.GOARCH != "arm64" {
		return "", "", errors.New("native Grok binary is not qualified for this architecture")
	}
	name, digest, err := Variant(b.Variant)
	if err != nil {
		return "", "", err
	}
	for _, path := range []string{b.BinaryPath, b.AuthPath, b.ScratchRoot} {
		if !safePath(path) {
			return "", "", errors.New("native Grok path unavailable")
		}
	}
	if len(b.PrincipalSHA256) != 64 {
		return "", "", errors.New("native Grok principal binding invalid")
	}
	if _, err := hex.DecodeString(b.PrincipalSHA256); err != nil {
		return "", "", errors.New("native Grok principal binding invalid")
	}
	physical, err := filepath.EvalSymlinks(b.BinaryPath)
	if err != nil || physical != b.BinaryPath {
		return "", "", errors.New("native Grok executable is not pinned")
	}
	info, err := os.Stat(b.BinaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", "", errors.New("native Grok executable unavailable")
	}
	root := filepath.Dir(b.BinaryPath)
	if b.Variant == "npm-grok-1.0.30" {
		root = filepath.Dir(root)
		if filepath.Base(root) != "grok" || filepath.Base(filepath.Dir(root)) != "@xai-official" || filepath.Base(filepath.Dir(filepath.Dir(root))) != "node_modules" {
			return "", "", errors.New("native Grok package path invalid")
		}
	}
	if b.Variant == "source-xai-grok-pager-1.0.32" && (filepath.Base(root) != "release" || filepath.Base(filepath.Dir(root)) != "target") {
		return "", "", errors.New("native Grok build path invalid")
	}
	if filepath.Base(b.BinaryPath) != name || inside(root, b.AuthPath) || inside(root, b.ScratchRoot) || inside(b.ScratchRoot, root) ||
		inside(workspace, root) || inside(root, workspace) || inside(workspace, b.ScratchRoot) || inside(b.ScratchRoot, workspace) || inside(b.ScratchRoot, b.AuthPath) {
		return "", "", errors.New("native Grok path boundary invalid")
	}
	physical, err = filepath.EvalSymlinks(b.ScratchRoot)
	if err != nil || physical != b.ScratchRoot {
		return "", "", errors.New("native Grok scratch path changed")
	}
	info, err = os.Stat(b.ScratchRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("native Grok scratch is not private")
	}
	f, err := os.Open(b.BinaryPath)
	if err != nil {
		return "", "", errors.New("native Grok binary unavailable")
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", "", errors.New("native Grok binary hash mismatch")
	}
	return digest, root, nil
}

type Snapshot struct{ Digest [32]byte }
type authIdentity struct {
	snapshot Snapshot
	bearer   string
	subject  string
}

// ReadAuth never logs the bearer or cached identity. The trusted caller holds
// the bearer only until UserInfo verification completes.
func ReadAuth(path, expectedPrincipal string) (Snapshot, string, string, error) {
	a, err := parseAuth(path)
	if err != nil {
		return Snapshot{}, "", "", err
	}
	principal := sha256.Sum256([]byte(a.subject))
	want, err := hex.DecodeString(expectedPrincipal)
	if err != nil || subtle.ConstantTimeCompare(principal[:], want) != 1 {
		return Snapshot{}, "", "", errors.New("native Grok account mismatch")
	}
	return a.snapshot, a.bearer, a.subject, nil
}

func parseAuth(path string) (authIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	file := os.NewFile(uintptr(fd), "grok-auth")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > 64<<10 {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	var scope map[string]struct {
		Mode          string `json:"auth_mode"`
		Issuer        string `json:"oidc_issuer"`
		ClientID      string `json:"oidc_client_id"`
		PrincipalType string `json:"principal_type"`
		UserID        string `json:"user_id"`
		Bearer        string `json:"key"`
		ExpiresAt     string `json:"expires_at"`
	}
	if json.Unmarshal(raw, &scope) != nil || len(scope) != 1 {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	var entryName string
	var entry struct {
		Mode, Issuer, ClientID, PrincipalType, UserID, Bearer, ExpiresAt string
	}
	for key, value := range scope {
		entryName = key
		entry.Mode, entry.Issuer, entry.ClientID, entry.PrincipalType = value.Mode, value.Issuer, value.ClientID, value.PrincipalType
		entry.UserID, entry.Bearer, entry.ExpiresAt = value.UserID, value.Bearer, value.ExpiresAt
	}
	expiry, err := time.Parse(time.RFC3339Nano, entry.ExpiresAt)
	if err != nil || !expiry.After(time.Now().Add(5*time.Minute)) || entry.Mode != "oidc" || entry.Issuer != "https://auth.x.ai" ||
		entry.ClientID == "" || entryName != entry.Issuer+"::"+entry.ClientID || entry.PrincipalType != "User" ||
		entry.UserID == "" || len(entry.UserID) > 512 || entry.Bearer == "" || len(entry.Bearer) > 16<<10 {
		return authIdentity{}, errors.New("native Grok account unavailable")
	}
	return authIdentity{snapshot: Snapshot{Digest: sha256.Sum256(raw)}, bearer: entry.Bearer, subject: entry.UserID}, nil
}

func VerifyUserInfo(ctx context.Context, bearer, expectedSub string) error {
	_, err := fetchUserInfo(ctx, bearer, expectedSub, nil)
	return err
}

var grokEmail = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9.-]+$`)

// The endpoint is fixed. A client may be supplied only by package tests.
func fetchUserInfo(ctx context.Context, bearer, expectedSub string, client *http.Client) (string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, grokUserInfoURL, nil)
	if err != nil {
		return "", errors.New("native Grok identity unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // An ambient daemon proxy must never receive the bearer.
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("native Grok identity unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 64<<10 {
		return "", errors.New("native Grok identity unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return "", errors.New("native Grok identity unavailable")
	}
	var userInfo struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if json.Unmarshal(raw, &userInfo) != nil || userInfo.Sub == "" || len(userInfo.Sub) > 512 ||
		subtle.ConstantTimeCompare([]byte(userInfo.Sub), []byte(expectedSub)) != 1 {
		return "", errors.New("native Grok identity mismatch")
	}
	if userInfo.EmailVerified && len(userInfo.Email) <= 128 && grokEmail.MatchString(userInfo.Email) {
		return userInfo.Email, nil
	}
	return "", nil
}

func AuthUnchanged(path, expectedPrincipal string, before Snapshot) error {
	after, _, _, err := ReadAuth(path, expectedPrincipal)
	if err != nil || !bytes.Equal(after.Digest[:], before.Digest[:]) {
		return errors.New("native Grok account changed during conversation")
	}
	return nil
}

// Probe derives an identity only after the pinned binary and authenticated
// userinfo subject pass. The bearer remains local to this call.
func Probe(ctx context.Context, b Binding) (Identity, error) {
	return probe(ctx, b, nil, VerifyBinding)
}

func probe(ctx context.Context, b Binding, client *http.Client, verify func(Binding, string) (string, string, error)) (Identity, error) {
	preflight := b
	if preflight.PrincipalSHA256 == "" {
		preflight.PrincipalSHA256 = strings.Repeat("0", 64)
	}
	if _, _, err := verify(preflight, ""); err != nil {
		return Identity{}, err
	}
	var a authIdentity
	if b.PrincipalSHA256 == "" {
		var err error
		a, err = parseAuth(b.AuthPath)
		if err != nil {
			return Identity{}, err
		}
	} else {
		snapshot, bearer, subject, err := ReadAuth(b.AuthPath, b.PrincipalSHA256)
		if err != nil {
			return Identity{}, err
		}
		a = authIdentity{snapshot: snapshot, bearer: bearer, subject: subject}
	}
	principal := sha256.Sum256([]byte(a.subject))
	b.PrincipalSHA256 = hex.EncodeToString(principal[:])
	email, err := fetchUserInfo(ctx, a.bearer, a.subject, client)
	if err != nil {
		return Identity{}, err
	}
	if err := AuthUnchanged(b.AuthPath, b.PrincipalSHA256, a.snapshot); err != nil {
		return Identity{}, err
	}
	if _, _, err := verify(b, ""); err != nil {
		return Identity{}, err
	}
	label := email
	if label == "" {
		label = "Grok subject SHA-256: " + b.PrincipalSHA256
	}
	return Identity{Binding: b, Label: label}, nil
}
