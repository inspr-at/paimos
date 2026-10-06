// SPDX-License-Identifier: AGPL-3.0-only
package hooknote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Exact daemon bytes are independently approved in the hook release. Public
// hook-peer.json only narrows process identity and cannot extend this ceiling.
// Native qualification and publication are still pending: no digest is guessed.
func approvedDaemonDigests() []string { return nil }

func authenticateDaemon(ctx context.Context, peer Process) error {
	return authenticateDaemonImage(ctx, peer, approvedDaemonDigests())
}

func authenticateDaemonImage(ctx context.Context, peer Process, approved []string) error {
	if ctx.Err() != nil || !filepath.IsAbs(peer.Executable) || filepath.Clean(peer.Executable) != peer.Executable {
		return ErrPeer
	}
	before, err := os.Lstat(peer.Executable)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 256<<20 || before.Mode().Perm()&0022 != 0 {
		return ErrPeer
	}
	f, err := os.Open(peer.Executable)
	if err != nil {
		return ErrPeer
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) {
		return ErrPeer
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != peer.Dev || uint64(st.Ino) != peer.Ino {
		return ErrPeer
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (256<<20)+1))
	if err != nil || n > 256<<20 || ctx.Err() != nil {
		return ErrPeer
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) || info.Size() != after.Size() {
		return ErrPeer
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	for _, expected := range approved {
		if ValidNonce(expected) && digest == expected {
			return nil
		}
	}
	return ErrPeer
}
