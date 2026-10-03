// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Samples from cwebp 64×64: lossless (VP8L), lossy (VP8) and lossy with an
// alpha channel (VP8X, ALPH, VP8).
const (
	webpLossless = "5249464624000000574542505650384c170000002f3fc00f000750914674a6ff012021fc7faf45f43ff50300"
	webpLossy    = "524946464a00000057454250565038203e000000d003009d012a400040003ed168af522825a422a20801001a096900d39c061657593b1063d0320000fef0850e13761dd5b9a440de21a46be93b5fe3780000"
	webpAlpha    = "524946467400000057454250565038580a000000100000003f00003f0000414c504810000000010750c088080009e1ff7b2da2ffa91f565038203e000000d003009d012a400040003ed168af522825a422a20801001a096900d39c061657593b1063d0320000fef0850e13761dd5b9a440de21a46be93b5fe3780000"
	// The review's 74-byte file: a 64×64 VP8X canvas around a 5000×64 VP8L
	// frame. Exported for the API test.
	WebPFalseCanvas = "524946464200000057454250565038580a000000000000003f00003f00005650384c230000002f87d30f000750914674a6ff010045faff9f22fa9ffadffffef7bffffdef7ffffbff0800"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// riff wraps chunks in a RIFF WEBP container.
func riff(chunks ...[]byte) []byte {
	body := append([]byte("WEBP"), bytes.Join(chunks, nil)...)
	return append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)
}

func chunk(kind string, data []byte) []byte {
	b := append(binary.LittleEndian.AppendUint32([]byte(kind), uint32(len(data))), data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func vp8x(flags byte, w, h int) []byte {
	d := []byte{flags, 0, 0, 0, byte(w - 1), byte((w - 1) >> 8), byte((w - 1) >> 16), byte(h - 1), byte((h - 1) >> 8), byte((h - 1) >> 16)}
	return chunk("VP8X", d)
}

// withVP8LSize rewrites a lossless header's size; a solid-colour bitstream
// decodes at any size.
func withVP8LSize(frame []byte, w, h int) []byte {
	out := append([]byte{}, frame...)
	bits := binary.LittleEndian.Uint32(out[1:5])&^(1<<28-1) | uint32(w-1) | uint32(h-1)<<14
	binary.LittleEndian.PutUint32(out[1:5], bits)
	return out
}

// allocated is what fn allocates on the heap.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestWebPSizeComesFromEveryHeader(t *testing.T) {
	lossless := unhex(t, webpLossless)
	frame := lossless[20:] // the VP8L chunk's data
	for name, b := range map[string][]byte{
		"lossless":          lossless,
		"lossy":             unhex(t, webpLossy),
		"alpha":             unhex(t, webpAlpha),
		"extended lossless": riff(vp8x(0, 64, 64), chunk("VP8L", frame)),
		"extended exif":     riff(vp8x(1<<3, 64, 64), chunk("VP8L", frame), chunk("EXIF", []byte("abc"))),
	} {
		l, err := ValidateLogo("image/webp", b)
		if err != nil || l.Width != 64 || l.Height != 64 {
			t.Errorf("%s: %+v %v", name, l, err)
		}
	}

	// The review's file: DecodeConfig believes the canvas, Decode the frame.
	falseCanvas := unhex(t, WebPFalseCanvas)
	if len(falseCanvas) != 74 {
		t.Fatalf("sample is %d bytes", len(falseCanvas))
	}
	if cfg, err := webp.DecodeConfig(bytes.NewReader(falseCanvas)); err != nil || cfg.Width != 64 {
		t.Fatalf("sample no longer shows the decoder's gap: %+v %v", cfg, err)
	}
	if _, err := ValidateLogo("image/webp", falseCanvas); !errors.Is(err, errUnreadable) {
		t.Errorf("false canvas: %v", err)
	}

	lossy := unhex(t, webpLossy)
	scaled := append([]byte{}, lossy...)
	scaled[27] |= 0x40 // the width's scaling bits
	anmf := chunk("ANMF", append(make([]byte, 16), chunk("VP8L", frame)...))
	overrun := chunk("VP8L", frame)
	binary.LittleEndian.PutUint32(overrun[4:8], 1000)
	for name, tc := range map[string]struct {
		b    []byte
		want error
	}{
		"frame wider than canvas":   {riff(vp8x(0, 64, 64), chunk("VP8L", withVP8LSize(frame, 5000, 64))), errUnreadable},
		"frame smaller than canvas": {riff(vp8x(0, 64, 64), chunk("VP8L", withVP8LSize(frame, 32, 32))), errUnreadable},
		"canvas without a frame":    {riff(vp8x(0, 64, 64)), errUnreadable},
		"two frames":                {riff(vp8x(0, 64, 64), chunk("VP8L", frame), chunk("VP8L", frame)), errUnreadable},
		"VP8X not first":            {riff(chunk("VP8L", frame), vp8x(0, 64, 64)), errUnreadable},
		"chunk after simple frame":  {riff(chunk("VP8L", frame), chunk("EXIF", []byte("ab"))), errUnreadable},
		"animated flag":             {riff(vp8x(1<<1, 64, 64), chunk("ANIM", make([]byte, 6)), anmf), errAnimated},
		"frame chunk, no flag":      {riff(vp8x(0, 64, 64), anmf), errAnimated},
		"scaled lossy frame":        {scaled, errUnreadable},
		"lossless version 1":        {riff(chunk("VP8L", append([]byte{frame[0], frame[1], frame[2], frame[3], frame[4] | 0x20}, frame[5:]...))), errUnreadable},
		"bytes after the RIFF":      {append(append([]byte{}, lossless...), 0, 0), errUnreadable},
		"RIFF size too large":       {append(append([]byte{}, lossless[:4]...), append([]byte{0xff, 0, 0, 0}, lossless[8:]...)...), errUnreadable},
		"chunk overruns the RIFF":   {riff(overrun), errUnreadable},
	} {
		if _, err := ValidateLogo("image/webp", tc.b); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

func TestWebPBombIsRefusedBeforeDecoding(t *testing.T) {
	frame := unhex(t, webpLossless)[20:]
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		// A decoder would allocate 16384×16384×4 bytes (1 GiB) for these.
		"simple":   {riff(chunk("VP8L", withVP8LSize(frame, 16384, 16384))), "at most 4096 px"},
		"extended": {riff(vp8x(0, 16384, 16384), chunk("VP8L", withVP8LSize(frame, 16384, 16384))), "at most 4096 px"},
		// Inside the side limit, over the pixel limit (64 MiB).
		"4096 square": {riff(chunk("VP8L", withVP8LSize(frame, 4096, 4096))), "4 megapixels"},
	} {
		var err error
		if n := allocated(func() { _, err = ValidateLogo("image/webp", tc.b) }); n > 1<<20 {
			t.Errorf("%s: allocated %d bytes", name, n)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// pngChunk is one PNG chunk with its CRC.
func pngChunk(kind string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(append(b, kind...), data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// pngFile builds an 8-bit RGB PNG whose IHDR says w×h and whose IDAT
// inflates to raw.
func pngFile(t *testing.T, w, h int, raw []byte, extra ...[]byte) []byte {
	t.Helper()
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(w)), uint32(h))
	ihdr = append(ihdr, 8, 2, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	for _, c := range extra {
		b = append(b, c...)
	}
	b = append(b, pngChunk("IDAT", z.Bytes())...)
	return append(b, pngChunk("IEND", nil)...)
}

func TestPNGBombIsRefusedBeforeDecoding(t *testing.T) {
	rows := func(w, h int) []byte { return make([]byte, (1+3*w)*h) }
	if l, err := ValidateLogo("image/png", pngFile(t, 64, 64, rows(64, 64))); err != nil || l.Width != 64 {
		t.Fatalf("plain png: %+v %v", l, err)
	}
	for name, tc := range map[string]struct {
		b    []byte
		want string
	}{
		"huge IHDR":   {pngFile(t, 500000, 500000, rows(1, 1)), "at most 4096 px"},
		"4096 square": {pngFile(t, 4096, 4096, rows(1, 1)), "4 megapixels"},
		// 64×64 declared, 64 MiB of zeros inflated: the decoder stops at what
		// IHDR allows and refuses the rest.
		"inflates past IHDR": {pngFile(t, 64, 64, make([]byte, 64<<20)), "cannot be read"},
		"APNG":               {pngFile(t, 64, 64, rows(64, 64), pngChunk("acTL", make([]byte, 8))), "animated"},
	} {
		var err error
		if n := allocated(func() { _, err = ValidateLogo("image/png", tc.b) }); n > 4<<20 {
			t.Errorf("%s: allocated %d bytes", name, n)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	plain := pngFile(t, 64, 64, rows(64, 64))
	for name, b := range map[string][]byte{
		"data after IEND": append(append([]byte{}, plain...), "<svg onload=alert(1)>"...),
		"IHDR not first":  append(append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("tEXt", []byte("a\x00b"))...), plain[8:]...),
		"no IEND":         plain[:len(plain)-12],
		"chunk overruns":  plain[:len(plain)-13],
	} {
		if _, err := ValidateLogo("image/png", b); !errors.Is(err, errUnreadable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// bitWriter packs values least significant bit first, as VP8L reads them.
type bitWriter struct {
	b []byte
	n uint
}

func (w *bitWriter) put(v uint32, bits uint) {
	for i := range bits {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[len(w.b)-1] |= byte(v>>i&1) << (w.n % 8)
		w.n++
	}
}

// oneSymbol writes a simple prefix code with the single symbol s.
func (w *bitWriter) oneSymbol(s uint32) {
	w.put(1, 1) // simple code
	w.put(0, 1) // one symbol
	if s > 1 {
		w.put(1, 1)
		w.put(s, 8)
	} else {
		w.put(0, 1)
		w.put(s, 1)
	}
}

// vp8lHuffmanBomb is the review's shape: a w×h lossless frame, 96 bytes as a
// file, whose one-tile meta prefix image names Huffman group 65535. A decoder
// that sizes its group table from the largest index allocates 65536 groups
// (167 MiB on x/image v0.36.0) before it reads the first one.
func vp8lHuffmanBomb(w, h int) []byte {
	var bw bitWriter
	bw.put(0x2f, 8)
	bw.put(uint32(w-1), 14)
	bw.put(uint32(h-1), 14)
	bw.put(0, 4) // no alpha hint, version 0
	bw.put(0, 1) // no transform
	bw.put(0, 1) // no colour cache
	bw.put(1, 1) // a meta prefix image follows
	bw.put(7, 3) // 512-px tiles: one tile up to 512×512
	// The prefix image: one pixel, red 0xff and green 0xff, so group 0xffff.
	bw.put(0, 1) // no colour cache
	// Its codes: green, red, blue, alpha, distance.
	for _, s := range []uint32{0xff, 0xff, 0, 0, 0} {
		bw.oneSymbol(s)
	}
	frame := bw.b
	// Pad to the review's 96 bytes; the decoder reads no further than the
	// group table's first code.
	frame = append(frame, make([]byte, 96-20-len(frame))...)
	return riff(chunk("VP8L", frame))
}

func TestVP8LHuffmanBomb(t *testing.T) {
	for _, side := range []int{32, 512} {
		b := vp8lHuffmanBomb(side, side)
		if len(b) != 96 {
			t.Fatalf("%d: sample is %d bytes", side, len(b))
		}
		// The library itself must refuse it (x/image v0.45.0 and later).
		if _, err := webp.Decode(bytes.NewReader(b)); err == nil || !strings.Contains(err.Error(), "too many Huffman trees") {
			t.Errorf("%d: webp.Decode: %v", side, err)
		}
		var err error
		if n := allocated(func() { _, err = ValidateLogo("image/webp", b) }); n > 16<<20 {
			t.Errorf("%d: allocated %d bytes", side, n)
		}
		if !errors.Is(err, errUnreadable) {
			t.Errorf("%d: %v", side, err)
		}
	}
}

// An upload waits while another logo is being checked, and gives up with its
// request.
func TestLogoChecksTakeTurns(t *testing.T) {
	m := New(nil)
	m.decoding <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "PUT", "/api/settings/brand/logo/light", bytes.NewReader(unhex(t, webpLossless)))
	r.SetPathValue("variant", "light")
	r.Header.Set("Content-Type", "image/webp")
	if _, err := m.putLogo(r, nil, tenant.Principal{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("putLogo while another check runs: %v", err)
	}
}
