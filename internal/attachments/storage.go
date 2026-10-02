// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
)

const DefaultMaxSize int64 = 50 << 20

// ErrUnsupportedType is returned for content outside the allow-list.
var ErrUnsupportedType = errors.New("unsupported content type")

// Store owns immutable, tenant-namespaced content and its image variants.
// FilesDir defaults to AEON_FILES_DIR, then ./data/files.
type Store struct {
	FilesDir string
	MaxSize  int64
}

func (s Store) root() string {
	if s.FilesDir != "" {
		return s.FilesDir
	}
	if v := os.Getenv("AEON_FILES_DIR"); v != "" {
		return v
	}
	return "./data/files"
}
func (s Store) limit() int64 {
	if s.MaxSize > 0 {
		return s.MaxSize
	}
	return DefaultMaxSize
}
func validTenant(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func (s Store) path(tenant, hash, variant string) (string, error) {
	if !validTenant(tenant) || !validHash(hash) {
		return "", errors.New("invalid tenant or hash")
	}
	suffix := ""
	switch variant {
	case "original", "":
	case "thumb", "preview":
		suffix = "." + variant + ".png"
	default:
		return "", errors.New("invalid variant")
	}
	return filepath.Join(s.root(), tenant, hash[:2], hash[2:4], hash+suffix), nil
}

type Prepared struct {
	SHA256        string
	ContentType   string
	Size          int64
	Width, Height int
	Image         bool
}

// Staged holds private upload bytes. Close it after publishing or on failure.
// Preparing an upload does not make it visible in the content-addressed store.
type Staged struct {
	Prepared
	store    Store
	tenant   string
	original *os.File
	variants map[string][]byte
}

func (s *Staged) Close() error {
	if s.original == nil {
		return nil
	}
	name := s.original.Name()
	err := s.original.Close()
	s.original = nil
	removeErr := os.Remove(name)
	return errors.Join(err, removeErr)
}

// Stage reads and validates the request before opening a database transaction.
func (s Store) Stage(ctx context.Context, tenant string, src io.Reader) (_ *Staged, err error) {
	if !validTenant(tenant) {
		return nil, errors.New("invalid tenant")
	}
	// Spool to a private file, then sniff and address by the computed digest.
	spoolDir := filepath.Join(s.root(), tenant, "incoming")
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(spoolDir, "upload-*")
	if err != nil {
		return nil, err
	}
	staged := &Staged{store: s, tenant: tenant, original: tmp, variants: map[string][]byte{}}
	defer func() {
		if err != nil {
			_ = staged.Close()
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, s.limit()+1))
	if err != nil {
		return nil, err
	}
	if n > s.limit() {
		return nil, fmt.Errorf("file exceeds %d bytes", s.limit())
	}
	if n == 0 {
		return nil, errors.New("empty file")
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	read, _ := io.ReadFull(tmp, head)
	head = head[:read]
	ct := http.DetectContentType(head)
	if ct == "application/octet-stream" && bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		ct = "application/zip"
	}
	if ct == "application/octet-stream" && bytes.HasPrefix(head, []byte("RIFF")) && len(head) > 12 && string(head[8:12]) == "WEBP" {
		ct = "image/webp"
	}
	if strings.HasPrefix(ct, "text/plain") {
		ct = "text/plain; charset=utf-8"
	}
	// Unknown binaries (logs, exports, office files that don't sniff as zip)
	// are stored too; they are only ever served as a download with nosniff
	// and a sandboxing CSP, never inline.
	allowed := ct == "image/png" || ct == "image/jpeg" || ct == "image/gif" || ct == "image/webp" || ct == "application/pdf" || ct == "application/zip" || ct == "text/plain; charset=utf-8" || ct == "application/octet-stream"
	if !allowed {
		return nil, fmt.Errorf("%w %s", ErrUnsupportedType, ct)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	out := Prepared{SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: ct, Size: n}
	if strings.HasPrefix(ct, "image/") {
		cfg, format, err := image.DecodeConfig(tmp)
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 100_000_000 {
			return nil, errors.New("invalid or oversized image")
		}
		if map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif", "image/webp": "webp"}[ct] != format {
			return nil, errors.New("image signature mismatch")
		}
		out.Width, out.Height, out.Image = cfg.Width, cfg.Height, true
	} else if ct == "text/plain; charset=utf-8" {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		b, err := io.ReadAll(tmp)
		if err != nil || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return nil, errors.New("invalid UTF-8 text")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if out.Image {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		img, _, err := image.Decode(tmp)
		if err != nil {
			return nil, err
		}
		for _, v := range []struct {
			name string
			max  int
		}{{"thumb", 320}, {"preview", 1600}} {
			var buf bytes.Buffer
			if err := png.Encode(&buf, resize(img, v.max)); err != nil {
				return nil, err
			}
			staged.variants[v.name] = buf.Bytes()
		}
	}
	staged.Prepared = out
	return staged, nil
}

// Publish takes all lifetime locks in a stable order before installing bytes.
// The caller MUST write the owner's reference in tx before committing it. On
// rollback the files are ordinary orphans, eligible only after the grace period.
func Publish(ctx context.Context, tx pgx.Tx, owner Owner, staged ...*Staged) error {
	if !knownOwner(owner) {
		return errors.New("unregistered blob owner")
	}
	ordered := append([]*Staged(nil), staged...)
	for _, s := range ordered {
		if s == nil || s.original == nil {
			return errors.New("closed staged upload")
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].tenant+ordered[i].SHA256 < ordered[j].tenant+ordered[j].SHA256 })
	for _, s := range ordered {
		if err := lockBlob(ctx, tx, s.tenant, s.SHA256); err != nil {
			return err
		}
	}
	for _, s := range ordered {
		original, err := s.store.path(s.tenant, s.SHA256, "original")
		if err != nil {
			return err
		}
		if err := writeFromFile(original, s.original); err != nil {
			return err
		}
		for _, variant := range []string{"thumb", "preview"} {
			body, exists := s.variants[variant]
			if !exists {
				continue
			}
			target, err := s.store.path(s.tenant, s.SHA256, variant)
			if err != nil {
				return err
			}
			if _, err := os.Stat(target); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := writeAtomic(target, bytes.NewReader(body)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Put is for already-buffered inputs inside the reference transaction. Network
// uploads should Stage first, then Publish inside that transaction.
func (s Store) Put(ctx context.Context, tx pgx.Tx, tenant string, owner Owner, src io.Reader) (Prepared, error) {
	staged, err := s.Stage(ctx, tenant, src)
	if err != nil {
		return Prepared{}, err
	}
	defer staged.Close()
	if err := Publish(ctx, tx, owner, staged); err != nil {
		return Prepared{}, err
	}
	return staged.Prepared, nil
}

// PutProfileAsset stores fonts and safe SVGs already validated by the quote
// profile module. PNGs take the standard image/derivative path. No other caller
// may write directly to the shared tenant/hash layout.
func (s Store) PutProfileAsset(ctx context.Context, tx pgx.Tx, tenant string, raw []byte, contentType string) (Prepared, error) {
	if contentType == "image/png" {
		return s.Put(ctx, tx, tenant, OwnerQuoteProfile, bytes.NewReader(raw))
	}
	switch contentType {
	case "font/ttf", "font/otf", "font/woff2", "image/svg+xml":
	default:
		return Prepared{}, ErrUnsupportedType
	}
	if len(raw) == 0 || len(raw) > 10<<20 {
		return Prepared{}, errors.New("invalid profile asset size")
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if err := lockBlob(ctx, tx, tenant, digest); err != nil {
		return Prepared{}, err
	}
	target, err := s.path(tenant, digest, "original")
	if err != nil {
		return Prepared{}, err
	}
	if err := writeAtomic(target, bytes.NewReader(raw)); err != nil {
		return Prepared{}, err
	}
	return Prepared{SHA256: digest, ContentType: contentType, Size: int64(len(raw))}, nil
}

func resize(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	scale := float64(max) / float64(w)
	if h > w {
		scale = float64(max) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/nw, b.Min.Y+y*h/nh))
		}
	}
	return dst
}
func writeFromFile(path string, src *os.File) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return writeAtomic(path, src)
}
func writeAtomic(path string, src io.Reader) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = io.Copy(f, src); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s Store) Open(tenant, hash, variant string) (*os.File, error) {
	path, err := s.path(tenant, hash, variant)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.Open(path)
}
