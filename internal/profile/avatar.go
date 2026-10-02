// SPDX-License-Identifier: AGPL-3.0-only
package profile

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
)

const maxAvatarBytes = 8 << 20

type Crop struct {
	X    int `json:"x"`
	Y    int `json:"y"`
	Size int `json:"size"`
}

// ProcessAvatar decodes PNG, JPEG or WebP, applies JPEG EXIF orientation,
// strips metadata by PNG re-encoding, and produces exact square variants.
func ProcessAvatar(data []byte, crop Crop) ([]byte, map[int][]byte, error) {
	if len(data) == 0 || len(data) > maxAvatarBytes {
		return nil, nil, badInput("file must be 1–8 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, nil, badInput("invalid image")
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return nil, nil, badInput("image must be PNG, JPEG or WebP")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 100_000_000 {
		return nil, nil, badInput("image dimensions exceed limit")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, badInput("invalid image")
	}
	if format == "jpeg" {
		img = orient(img, jpegOrientation(data))
	}
	b := img.Bounds()
	if crop.X < 0 || crop.Y < 0 || crop.Size < 1 || crop.X > b.Dx()-crop.Size || crop.Y > b.Dy()-crop.Size {
		return nil, nil, badInput("crop outside oriented image")
	}
	var original bytes.Buffer
	if err := png.Encode(&original, img); err != nil {
		return nil, nil, err
	}
	selected := image.NewRGBA(image.Rect(0, 0, crop.Size, crop.Size))
	draw.Draw(selected, selected.Bounds(), img, image.Point{X: b.Min.X + crop.X, Y: b.Min.Y + crop.Y}, draw.Src)
	variants := map[int][]byte{}
	for _, size := range []int{32, 64, 128, 256} {
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.CatmullRom.Scale(dst, dst.Bounds(), selected, selected.Bounds(), draw.Over, nil)
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			return nil, nil, err
		}
		variants[size] = buf.Bytes()
	}
	return original.Bytes(), variants, nil
}
func orient(src image.Image, n int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if n < 2 || n > 8 {
		return src
	}
	ow, oh := w, h
	if n >= 5 {
		ow, oh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := x, y
			switch n {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 1
		}
		marker := data[i+1]
		i += 2
		if marker == 0xd9 || marker == 0xda {
			return 1
		}
		if i+2 > len(data) {
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[i : i+2]))
		if n < 2 || i+n > len(data) {
			return 1
		}
		segment := data[i+2 : i+n]
		i += n
		if marker != 0xe1 || len(segment) < 14 || string(segment[:6]) != "Exif\x00\x00" {
			continue
		}
		t := segment[6:]
		var order binary.ByteOrder
		switch string(t[:2]) {
		case "II":
			order = binary.LittleEndian
		case "MM":
			order = binary.BigEndian
		default:
			return 1
		}
		if order.Uint16(t[2:4]) != 42 {
			return 1
		}
		off := int(order.Uint32(t[4:8]))
		if off < 0 || off+2 > len(t) {
			return 1
		}
		count := int(order.Uint16(t[off : off+2]))
		for j := 0; j < count; j++ {
			at := off + 2 + j*12
			if at+12 > len(t) {
				return 1
			}
			if order.Uint16(t[at:at+2]) == 0x112 && order.Uint16(t[at+2:at+4]) == 3 && order.Uint32(t[at+4:at+8]) == 1 {
				v := int(order.Uint16(t[at+8 : at+10]))
				if v >= 1 && v <= 8 {
					return v
				}
				return 1
			}
		}
	}
	return 1
}
func (m *Module) stageAvatar(ctx *http.Request, tenantID string, data []byte, crop Crop) (staged []*attachments.Staged, err error) {
	original, variants, err := ProcessAvatar(data, crop)
	if err != nil {
		return nil, err
	}
	store := m.Store
	store.MaxSize = 0 // re-encoded PNG can exceed the upload limit
	defer func() {
		if err != nil {
			for _, blob := range staged {
				_ = blob.Close()
			}
		}
	}()
	for _, raw := range [][]byte{original, variants[32], variants[64], variants[128], variants[256]} {
		blob, e := store.Stage(ctx.Context(), tenantID, bytes.NewReader(raw))
		if e != nil {
			return staged, e
		}
		staged = append(staged, blob)
	}
	return staged, nil
}

func (m *Module) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		httpapi.WriteError(w, 400, "multipart body required")
		return
	}
	var file, cropJSON []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			httpapi.WriteError(w, 400, "invalid multipart body")
			return
		}
		switch part.FormName() {
		case "file":
			if file != nil {
				httpapi.WriteError(w, 400, "one file required")
				return
			}
			file, err = io.ReadAll(io.LimitReader(part, maxAvatarBytes+1))
		case "crop":
			cropJSON, err = io.ReadAll(io.LimitReader(part, 1024))
		}
		if err != nil {
			httpapi.WriteError(w, 400, "invalid multipart body")
			return
		}
	}
	if len(file) > maxAvatarBytes {
		httpapi.WriteError(w, 413, "avatar exceeds 8 MiB")
		return
	}
	var crop Crop
	dec := json.NewDecoder(bytes.NewReader(cropJSON))
	dec.DisallowUnknownFields()
	if dec.Decode(&crop) != nil || dec.Decode(new(any)) != io.EOF || crop.Size < 1 {
		httpapi.WriteError(w, 400, "invalid crop")
		return
	}
	staged, err := m.stageAvatar(r, p.TenantID, file, crop)
	if err != nil {
		var invalid invalidInput
		if errors.As(err, &invalid) {
			httpapi.WriteError(w, 400, invalid.Error())
		} else {
			httpapi.WriteError(w, 500, "avatar storage failed")
		}
		return
	}
	defer func() {
		for _, blob := range staged {
			_ = blob.Close()
		}
	}()
	var out Profile
	err = db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		email, err := identityEmail(r.Context(), tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		before, exists, err := read(r.Context(), tx, p.TenantID, p.ID, true)
		if err != nil {
			return err
		}
		if err := attachments.Publish(r.Context(), tx, attachments.OwnerAvatar, staged...); err != nil {
			return err
		}
		after := before
		after.AvatarOriginalHash = staged[0].SHA256
		after.AvatarHashes = map[string]string{}
		for i, size := range []int{32, 64, 128, 256} {
			after.AvatarHashes[strconv.Itoa(size)] = staged[i+1].SHA256
		}
		if err := change(r.Context(), tx, p, before, exists, after); err != nil {
			return err
		}
		if !same(before, after) {
			after.Revision++
		}
		out = after.public(email)
		return nil
	})
	if err != nil {
		apiError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	p, ok := actor(w, r)
	if !ok {
		return
	}
	var out Profile
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		email, err := identityEmail(r.Context(), tx, p.TenantID, p.ID)
		if err != nil {
			return err
		}
		before, exists, err := read(r.Context(), tx, p.TenantID, p.ID, true)
		if err != nil {
			return err
		}
		after := before
		after.AvatarOriginalHash = ""
		after.AvatarHashes = map[string]string{}
		if err := change(r.Context(), tx, p, before, exists, after); err != nil {
			return err
		}
		if !same(before, after) {
			after.Revision++
		}
		out = after.public(email)
		return nil
	})
	if err != nil {
		apiError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) avatar(w http.ResponseWriter, r *http.Request) {
	p, ok := viewer(w, r)
	if !ok {
		return
	}
	id := r.PathValue("principalId")
	size := r.PathValue("size")
	if !validUUID(id) || (size != "32" && size != "64" && size != "128" && size != "256") {
		httpapi.WriteError(w, 400, "invalid avatar path")
		return
	}
	var hash string
	err := db.InTenant(r.Context(), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		if err := ensurePerson(r.Context(), tx, p.TenantID, id); err != nil {
			return err
		}
		s, _, err := read(r.Context(), tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		hash = s.AvatarHashes[size]
		if hash == "" {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		apiError(w, err)
		return
	}
	f, err := m.Store.Open(p.TenantID, hash, "original")
	if err != nil {
		if os.IsNotExist(err) {
			httpapi.WriteError(w, 404, "avatar not found")
			return
		}
		apiError(w, err)
		return
	}
	defer f.Close()
	// The hash is the content address of the exact returned PNG.
	etag := `"` + hash + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("v") == hash {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	fi, err := f.Stat()
	if err != nil {
		apiError(w, err)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	_, _ = io.Copy(w, f)
}
