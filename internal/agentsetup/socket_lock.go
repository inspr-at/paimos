// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/inspr-at/paimos/internal/hooknote"
	"golang.org/x/sys/unix"
)

// LockSocket acquires the listener's lifetime lock before inspecting or removing
// any socket artifacts. The caller must retain the returned descriptor until
// the listener is closed and its socket/token cleanup is complete. Clients do
// not take this lock. As with Store.Lock, it serializes cooperating daemons,
// not hostile code running as the same uid. Never unlink the lock file.
// peerStore optionally identifies daemon state when a short fallback socket
// lives elsewhere. Its hook pin is checked under the same lifetime lock.
func (s *Store) LockSocket(name string, peerStore ...*Store) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, err := s.socketPeerStore(peerStore)
	if err != nil {
		return nil, err
	}
	if peer != s {
		peer.mu.Lock()
		defer peer.mu.Unlock()
	}
	if !validName(name) || !validName(name+".owner.json") {
		return nil, ErrUnsafePath
	}
	lock, err := s.lockNamed(name + ".lock")
	if errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("agentd is already running for this state root: %w", err)
	}
	if err != nil {
		return nil, err
	}
	if err := s.cleanSocketArtifacts(name, lock, nil, peer); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

// CleanupSocket removes only this listener's recorded socket/token inodes,
// after closing the listener and while retaining its verified lifetime lock.
// Lost lock identity leaves residue for a later, verified owner to recover.
func (s *Store) CleanupSocket(name string, lock *os.File, socketInfo, tokenInfo os.FileInfo, peerStore ...*Store) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	peer, err := s.socketPeerStore(peerStore)
	if err != nil {
		return err
	}
	if peer != s {
		peer.mu.Lock()
		defer peer.mu.Unlock()
	}
	return s.cleanSocketArtifacts(name, lock, map[string]os.FileInfo{
		name: socketInfo, name + ".token": tokenInfo,
	}, peer)
}

func (s *Store) socketPeerStore(stores []*Store) (*Store, error) {
	if len(stores) == 0 {
		return s, nil
	}
	if len(stores) != 1 || stores[0] == nil || stores[0].root == nil {
		return nil, ErrUnsafePath
	}
	return stores[0], nil
}

type socketArtifact struct {
	name  string
	kind  uint32
	dev   uint64
	ino   uint64
	store *Store
	hook  bool
}

func (a socketArtifact) private(st *unix.Stat_t) bool {
	if a.hook {
		// An owner-only but unreadable hook pin is recoverable too. Never
		// follow links or relax socket/token/lock permissions to recover it.
		return uint32(st.Mode)&unix.S_IFMT == unix.S_IFREG && st.Mode&07777&^0600 == 0 && int(st.Uid) == os.Getuid() && st.Nlink == 1
	}
	return privateArtifact(st, a.kind)
}

type socketPeer struct {
	PID        int
	Started    string
	Executable string
}

// Only the ownership fields matter here. Pin.Valid is deliberately not used:
// another release's optional fields must never hide an otherwise live PID.
func parseSocketPeer(raw []byte) socketPeer {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return socketPeer{}
	}
	var peer socketPeer
	_ = json.Unmarshal(fields["pid"], &peer.PID)
	_ = json.Unmarshal(fields["started"], &peer.Started)
	_ = json.Unmarshal(fields["exe"], &peer.Executable)
	return peer
}

func (p socketPeer) live(kill func(int, syscall.Signal) error, observe func(int) (hooknote.Process, error)) bool {
	if p.PID < 1 || p.PID > 1<<31-1 || errors.Is(kill(p.PID, 0), syscall.ESRCH) {
		return false
	}
	// A permission failure or incomplete observation cannot prove exit. Only
	// compare the saved image to this PID, never to the starting binary.
	current, err := observe(p.PID)
	if err != nil || current.PID != p.PID || p.Started == "" || p.Executable == "" || current.Started == "" || current.Executable == "" {
		return true
	}
	return p.Started == current.Started && p.Executable == current.Executable
}

func (s *Store) socketHookArtifact(shutdown bool) (*socketArtifact, error) {
	item := socketArtifact{name: "hook-peer.json", kind: unix.S_IFREG, store: s, hook: true}
	var st unix.Stat_t
	if err := unix.Fstatat(int(s.root.Fd()), item.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	if !item.private(&st) {
		return nil, fmt.Errorf("validate hook peer %s: %w", filepath.Join(s.path, item.name), ErrUnsafePath)
	}
	item.dev, item.ino = uint64(st.Dev), uint64(st.Ino)
	var peer socketPeer
	if st.Mode&0400 != 0 {
		fd, err := unix.Openat(int(s.root.Fd()), item.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			if shutdown {
				return nil, nil
			}
			return &item, nil
		}
		f := os.NewFile(uintptr(fd), "socket-hook-peer")
		var opened unix.Stat_t
		statErr := unix.Fstat(fd, &opened)
		if statErr != nil || !item.private(&opened) || opened.Dev != st.Dev || opened.Ino != st.Ino {
			f.Close()
			return nil, ErrCollision
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if readErr == nil && len(raw) <= 4096 {
			peer = parseSocketPeer(raw)
		}
	}
	if shutdown {
		if peer.PID != os.Getpid() {
			return nil, nil
		}
	} else if peer.live(syscall.Kill, hooknote.Observe) {
		return nil, fmt.Errorf("agentd is already running for this state root (hook-peer.json names live pid %d): %w", peer.PID, ErrBusy)
	}
	return &item, nil
}

func privateArtifact(st *unix.Stat_t, kind uint32) bool {
	return uint32(st.Mode) == kind|0600 && int(st.Uid) == os.Getuid() && st.Nlink == 1
}

// Old recovery generated exactly .s plus eight lowercase hex digits, for both
// sockets and tokens. Do not adopt unrelated dotfiles sharing the .s prefix.
func legacySocketAside(name string) bool {
	return len(name) == 10 && strings.HasPrefix(name, ".s") && strings.Trim(name[2:], "0123456789abcdef") == ""
}

// cleanSocketArtifacts requires a verified lifetime lock and no live hook peer.
// Connection refusal cannot prove staleness: Darwin also refuses connections
// when a live listener's accept queue is full. All checks and removals use the
// pinned directory fd. A same-uid process can disrupt a daemon by deleting or
// replacing its lock file or directory; this is outside the protection boundary
// (that process can already signal or kill the daemon).
func (s *Store) cleanSocketArtifacts(name string, lock *os.File, owned map[string]os.FileInfo, peer *Store) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("clean local socket artifacts %s: %w", filepath.Join(s.path, name), resultErr)
		}
	}()
	if s.readOnly || peer.readOnly {
		return ErrReadOnly
	}
	if err := s.verifyLock(name+".lock", lock); err != nil {
		return err
	}
	hook, err := peer.socketHookArtifact(owned != nil)
	if err != nil {
		return err
	}
	items := []socketArtifact{
		{name: name, kind: unix.S_IFSOCK},
		{name: name + ".token", kind: unix.S_IFREG},
	}
	if owned == nil {
		items = append(items, socketArtifact{name: name + ".owner.json", kind: unix.S_IFREG})
		// Open a fresh directory cursor relative to the pinned fd, not the path.
		fd, err := unix.Openat(int(s.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		dir := os.NewFile(uintptr(fd), "socket-artifacts")
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, entry := range names {
			if legacySocketAside(entry) {
				items = append(items, socketArtifact{name: entry})
			}
		}
	}
	// Validate the complete set before removing anything, so an unsafe token,
	// old owner record or aside cannot cause partial cleanup of safe artifacts.
	present := make([]socketArtifact, 0, len(items))
	for _, item := range items {
		item.store = s
		if owned != nil && owned[item.name] == nil {
			continue
		}
		var st unix.Stat_t
		err := unix.Fstatat(int(s.root.Fd()), item.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat socket artifact %s: %w", filepath.Join(s.path, item.name), err)
		}
		if item.kind == 0 {
			item.kind = uint32(st.Mode) & unix.S_IFMT
		}
		if (item.kind != unix.S_IFSOCK && item.kind != unix.S_IFREG) || !privateArtifact(&st, item.kind) {
			return fmt.Errorf("validate socket artifact %s: %w", filepath.Join(s.path, item.name), ErrUnsafePath)
		}
		if owned != nil {
			original, ok := owned[item.name].Sys().(*syscall.Stat_t)
			if !ok || uint64(original.Dev) != uint64(st.Dev) || uint64(original.Ino) != uint64(st.Ino) {
				return fmt.Errorf("socket artifact %s: %w", filepath.Join(s.path, item.name), ErrCollision)
			}
		}
		item.dev, item.ino = uint64(st.Dev), uint64(st.Ino)
		present = append(present, item)
	}
	if hook != nil {
		present = append(present, *hook)
	}
	for _, item := range present {
		if err := s.verifyLock(name+".lock", lock); err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(item.store.root.Fd()), item.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
			!item.private(&st) || uint64(st.Dev) != item.dev || uint64(st.Ino) != item.ino {
			return fmt.Errorf("socket artifact %s: %w", filepath.Join(item.store.path, item.name), ErrCollision)
		}
		if err := unix.Unlinkat(int(item.store.root.Fd()), item.name, 0); err != nil {
			return fmt.Errorf("unlink socket artifact %s: %w", filepath.Join(item.store.path, item.name), err)
		}
	}
	err = unix.Fsync(int(s.root.Fd()))
	if hook != nil && peer != s {
		err = errors.Join(err, unix.Fsync(int(peer.root.Fd())))
	}
	return err
}
