// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

type socketInode struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type socketOwner struct {
	DaemonID string      `json:"daemon_id"`
	State    string      `json:"state"`
	Socket   socketInode `json:"socket"`
	Token    socketInode `json:"token"`
}

func privateSocketInode(path string, mode os.FileMode) (socketInode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return socketInode{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode() != mode|0600 || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return socketInode{}, agentsetup.ErrUnsafePath
	}
	return socketInode{Device: uint64(st.Dev), Inode: uint64(st.Ino)}, nil
}

// ServePairedLocal gives the stable socket an exclusive lifetime lock. After a
// crash it removes recorded inodes, or a strictly validated refused socket left
// before its owner record was written. Local auth is unchanged.
func ServePairedLocal(s *Supervisor, socket string, attachments ...*AttachManager) (*LocalServer, error) {
	if s == nil || s.state == nil {
		return nil, errors.New("invalid paired local socket")
	}
	store, err := agentsetup.OpenStore(filepath.Dir(socket), false)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			store.Close()
		}
	}()
	lock, err := store.LockNamed(filepath.Base(socket) + ".lock")
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			lock.Close()
		}
	}()
	name := filepath.Base(socket) + ".owner.json"
	if raw, err := store.Read(name, 4096); err == nil {
		var owner socketOwner
		if json.Unmarshal(raw, &owner) != nil || owner.DaemonID != s.DaemonID() || owner.State != s.state.Path() {
			return nil, agentsetup.ErrCollision
		}
		// Validate both before removing either. A swapped inode, symlink,
		// hardlink, unsafe mode or foreign owner closes the recovery gate.
		for _, item := range []struct {
			path string
			mode os.FileMode
			want socketInode
		}{{socket, os.ModeSocket, owner.Socket}, {socket + ".token", 0, owner.Token}} {
			got, err := privateSocketInode(item.path, item.mode)
			if !errors.Is(err, os.ErrNotExist) && (err != nil || got != item.want) {
				return nil, agentsetup.ErrCollision
			}
		}
		for _, path := range []string{socket, socket + ".token"} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if err := store.RemoveExact(name, agentsetup.Hash(raw)); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if err := recoverUnrecordedSocket(socket); err != nil {
		return nil, err
	}
	local, err := ServeLocal(s, socket, attachments...)
	if err != nil {
		return nil, err
	}
	owner := socketOwner{DaemonID: s.DaemonID(), State: s.state.Path()}
	owner.Socket, err = privateSocketInode(socket, os.ModeSocket)
	if err == nil {
		owner.Token, err = privateSocketInode(socket+".token", 0)
	}
	if err != nil {
		local.Close()
		return nil, err
	}
	raw, _ := json.Marshal(owner)
	if err := store.Write(name, raw, true); err != nil {
		local.Close()
		return nil, err
	}
	var once sync.Once
	local.release = func() {
		once.Do(func() {
			_ = store.RemoveExact(name, agentsetup.Hash(raw))
			_ = lock.Close()
			_ = store.Close()
		})
	}
	keep = true
	return local, nil
}

// Called only with the lifetime lock held and no owner record. A token alone
// is never adopted: only a private, owned socket that refuses a real connect
// establishes the bind-before-owner crash window. Validate both artifacts and
// recheck their identities after the connect before removing either one.
func recoverUnrecordedSocket(socket string) error {
	want, err := privateSocketInode(socket, os.ModeSocket)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	token, tokenErr := privateSocketInode(socket+".token", 0)
	if tokenErr != nil && !errors.Is(tokenErr, os.ErrNotExist) {
		return tokenErr
	}
	conn, err := net.DialTimeout("unix", socket, 250*time.Millisecond)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return agentsetup.ErrCollision
	}
	if _, err := os.Lstat(socket + ".owner.json"); !errors.Is(err, os.ErrNotExist) {
		return agentsetup.ErrCollision
	}
	if got, err := privateSocketInode(socket, os.ModeSocket); err != nil || got != want {
		return agentsetup.ErrCollision
	}
	got, err := privateSocketInode(socket+".token", 0)
	if tokenErr == nil {
		if err != nil || got != token {
			return agentsetup.ErrCollision
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return agentsetup.ErrCollision
	}
	// Leave the recoverable socket until last: another crash must not turn
	// this pair into an unrecorded token with no socket to prove staleness.
	if tokenErr == nil {
		if err := os.Remove(socket + ".token"); err != nil {
			return err
		}
	}
	return os.Remove(socket)
}
