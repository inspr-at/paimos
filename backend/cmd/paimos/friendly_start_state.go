// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

type friendlyStartRecord struct {
	Version int                 `json:"version"`
	Digest  string              `json:"digest"`
	Result  friendlyStartResult `json:"result"`
}

func friendlyStartFingerprint(o friendlyStartOptions, prompt string) string {
	// Presentation and wait choices do not alter the requested generation.
	o.DryRun, o.Explain, o.Guided, o.NonInteractive = false, false, false, false
	o.Wait = 0
	o.PromptFile = ""
	raw, _ := json.Marshal(struct {
		Options friendlyStartOptions
		Prompt  string
	}{o, prompt})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func friendlyRecordPath(dir, key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
}
func readFriendlyStartRecord(dir, key, digest string) (friendlyStartResult, bool, error) {
	directory, dirErr := os.Lstat(dir)
	if errors.Is(dirErr, os.ErrNotExist) {
		return friendlyStartResult{}, false, nil
	}
	if dirErr != nil || !directory.IsDir() || directory.Mode().Perm() != 0o700 {
		return friendlyStartResult{}, false, errors.New("retry directory must be private (0700) and not a symlink")
	}
	path := friendlyRecordPath(dir, key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return friendlyStartResult{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return friendlyStartResult{}, false, errors.New("retry record is unsafe or unavailable; inspect local state before retrying")
	}
	file, err := os.Open(path) // #nosec G304 -- hashed retry key beneath the private ledger; compare opened descriptor with validated metadata before decoding.
	if err != nil {
		return friendlyStartResult{}, false, errors.New("retry record could not be read")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() || actual.Mode().Perm() != 0o600 || actual.Size() > 64<<10 {
		return friendlyStartResult{}, false, errors.New("retry record changed before read")
	}
	var record friendlyStartRecord
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || record.Version != 1 || record.Digest == "" || record.Result.Outcome == "" || decoder.Decode(&struct{}{}) != io.EOF {
		return friendlyStartResult{}, false, errors.New("retry record is incomplete; another start may be in progress; inspect runtime doctor before any new key")
	}
	if record.Digest != digest {
		return friendlyStartResult{}, false, errors.New("idempotency key was already used with different input; conflicting reuse is forbidden")
	}
	record.Result.Replayed = true
	return record.Result, true, nil
}
func reserveFriendlyStartRecord(dir, key, digest string, result friendlyStartResult) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("private retry directory cannot be created")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("retry directory must be private (0700) and not a symlink")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) >= 4096 {
		return errors.New("retry ledger is unavailable or at capacity; archive reconciled records before starting")
	}
	file, err := os.OpenFile(friendlyRecordPath(dir, key), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("idempotency key is already reserved or its record is unavailable")
	}
	defer file.Close()
	if json.NewEncoder(file).Encode(friendlyStartRecord{Version: 1, Digest: digest, Result: result}) != nil || file.Sync() != nil {
		return errors.New("start intent could not be made durable; no start was attempted")
	}
	return syncFriendlyStartDir(dir)
}
func completeFriendlyStartRecord(dir, key, digest string, result friendlyStartResult) error {
	file, err := os.CreateTemp(dir, ".outcome-")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if json.NewEncoder(file).Encode(friendlyStartRecord{Version: 1, Digest: digest, Result: result}) != nil || file.Sync() != nil {
		file.Close()
		return errors.New("start outcome write failed")
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, friendlyRecordPath(dir, key)); err != nil {
		return err
	}
	return syncFriendlyStartDir(dir)
}
func syncFriendlyStartDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("retry directory is unsafe")
	}
	file, err := os.Open(dir) // #nosec G304 -- fixed private retry directory, descriptor identity verified below; opened only for directory sync.
	if err != nil {
		return errors.New("retry directory is unavailable")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.IsDir() || actual.Mode().Perm() != 0o700 {
		return errors.New("retry directory changed before sync")
	}
	if file.Sync() != nil {
		return errors.New("retry directory could not be synchronized")
	}
	return nil
}

func validateFriendlyWorkspace(ctx context.Context, path string, sessions []agentd.Session) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("workspace must be an existing directory")
	}
	top := path
	tracked := false
	for parent := path; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			tracked = true
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("workspace provenance cannot be inspected")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if tracked {
		output, err := friendlyGit(ctx, path, "rev-parse", "--show-toplevel")
		if err != nil {
			return errors.New("workspace Git provenance is unavailable; repair it before starting")
		}
		top, err = filepath.EvalSymlinks(strings.TrimSpace(string(output)))
		if err != nil {
			return errors.New("workspace Git top-level is unavailable")
		}
		output, err = friendlyGit(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=none")
		if err != nil {
			return errors.New("workspace cleanliness could not be verified; no start was attempted")
		}
		if len(output) != 0 {
			return errors.New("workspace is dirty; commit or preserve the changes yourself, or select a separate clean worktree")
		}
	}
	for _, s := range sessions {
		candidate := s.WorkspaceProvenance.GitTopLevel
		if candidate == "" {
			candidate = s.WorkspaceProvenance.CanonicalPath
		}
		if candidate == "" {
			candidate = s.Workspace
		}
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = resolved
		}
		// Keep unresolved remote closures visible to the operator before another
		// friendly start, even if the local process has already terminated.
		remoteClosed := s.Reporter.PublicSessionID == "" || s.Reporter.Closed
		if friendlyTerminal(s.State) && remoteClosed {
			continue
		}
		if candidate == top || candidate == path || strings.HasPrefix(path, candidate+string(filepath.Separator)) || strings.HasPrefix(candidate, path+string(filepath.Separator)) {
			return errors.New("workspace is exclusively owned by an existing generation; inspect harness status and stop it or select another clean workspace")
		}
	}
	return nil
}
func friendlyGit(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...) // #nosec G204 -- fixed git executable, two closed read-only probe argv sets above; operator workspace is a separate -C argument, never shell input.
	// Match the existing agentd Git isolation: caller Git overrides and global
	// configuration cannot redirect a cleanliness/ownership probe.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") && key != "LC_ALL" && key != "LANG" {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var output friendlyBoundedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.overflow {
		return nil, errors.New("workspace probe failed")
	}
	return output.data, nil
}

type friendlyBoundedBuffer struct {
	data     []byte
	overflow bool
}

func (b *friendlyBoundedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if len(b.data)+n > 64<<10 {
		b.overflow = true
		return n, nil
	}
	b.data = append(b.data, data...)
	return n, nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
