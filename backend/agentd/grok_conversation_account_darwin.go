//go:build darwin

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

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
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const grokUserInfoURL = "https://auth.x.ai/oauth2/userinfo"

type grokAuthSnapshot struct{ digest [32]byte }

// readGrokAuth never emits the bearer or cached identity. The caller holds the
// bearer only until UserInfo verification completes, then drops that reference.
func readGrokAuth(path, expectedPrincipal string) (grokAuthSnapshot, string, string, error) {
	var closed grokAuthSnapshot
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return closed, "", "", errors.New("native Grok account unavailable")
	}
	file := os.NewFile(uintptr(fd), "grok-auth")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > 64<<10 {
		return closed, "", "", errors.New("native Grok account unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return closed, "", "", errors.New("native Grok account unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return closed, "", "", errors.New("native Grok account unavailable")
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
		return closed, "", "", errors.New("native Grok account unavailable")
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
		return closed, "", "", errors.New("native Grok account unavailable")
	}
	principal := sha256.Sum256([]byte(entry.UserID))
	want, err := hex.DecodeString(expectedPrincipal)
	if err != nil || subtle.ConstantTimeCompare(principal[:], want) != 1 {
		return closed, "", "", errors.New("native Grok account mismatch")
	}
	return grokAuthSnapshot{digest: sha256.Sum256(raw)}, entry.Bearer, entry.UserID, nil
}

func verifyGrokUserInfo(ctx context.Context, bearer, expectedSub string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, grokUserInfoURL, nil)
	if err != nil {
		return errors.New("native Grok identity unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // An ambient daemon proxy must never receive the bearer.
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("native Grok identity unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 64<<10 {
		return errors.New("native Grok identity unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return errors.New("native Grok identity unavailable")
	}
	var userInfo struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &userInfo) != nil || userInfo.Sub == "" || len(userInfo.Sub) > 512 ||
		subtle.ConstantTimeCompare([]byte(userInfo.Sub), []byte(expectedSub)) != 1 {
		return errors.New("native Grok identity mismatch")
	}
	return nil
}

func verifyGrokAuthUnchanged(path, expectedPrincipal string, before grokAuthSnapshot) error {
	after, _, _, err := readGrokAuth(path, expectedPrincipal)
	if err != nil || !bytes.Equal(after.digest[:], before.digest[:]) {
		return errors.New("native Grok account changed during conversation")
	}
	return nil
}
