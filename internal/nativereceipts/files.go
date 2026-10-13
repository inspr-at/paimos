// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"golang.org/x/sys/unix"
)

const journalFileBytes = 8 << 20
const outboxFileBytes = 32 << 20 // JSON escaping plus bounded metadata/text.

// Store holds an exclusive owner-only lock for its lifetime. Use a dedicated
// subdirectory of the already protected daemon state root, never an account
// directory. The caller still owns the native writer/process lock.
type Store struct {
	mu               sync.Mutex
	dir              *os.File
	lock             *os.File
	now              func() time.Time
	validator        *nativecontract.Validator
	entries          []Intent
	finals           []finalEntry
	closed, poisoned bool
	busy             map[string]bool
}

func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "native-receipts")
	if err = privateFile(dir, true); err != nil {
		dir.Close()
		return nil, err
	}
	lfd, err := unix.Openat(fd, "lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		dir.Close()
		return nil, err
	}
	lock := os.NewFile(uintptr(lfd), "native-receipts-lock")
	if err = privateFile(lock, false); err == nil {
		err = unix.Flock(lfd, unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		lock.Close()
		dir.Close()
		return nil, err
	}
	v, err := nativecontract.NewValidator()
	s := &Store{dir: dir, lock: lock, now: now, validator: v, busy: map[string]bool{}}
	if err == nil {
		err = s.read("receipts.json", journalFileBytes, &s.entries)
	}
	if err == nil {
		err = s.read("finals.json", outboxFileBytes, &s.finals)
	}
	if err == nil {
		// At most two crash leftovers exist. Remove only our exact temporary
		// names after validating the committed files, without reading bodies.
		for _, name := range []string{".pending-receipts.json", ".pending-finals.json"} {
			if e := unix.Unlinkat(fd, name, 0); e != nil && !errors.Is(e, unix.ENOENT) {
				err = e
				break
			}
		}
	}
	if err == nil {
		err = s.recover()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func privateFile(f *os.File, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return err
	}
	want := uint32(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if uint32(stat.Mode)&unix.S_IFMT != want || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || !directory && stat.Nlink != 1 {
		return Fail(nativecontract.ReasonRetentionUnapproved)
	}
	return nil
}

func (s *Store) read(name string, limit int64, out any) error {
	fd, err := unix.Openat(int(s.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err = privateFile(f, false); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return Fail(nativecontract.ReasonBufferFull)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return Fail(nativecontract.ReasonBufferFull)
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	return nil
}

// Rename plus file/directory fsync is the commit boundary. A failed commit
// poisons this handle, so no possible native write can follow uncertain disk I/O.
func (s *Store) save(name string, value any, limit int) error {
	if s.closed || s.poisoned {
		return Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	if len(raw) > limit {
		return Fail(nativecontract.ReasonBufferFull)
	}
	tmp := ".pending-" + name
	fd, err := unix.Openat(int(s.dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer unix.Unlinkat(int(s.dir.Fd()), tmp, 0)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat(int(s.dir.Fd()), tmp, int(s.dir.Fd()), name)
	}
	if err == nil {
		err = s.dir.Sync()
	}
	if err != nil {
		s.poisoned = true
	}
	return err
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.lock.Close(), s.dir.Close())
}
