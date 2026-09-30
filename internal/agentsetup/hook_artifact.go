// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

var errHookArtifact = errors.New("artifact_untrusted")

// This is an independent release trust anchor, never read from a downloaded
// checksum, user settings or environment. Linux remains watch-only until the
// owning release-signing work supplies its reviewed public key and manifest.
const hookManifestPublicKey = ""
const hookAppleRequirement = `anchor apple generic and identifier "aeon-cli" and certificate leaf[subject.OU] = "P66J39QV6V"`

// HookPin is safe public identity metadata for the daemon's loaded-image check.
// A matching pathname alone is insufficient: compare kernel-observed image
// device/inode at each exchange, then revalidate digest/config/launch ancestry.
type HookPin struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func readHookImage(path string) ([]byte, HookPin, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, HookPin{}, errHookArtifact
	}
	d, err := openDirectory(filepath.Dir(path), false, false)
	// Executables in root-owned release directories are allowed too; the final
	// parent need not be owned by the invoking user.
	if err != nil {
		d, err = openHookImageDirectory(filepath.Dir(path))
	}
	if err != nil {
		return nil, HookPin{}, errHookArtifact
	}
	defer d.Close()
	fd, err := unix.Openat(int(d.root.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, HookPin{}, errHookArtifact
	}
	f := os.NewFile(uintptr(fd), "hook-image")
	defer f.Close()
	var st, after unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Mode&0111 == 0 || (st.Uid != 0 && int(st.Uid) != os.Getuid()) || st.Nlink != 1 {
		return nil, HookPin{}, errHookArtifact
	}
	raw, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil || len(raw) > 256<<20 || unix.Fstat(fd, &after) != nil || st.Dev != after.Dev || st.Ino != after.Ino || st.Size != after.Size || st.Mode != after.Mode {
		return nil, HookPin{}, errHookArtifact
	}
	return raw, HookPin{Path: path, Digest: Hash(raw), Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func openHookImageDirectory(path string) (*Store, error) {
	// Traverse each component without following a profile or directory symlink.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errHookArtifact
	}
	for _, part := range splitPhysicalPath(path) {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, errHookArtifact
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && int(st.Uid) != os.Getuid()) || (st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&unix.S_ISVTX != 0)) {
			unix.Close(fd)
			return nil, errHookArtifact
		}
	}
	return &Store{root: os.NewFile(uintptr(fd), "hook-directory"), path: path}, nil
}

func (p HookPin) Check() error {
	info, err := os.Lstat(p.Path)
	if err != nil || info.Mode().Perm() != 0500 {
		return errors.New("artifact_changed")
	}
	_, now, err := readHookImage(p.Path)
	if err != nil || now != p {
		return errors.New("artifact_changed")
	}
	return nil
}

func checkHookSource(p HookPin) error {
	_, now, err := readHookImage(p.Path)
	if err != nil || now != p {
		return errors.New("artifact_changed")
	}
	return nil
}

func (p HookPin) MatchesLoadedImage(device, inode uint64) bool {
	return p.Device == device && p.Inode == inode && p.Check() == nil
}

func authenticateHook(ctx context.Context, path, digest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--all-architectures", "-R="+hookAppleRequirement, path)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if cmd.Run() != nil {
			return errHookArtifact
		}
		return nil
	}
	if runtime.GOOS != "linux" {
		return errHookArtifact
	}
	key, err := base64.StdEncoding.DecodeString(hookManifestPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errHookArtifact
	}
	// Public manifest/signature, not a checksum fetched alongside the executable.
	manifest, err := readHookPublicFile(path+".manifest.json", 8192)
	if err != nil {
		return errHookArtifact
	}
	signature, err := readHookPublicFile(path+".manifest.sig", 256)
	if err != nil {
		return errHookArtifact
	}
	return verifyHookManifest(manifest, signature, key, digest, runtime.GOOS, runtime.GOARCH)
}

func verifyHookManifest(raw, signature, key []byte, digest, goos, arch string) error {
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, signature) {
		return errHookArtifact
	}
	var m struct{ Schema, Artifact, Version, OS, Arch, SHA256 string }
	if json.Unmarshal(raw, &m) != nil || m.Schema != "aeon.hook-release.v1" || m.Artifact != "aeon-cli" || m.Version == "" || m.OS != goos || m.Arch != arch || m.SHA256 != digest {
		return errHookArtifact
	}
	return nil
}

func readHookPublicFile(path string, max int64) ([]byte, error) {
	d, err := openHookImageDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	raw, _, err := d.readHookSettings(filepath.Base(path), max)
	return raw, err
}

// pinHook copies bytes into a digest-named, private release root. This roots
// Nix-derived artifacts independently of a moving profile and its GC lifetime.
// Authentication happens before and after copying; neither PATH nor a shell
// profile participates. A post-approval source swap aborts installation.
func pinHook(ctx context.Context, source string, store *Store, authenticate func(context.Context, string, string) error) (HookPin, error) {
	if !filepath.IsAbs(source) {
		return HookPin{}, errHookArtifact
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return HookPin{}, errHookArtifact
	}
	raw, initial, err := readHookImage(resolved)
	if err != nil || authenticate(ctx, resolved, initial.Digest) != nil {
		return HookPin{}, errHookArtifact
	}
	again, err := filepath.EvalSymlinks(source)
	if err != nil || again != resolved || checkHookSource(initial) != nil {
		return HookPin{}, errors.New("artifact_changed")
	}
	name := "aeon-" + initial.Digest
	fd, err := unix.Openat(int(store.root.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0500)
	if err == nil {
		f := os.NewFile(uintptr(fd), "pinned-hook")
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil || unix.Fsync(int(store.root.Fd())) != nil {
			return HookPin{}, errHookArtifact
		}
	} else if !errors.Is(err, unix.EEXIST) {
		return HookPin{}, errHookArtifact
	}
	path := filepath.Join(store.Path(), name)
	_, pin, err := readHookImage(path)
	if err != nil || pin.Digest != initial.Digest || checkHookSource(initial) != nil {
		return HookPin{}, errors.New("artifact_changed")
	}
	// Darwin verifies the copied code signature too. Linux's authenticated
	// manifest already bound these exact bytes; it need not be copied beside it.
	if runtime.GOOS == "darwin" && authenticate(ctx, path, pin.Digest) != nil {
		return HookPin{}, errHookArtifact
	}
	return pin, nil
}
