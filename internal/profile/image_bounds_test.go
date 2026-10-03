// SPDX-License-Identifier: AGPL-3.0-only
package profile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/imagework"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"
)

// Admission tests exercise preparation without database I/O inside synctest.
// Publication and the durable avatar references are checked separately below.
func stageAvatarForTest(ctx context.Context, store attachments.Store, tenant string, data []byte, crop Crop) error {
	staged, err := stageAvatarData(ctx, store, tenant, data, crop)
	for _, blob := range staged {
		_ = blob.Close()
	}
	return err
}

// A valid PNG header with no pixel data: a decoder error means the caller
// reached full decoding instead of rejecting dimensions/crops from the header.
func pngHeader(width, height uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	b = binary.BigEndian.AppendUint32(b, 13)
	b = append(b, "IHDR"...)
	b = binary.BigEndian.AppendUint32(b, width)
	b = binary.BigEndian.AppendUint32(b, height)
	b = append(b, 8, 0, 0, 0, 0) // 8-bit grayscale
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[12:]))
}

// Stream zero scanlines into zlib, so even the synthetic bomb fixture never
// allocates a large source bitmap. The result is a complete, tiny PNG.
func compressedPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	row := make([]byte, width+1)
	for range height {
		if _, err := z.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	data := pngHeader(width, height)
	for _, chunk := range []struct {
		kind string
		body []byte
	}{{"IDAT", compressed.Bytes()}, {"IEND", nil}} {
		data = binary.BigEndian.AppendUint32(data, uint32(len(chunk.body)))
		start := len(data)
		data = append(data, chunk.kind...)
		data = append(data, chunk.body...)
		data = binary.BigEndian.AppendUint32(data, crc32.ChecksumIEEE(data[start:]))
	}
	return data
}

func TestCompressedImageBombs(t *testing.T) {
	for _, dimensions := range [][2]uint32{{3000, 2000}, {10000, 10000}} {
		data := compressedPNG(t, dimensions[0], dimensions[1])
		if len(data) > 128<<10 {
			t.Fatalf("fixture unexpectedly large: %d", len(data))
		}
		_, _, err := ProcessAvatar(data, Crop{Size: 1})
		if err == nil || !strings.Contains(err.Error(), "image dimensions exceed limit") {
			t.Fatalf("avatar bomb: %v", err)
		}
		if dimensions[0] == 10000 {
			store := attachments.Store{FilesDir: t.TempDir()}
			_, err := store.Stage(t.Context(), "00000000-0000-0000-0000-000000000559", bytes.NewReader(data))
			if err == nil || !strings.Contains(err.Error(), "invalid or oversized image") {
				t.Fatalf("attachment bomb: %v", err)
			}
		}
	}
}

func TestImageBudgetWeightedAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		avatar, err := imagework.Avatar.Acquire(t.Context(), 2048, 2048)
		if err != nil {
			t.Fatal(err)
		}
		// A small attachment fits alongside a maximum avatar, unlike another
		// maximum image. This distinguishes weighted admission from a mutex.
		small, err := imagework.Attachment.Acquire(t.Context(), 128, 128)
		if err != nil {
			t.Fatal(err)
		}
		large := make(chan func(), 1)
		go func() {
			release, err := imagework.Attachment.Acquire(t.Context(), 4096, 4096)
			if err != nil {
				t.Error(err)
			}
			large <- release
		}()
		synctest.Wait()
		select {
		case release := <-large:
			if release != nil {
				release()
			}
			t.Fatal("admitted image over shared budget")
		default:
		}
		avatar()
		synctest.Wait()
		select {
		case release := <-large:
			if release != nil {
				release()
			}
			t.Fatal("large image plus small image still exceeds budget")
		default:
		}
		small()
		synctest.Wait()
		(<-large)()
	})
}

func TestImageUploadAdmissionAndCancellation(t *testing.T) {
	data := fixture(t)
	store := attachments.Store{FilesDir: t.TempDir()}
	const tenant = "00000000-0000-0000-0000-000000000559"
	synctest.Test(t, func(t *testing.T) {
		release, err := imagework.Attachment.Acquire(t.Context(), 4096, 4096)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		avatarDone, attachmentDone := make(chan error, 1), make(chan error, 1)
		go func() {
			err := stageAvatarForTest(ctx, store, tenant, data, Crop{Size: 300})
			avatarDone <- err
		}()
		go func() {
			_, err := store.Stage(ctx, tenant, bytes.NewReader(data))
			attachmentDone <- err
		}()
		synctest.Wait()
		for _, done := range []chan error{avatarDone, attachmentDone} {
			select {
			case err := <-done:
				t.Fatalf("upload did not wait for admission: %v", err)
			default:
			}
		}
		cancel()
		synctest.Wait()
		for _, done := range []chan error{avatarDone, attachmentDone} {
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled waiter: %v", err)
			}
		}
		release()
	})
	// Cancellation must neither publish bytes nor leak the admission permit.
	if err := filepath.WalkDir(store.FilesDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			t.Errorf("canceled upload left a file: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := stageAvatarForTest(t.Context(), store, tenant, data, Crop{Size: 300}); err != nil {
		t.Fatal(err)
	}
	blob, err := store.Stage(t.Context(), tenant, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
}

func TestAvatarStorageDoesNotReenterDecodeAdmission(t *testing.T) {
	data := fixture(t)
	store := attachments.Store{FilesDir: t.TempDir()}
	synctest.Test(t, func(t *testing.T) {
		large, err := imagework.Avatar.Acquire(t.Context(), 2048, 2048)
		if err != nil {
			t.Fatal(err)
		}
		defer large()
		small, err := imagework.Attachment.Acquire(t.Context(), 128, 128)
		if err != nil {
			t.Fatal(err)
		}
		defer small()
		// Enough headroom for this avatar, but not a second admission while
		// storing its canonical PNG. Passing it through Store.Stage deadlocks.
		done := make(chan error, 1)
		go func() {
			err := stageAvatarForTest(t.Context(), store, "00000000-0000-0000-0000-000000000559", data, Crop{Size: 300})
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("avatar storage tried to acquire another decode reservation")
		}
	})
}

func TestImageAdmissionReleasedOnErrors(t *testing.T) {
	store := attachments.Store{FilesDir: t.TempDir()}
	const tenant = "00000000-0000-0000-0000-000000000559"
	data := fixture(t)
	synctest.Test(t, func(t *testing.T) {
		// Each malformed file passes header validation, then fails decoding.
		if err := processAvatar(t.Context(), pngHeader(2048, 2048), Crop{Size: 2048}, nil); err == nil {
			t.Fatal("accepted truncated avatar")
		}
		if _, err := store.Stage(t.Context(), tenant, bytes.NewReader(pngHeader(4096, 4096))); err == nil {
			t.Fatal("accepted truncated attachment")
		}
		want := errors.New("synthetic storage failure")
		if err := processAvatar(t.Context(), data, Crop{Size: 300}, func(image.Image, map[int]image.Image) error { return want }); !errors.Is(err, want) {
			t.Fatalf("storage failure lost: %v", err)
		}
		// Any leaked reservation above blocks this maximum-sized admission.
		release, err := imagework.Attachment.Acquire(t.Context(), 4096, 4096)
		if err != nil {
			t.Fatal(err)
		}
		release()
	})
}

func TestImageAdmissionRejectsCanceledAndInvalid(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, kind := range []imagework.Kind{imagework.Avatar, imagework.Attachment} {
		if release, err := kind.Acquire(ctx, 64, 64); !errors.Is(err, context.Canceled) {
			if release != nil {
				release()
			}
			t.Fatalf("already canceled admission: %v", err)
		}
		for _, dims := range [][2]int{{0, 1}, {-1, 1}, {1, 0}, {int(^uint(0) >> 1), int(^uint(0) >> 1)}} {
			if release, err := kind.Acquire(t.Context(), dims[0], dims[1]); !errors.Is(err, imagework.ErrDimensions) {
				if release != nil {
					release()
				}
				t.Fatalf("invalid dimensions %v: %v", dims, err)
			}
		}
	}
}

func TestAvatarOversizedUploadHTTP(t *testing.T) {
	store := attachments.Store{FilesDir: t.TempDir()}
	h := http.NewServeMux()
	New(nil, store).Mount(h)
	const id = "00000000-0000-0000-0000-000000000559"
	p := tenant.Principal{ID: id, TenantID: id, Kind: tenant.Person}
	ct, body := avatarBody(t, pngHeader(3000, 2000))
	w := req(t, h, p, "POST", "/api/me/avatar", ct, body)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "image dimensions exceed limit") {
		t.Fatalf("oversized avatar HTTP %d: %s", w.Code, w.Body.String())
	}
	for _, limit := range []string{"4,194,304 pixels", "4096 pixels per side", "crop at most 2048 pixels per side"} {
		if !strings.Contains(w.Body.String(), limit) {
			t.Errorf("avatar rejection missing %q: %s", limit, w.Body.String())
		}
	}
}

func TestAvatarOrientationViews(t *testing.T) {
	src := image.NewRGBA(image.Rect(10, 20, 13, 22))
	for i := range 6 {
		src.SetRGBA(10+i%3, 20+i/3, color.RGBA{R: uint8(i + 1), A: 255})
	}
	want := [][]uint8{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for n := 1; n <= 8; n++ {
		got := orient(src, n)
		b := got.Bounds()
		for i, red := range want[n-1] {
			r, _, _, _ := got.At(b.Min.X+i%b.Dx(), b.Min.Y+i/b.Dx()).RGBA()
			if uint8(r>>8) != red {
				t.Fatalf("orientation %d pixel %d: got %d, want %d", n, i, r>>8, red)
			}
		}
	}
	view := orient(src, 6)
	src.SetRGBA(10, 21, color.RGBA{R: 99, A: 255})
	r, _, _, _ := view.At(0, 0).RGBA()
	if r>>8 != 99 {
		t.Fatal("orientation copied the source bitmap")
	}
}

func TestAvatarCropPixelsAndStoredPNGs(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 9, 7))
	for y := range 7 {
		for x := range 9 {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 21), G: uint8(y * 30), B: 155, A: uint8(60 + x*15)})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, src); err != nil {
		t.Fatal(err)
	}
	crop := Crop{X: 2, Y: 1, Size: 5}
	original, variants, err := ProcessAvatar(data.Bytes(), crop)
	if err != nil {
		t.Fatal(err)
	}
	// Independent reference for the previous RGBA crop-copy behavior.
	selected := image.NewRGBA(image.Rect(0, 0, 5, 5))
	draw.Draw(selected, selected.Bounds(), src, image.Pt(2, 1), draw.Src)
	for _, size := range []int{32, 64, 128, 256} {
		want := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.CatmullRom.Scale(want, want.Bounds(), selected, selected.Bounds(), draw.Over, nil)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, want); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(variants[size], encoded.Bytes()) {
			t.Fatalf("crop PNG changed (size %d)", size)
		}
	}
	store := attachments.Store{FilesDir: t.TempDir()}
	d := dbtest.Open(t)
	p := person(t, d, "avatar-pixels", "pixels@example.test")
	tenant := p.TenantID
	staged, err := stageAvatarData(t.Context(), store, tenant, data.Bytes(), crop)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, blob := range staged {
			_ = blob.Close()
		}
	}()
	hash := staged[0].SHA256
	hashes := map[string]string{}
	for i, size := range []int{32, 64, 128, 256} {
		hashes[strconv.Itoa(size)] = staged[i+1].SHA256
	}
	if _, err := store.Open(tenant, hash, "original"); !os.IsNotExist(err) {
		t.Fatalf("avatar became visible before publication: %v", err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenant, func(tx pgx.Tx) error {
		before, existed, err := read(t.Context(), tx, tenant, p.ID, true)
		if err != nil {
			return err
		}
		if err := attachments.Publish(t.Context(), tx, attachments.OwnerAvatar, staged...); err != nil {
			return err
		}
		after := before
		after.AvatarOriginalHash, after.AvatarHashes = hash, hashes
		return change(t.Context(), tx, p, before, existed, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(hash string, want []byte) {
		t.Helper()
		for _, variant := range []string{"original", "thumb", "preview"} {
			f, err := store.Open(tenant, hash, variant)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			f.Close()
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("stored %s PNG differs: %v", variant, err)
			}
		}
	}
	check(hash, original)
	for _, size := range []int{32, 64, 128, 256} {
		check(hashes[strconv.Itoa(size)], variants[size])
	}
}

func TestAttachmentGIFSmallerFrame(t *testing.T) {
	frame := image.NewPaletted(image.Rect(3, 2, 5, 4), color.Palette{color.Black, color.White})
	var data bytes.Buffer
	if err := gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{ColorModel: frame.Palette, Width: 10, Height: 10}}); err != nil {
		t.Fatal(err)
	}
	store := attachments.Store{FilesDir: t.TempDir()}
	got, err := store.Stage(t.Context(), "00000000-0000-0000-0000-000000000559", &data)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if got.Width != 10 || got.Height != 10 {
		t.Fatalf("GIF logical screen/first frame: %+v", got)
	}
}

func TestAvatarHeaderBoundsBeforeDecode(t *testing.T) {
	for _, dimensions := range [][2]uint32{{3000, 2000}, {4097, 1}, {1, 4097}} {
		data := pngHeader(dimensions[0], dimensions[1])
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		_, _, err := ProcessAvatar(data, Crop{Size: 1})
		if err == nil || !strings.Contains(err.Error(), "image dimensions exceed limit") {
			t.Fatalf("%v: want header rejection, got %v", dimensions, err)
		}
	}
}

func TestAvatarCropBeforeDecode(t *testing.T) {
	for _, crop := range []Crop{{X: -1, Size: 1}, {Size: 0}, {Size: 2049}, {X: int(^uint(0) >> 1), Size: 2}} {
		_, _, err := ProcessAvatar(pngHeader(2048, 2048), crop)
		if err == nil || !strings.Contains(err.Error(), "crop outside oriented image") {
			t.Fatalf("%+v: want crop rejection before decode, got %v", crop, err)
		}
		if !strings.Contains(err.Error(), "at most 2048 pixels per side") {
			t.Fatalf("crop rejection must explain the limit: %v", err)
		}
	}
}

func TestAttachmentHeaderBoundsBeforeDecode(t *testing.T) {
	const tenant = "00000000-0000-0000-0000-000000000559"
	store := attachments.Store{FilesDir: t.TempDir()}
	for _, dimensions := range [][2]uint32{{4097, 4096}, {8193, 1}, {1, 8193}} {
		_, err := store.Stage(t.Context(), tenant, bytes.NewReader(pngHeader(dimensions[0], dimensions[1])))
		if err == nil || !strings.Contains(err.Error(), "invalid or oversized image") {
			t.Fatalf("%v: want header rejection, got %v", dimensions, err)
		}
	}
	if err := filepath.WalkDir(store.FilesDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			t.Errorf("rejected upload left a file: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
