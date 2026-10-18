// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
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

	"github.com/inspr-at/paimos/internal/releasehistory"
	"golang.org/x/sys/unix"
)

var errHookArtifact = errors.New("artifact_untrusted")

// This is an independent release trust anchor, never read from a downloaded
// checksum, user settings or environment. Linux remains watch-only until the
// owning release-signing work supplies its reviewed public key and manifest.
const hookManifestPublicKey = ""
const hookAppleRequirement = `anchor apple generic and identifier "aeon-cli" and certificate leaf[subject.OU] = "P66J39QV6V"`

// The signer proves origin; this separate, compiled release ceiling proves
// that these exact bytes were reviewed for hook use. A receipt, environment,
// downloaded checksum or a new release by the same signer cannot extend it.
type hookRelease struct {
	VersionScheme, Version, OS, Arch, SHA256 string
}

func approvedHookReleases() []hookRelease {
	// No artifact has completed native S2-4/S2-5 qualification. The coordinator
	// must add exact published digests with that evidence before activation.
	return nil
}

func approvedHookRelease(digest, goos, arch string, ceiling []hookRelease) (hookRelease, bool) {
	for _, r := range ceiling {
		if r.SHA256 == digest && r.OS == goos && r.Arch == arch && validHookRelease(r) {
			return r, true
		}
	}
	return hookRelease{}, false
}

func validHookRelease(r hookRelease) bool {
	return hashPattern.MatchString(r.SHA256) && releasehistory.CalendarScheme(r.VersionScheme) && releasehistory.ValidVersion(r.Version) &&
		(r.OS == "darwin" || r.OS == "linux") && (r.Arch == "arm64" || r.Arch == "amd64")
}

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
	return p.matchesLoadedImage(context.Background(), device, inode, authenticateHook) == nil
}

func (p HookPin) matchesLoadedImage(ctx context.Context, device, inode uint64, authenticate func(context.Context, string, string) error) error {
	if p.Device != device || p.Inode != inode {
		return errors.New("artifact_changed")
	}
	return p.authenticate(ctx, authenticate)
}

func (p HookPin) authenticate(ctx context.Context, verify func(context.Context, string, string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	if err := p.Check(); err != nil {
		return err
	}
	if verify(ctx, p.Path, p.Digest) != nil || ctx.Err() != nil {
		return errHookArtifact
	}
	// Signature verification uses a pathname. Re-observe the pinned bytes and
	// inode after it returns, so a swap during the external check cannot pass.
	return p.Check()
}

func authenticateHook(ctx context.Context, path, digest string) error {
	return authenticateHookRelease(ctx, path, digest, runtime.GOOS, runtime.GOARCH, approvedHookReleases(), authenticateHookSignature)
}

func authenticateHookRelease(ctx context.Context, path, digest, goos, arch string, ceiling []hookRelease, verify func(context.Context, string, hookRelease) error) error {
	release, ok := approvedHookRelease(digest, goos, arch, ceiling)
	if !ok {
		return errHookArtifact
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if verify(ctx, path, release) != nil || ctx.Err() != nil {
		return errHookArtifact
	}
	return nil
}

func authenticateHookSignature(ctx context.Context, path string, release hookRelease) error {
	if release.OS == "darwin" {
		return verifyHookAppleSignature(ctx, path)
	}
	if release.OS != "linux" {
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
	return verifyHookManifest(manifest, signature, key, release)
}

func verifyHookAppleSignature(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--all-architectures", "-R="+hookAppleRequirement, path)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Run() != nil {
		return errHookArtifact
	}
	return nil
}

func verifyHookManifest(raw, signature, key []byte, release hookRelease) error {
	if len(raw) > 8192 || len(signature) != ed25519.SignatureSize || len(key) != ed25519.PublicKeySize || !validHookRelease(release) || !ed25519.Verify(key, raw, signature) {
		return errHookArtifact
	}
	var m struct {
		Schema, Artifact string
		hookRelease
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !uniqueHookJSON(raw) || d.Decode(&m) != nil || m.Schema != "aeon.hook-release.v1" || m.Artifact != "aeon-cli" || m.hookRelease != release {
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
	if runtime.GOOS == "linux" {
		// Runtime must authenticate independently after installation, including
		// after the original release directory/profile disappears. Retain the
		// public signed evidence alongside the copied bytes, never a receipt key.
		for suffix, max := range map[string]int64{".manifest.json": 8192, ".manifest.sig": 256} {
			if err := pinHookPublicFile(store, resolved+suffix, name+suffix, max); err != nil {
				return HookPin{}, errHookArtifact
			}
		}
	}
	_, pin, err := readHookImage(path)
	if err != nil || pin.Digest != initial.Digest || checkHookSource(initial) != nil {
		return HookPin{}, errors.New("artifact_changed")
	}
	if authenticate(ctx, path, pin.Digest) != nil || checkHookSource(initial) != nil || pin.Check() != nil {
		return HookPin{}, errHookArtifact
	}
	return pin, nil
}

func pinHookPublicFile(store *Store, source, name string, max int64) error {
	raw, err := readHookPublicFile(source, max)
	if err != nil {
		return errHookArtifact
	}
	if err = store.Write(name, raw, true); errors.Is(err, ErrCollision) {
		existing, e := store.Read(name, max)
		if e == nil && bytes.Equal(existing, raw) {
			return nil
		}
	}
	return err
}
