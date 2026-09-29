// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"golang.org/x/sys/unix"
)

// ControlReference is published only after the local listener is ready.
// Socket is either a recognized basename or the exact per-state fallback path.
type ControlReference struct {
	Socket     string `json:"socket"`
	DaemonID   string `json:"daemon_id"`
	Generation string `json:"generation"`
}

// ResolveSocketPath is shared by paired startup, setup preflight and clients.
// A nil reference selects a new listener; existing references also recognize
// the generation-specific names used before AEON-376. It creates no state.
func ResolveSocketPath(state string, ref *ControlReference) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return resolveSocketPath(runtime.GOOS, home, state, ref)
}

func resolveSocketPath(goos, home, state string, ref *ControlReference) (string, error) {
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || state == "/" {
		return "", ErrUnsafePath
	}
	// Keep space below sun_path's 104 (Darwin) / 108 (Linux) bytes, including
	// its terminating NUL. len counts bytes, including multibyte usernames.
	limit := 100
	if goos == "linux" {
		limit = 104
	}
	if ref != nil && (ref.DaemonID == "" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(ref.Generation)) {
		return "", errors.New("local daemon reference unavailable")
	}
	if ref != nil && ref.Socket == "agentd-"+ref.Generation+".sock" {
		legacy := filepath.Join(state, ref.Socket)
		// Existing listeners may use the bytes reserved as margin for new
		// paths. Preserve them up to the actual sun_path limit (minus NUL).
		if len(legacy) < limit+4 {
			if err := CheckSocket(legacy); err == nil {
				return legacy, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
	path := filepath.Join(state, "agentd.sock")
	if len(path) > limit {
		if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
			return "", ErrUnsafePath
		}
		// 64 hash bits keep independent setup roots separate without placing
		// their full paths in sun_path. The listener also checks its owner ID.
		path = filepath.Join(home, ".aeon", "run", Hash([]byte(state))[:16]+".sock")
		if len(path) > limit {
			return "", fmt.Errorf("The agentd socket path is too long for this system (%d > %d bytes, with safety margin). Use a shorter --setup-root.", len(path), limit)
		}
	}
	if ref != nil && ref.Socket != "agentd-"+ref.Generation+".sock" && ref.Socket != "agentd.sock" && ref.Socket != path {
		return "", errors.New("local daemon reference unavailable")
	}
	return path, nil
}

// PrepareSocketDirectory keeps the short fallback as private as paired state.
// OpenStore rejects symlinks (including ancestors), foreign owners and modes;
// it never repairs an existing unsafe directory.
func PrepareSocketDirectory(socket string) error {
	dir := filepath.Dir(socket)
	if filepath.Base(dir) == "run" && filepath.Base(filepath.Dir(dir)) == ".aeon" {
		parent, err := OpenStore(filepath.Dir(dir), true)
		if err != nil {
			return err
		}
		parent.Close()
	}
	s, err := OpenStore(dir, true)
	if err != nil {
		return err
	}
	return s.Close()
}

// CheckSocket inspects an existing socket through a private directory handle;
// neither the socket nor any of its ancestors may be symlinks.
func CheckSocket(socket string) error {
	s, err := OpenStore(filepath.Dir(socket), false)
	if err != nil {
		return err
	}
	defer s.Close()
	var st unix.Stat_t
	err = unix.Fstatat(int(s.root.Fd()), filepath.Base(socket), &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return os.ErrNotExist
	}
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0777 != 0600 || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return ErrUnsafePath
	}
	return nil
}
