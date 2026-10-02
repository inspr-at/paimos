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
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/imagework"

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

func (s Store) Put(ctx context.Context, tenant string, src io.Reader) (Prepared, error) {
	return s.PutNamed(ctx, tenant, src, "")
}

// PutNamed accepts HTML fragments that sniff as plain text only when their
// filename explicitly selects HTML. A supplied MIME header is never authority.
func (s Store) PutNamed(ctx context.Context, tenant string, src io.Reader, name string) (Prepared, error) {
	if !validTenant(tenant) {
		return Prepared{}, errors.New("invalid tenant")
	}
	// Spool to a private file, then sniff and address by the computed digest.
	spoolDir := filepath.Join(s.root(), tenant, "incoming")
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		return Prepared{}, err
	}
	tmp, err := os.CreateTemp(spoolDir, "upload-*")
	if err != nil {
		return Prepared{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, s.limit()+1))
	if err != nil {
		return Prepared{}, err
	}
	if n > s.limit() {
		return Prepared{}, fmt.Errorf("file exceeds %d bytes", s.limit())
	}
	if n == 0 {
		return Prepared{}, errors.New("empty file")
	}
	if err := tmp.Sync(); err != nil {
		return Prepared{}, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return Prepared{}, err
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
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".html" || ext == ".htm" {
			ct = "text/html; charset=utf-8"
		}
	}
	// Unknown binaries (logs, exports, office files that don't sniff as zip)
	// are stored too; they are only ever served as a download with nosniff
	// and a sandboxing CSP, never inline.
	allowed := ct == "image/png" || ct == "image/jpeg" || ct == "image/gif" || ct == "image/webp" || ct == "application/pdf" || ct == "application/zip" || ct == "text/plain; charset=utf-8" || ct == "application/octet-stream" || isHTML(ct)
	if !allowed {
		return Prepared{}, fmt.Errorf("%w %s", ErrUnsupportedType, ct)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return Prepared{}, err
	}
	out := Prepared{SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: ct, Size: n}
	if strings.HasPrefix(ct, "image/") {
		cfg, format, err := image.DecodeConfig(tmp)
		if err != nil || imagework.Attachment.Check(cfg.Width, cfg.Height) != nil {
			return Prepared{}, errors.New("invalid or oversized image")
		}
		if map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif", "image/webp": "webp"}[ct] != format {
			return Prepared{}, errors.New("image signature mismatch")
		}
		out.Width, out.Height, out.Image = cfg.Width, cfg.Height, true
	} else if ct == "text/plain; charset=utf-8" || isHTML(ct) {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return Prepared{}, err
		}
		b, err := io.ReadAll(tmp)
		if err != nil || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return Prepared{}, errors.New("invalid UTF-8 text")
		}
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	var img image.Image
	if out.Image {
		release, err := imagework.Attachment.Acquire(ctx, out.Width, out.Height)
		if err != nil {
			return Prepared{}, err
		}
		defer release()
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return Prepared{}, err
		}
		img, _, err = image.Decode(tmp)
		if err != nil {
			return Prepared{}, err
		}
		// GIF's first frame can be smaller than its logical screen. Keep that
		// supported while rejecting any frame that exceeds the reserved bounds.
		if img.Bounds().Dx() > out.Width || img.Bounds().Dy() > out.Height {
			return Prepared{}, errors.New("image dimensions differ from header")
		}
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	original, _ := s.path(tenant, out.SHA256, "original")
	if err := writeFromFile(original, tmp); err != nil {
		return Prepared{}, err
	}
	if isHTML(ct) {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return Prepared{}, err
		}
		thumb, err := htmlTextThumbnail(ctx, tmp)
		if err != nil {
			return Prepared{}, err
		}
		target, _ := s.path(tenant, out.SHA256, "thumb")
		if err := writeAtomic(target, bytes.NewReader(thumb)); err != nil {
			return Prepared{}, err
		}
	}
	if out.Image {
		if err := s.writeImageVariants(ctx, tenant, out.SHA256, img); err != nil {
			return Prepared{}, err
		}
	}
	return out, nil
}

// PutGeneratedPNG stores an already decoded, server-generated avatar image and
// its normal attachment variants without another decode. The caller must hold
// imagework.Avatar admission through this call (including all PNG encoding).
// Untrusted upload bytes must go through Put instead.
func (s Store) PutGeneratedPNG(ctx context.Context, tenant string, img image.Image) (Prepared, error) {
	if !validTenant(tenant) {
		return Prepared{}, errors.New("invalid tenant")
	}
	b := img.Bounds()
	if err := imagework.Avatar.Check(b.Dx(), b.Dy()); err != nil {
		return Prepared{}, err
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Prepared{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	out := Prepared{SHA256: hex.EncodeToString(sum[:]), ContentType: "image/png", Size: int64(buf.Len()), Width: b.Dx(), Height: b.Dy(), Image: true}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	original, _ := s.path(tenant, out.SHA256, "original")
	if _, err := os.Stat(original); errors.Is(err, os.ErrNotExist) {
		if err := writeAtomic(original, bytes.NewReader(buf.Bytes())); err != nil {
			return Prepared{}, err
		}
	} else if err != nil {
		return Prepared{}, err
	}
	if err := s.writeImageVariants(ctx, tenant, out.SHA256, img); err != nil {
		return Prepared{}, err
	}
	return out, nil
}

func (s Store) writeImageVariants(ctx context.Context, tenant, hash string, img image.Image) error {
	for _, v := range []struct {
		name string
		max  int
	}{{"thumb", 320}, {"preview", 1600}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, _ := s.path(tenant, hash, v.name)
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, resize(img, v.max)); err != nil {
			return err
		}
		if err := writeAtomic(target, bytes.NewReader(buf.Bytes())); err != nil {
			return err
		}
	}
	return nil
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
