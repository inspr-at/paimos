// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand

import (
	"encoding/binary"
	"errors"
)

// A decoder sizes its buffers from the headers it trusts, and those can lie:
// golang.org/x/image/webp reports the VP8X canvas from DecodeConfig but
// decodes the frame at the frame's own size (a 74-byte file said 64×64 and
// decoded to 5000×64). So the size is read here from every header that
// carries one, before any decode, and a file whose headers disagree, that
// animates, or that has bytes a decoder would skip is refused.

var (
	errUnreadable = errors.New("the image cannot be read")
	errAnimated   = errors.New("animated logos are not supported")
)

// pngBounds walks the chunks of a PNG whose signature sniff has checked. The
// size is the one IHDR's; APNG frames are refused, and nothing may follow IEND.
func pngBounds(b []byte) (w, h int, err error) {
	b = b[8:]
	for first := true; ; first = false {
		if len(b) < 12 {
			return 0, 0, errUnreadable
		}
		n := binary.BigEndian.Uint32(b[:4])
		if uint64(n) > uint64(len(b)-12) {
			return 0, 0, errUnreadable
		}
		kind, data := string(b[4:8]), b[8:8+n]
		switch {
		case first != (kind == "IHDR"):
			return 0, 0, errUnreadable
		case kind == "IHDR":
			if n != 13 {
				return 0, 0, errUnreadable
			}
			w, h = int(binary.BigEndian.Uint32(data[0:4])), int(binary.BigEndian.Uint32(data[4:8]))
		case kind == "acTL", kind == "fcTL", kind == "fdAT":
			return 0, 0, errAnimated
		case kind == "IEND":
			if len(b) != 12+int(n) {
				return 0, 0, errUnreadable
			}
			return w, h, nil
		}
		b = b[12+n:]
	}
}

// webpBounds walks the chunks of a RIFF WEBP container whose header sniff has
// checked. It needs exactly one still frame; in the extended format that
// frame must be exactly the VP8X canvas.
func webpBounds(b []byte) (w, h int, err error) {
	if uint64(binary.LittleEndian.Uint32(b[4:8])) != uint64(len(b)-8) {
		return 0, 0, errUnreadable
	}
	b = b[12:]
	extended, frames := false, 0
	var canvasW, canvasH int
	for first := true; len(b) > 0; first = false {
		if len(b) < 8 {
			return 0, 0, errUnreadable
		}
		n := binary.LittleEndian.Uint32(b[4:8])
		if uint64(n)+uint64(n&1) > uint64(len(b)-8) {
			return 0, 0, errUnreadable
		}
		kind, data := string(b[:4]), b[8:8+n]
		switch kind {
		case "VP8X":
			const animation = 1 << 1
			if !first || n != 10 {
				return 0, 0, errUnreadable
			}
			if data[0]&animation != 0 {
				return 0, 0, errAnimated
			}
			extended = true
			canvasW = 1 + (int(data[4]) | int(data[5])<<8 | int(data[6])<<16)
			canvasH = 1 + (int(data[7]) | int(data[8])<<8 | int(data[9])<<16)
		case "ANIM", "ANMF":
			return 0, 0, errAnimated
		case "VP8 ", "VP8L":
			if frames++; frames > 1 {
				return 0, 0, errUnreadable
			}
			if kind == "VP8 " {
				w, h, err = vp8Size(data)
			} else {
				w, h, err = vp8lSize(data)
			}
			if err != nil {
				return 0, 0, err
			}
		default:
			// The simple format is one image chunk and nothing else.
			if !extended {
				return 0, 0, errUnreadable
			}
		}
		b = b[8+n+n&1:]
	}
	if frames != 1 || extended && (w != canvasW || h != canvasH) {
		return 0, 0, errUnreadable
	}
	return w, h, nil
}

// vp8Size reads a lossy key frame's header (RFC 6386, 9.1). The two scaling
// bits must be clear: a scaled frame is drawn at another size than it decodes.
func vp8Size(d []byte) (int, int, error) {
	if len(d) < 10 || d[0]&1 != 0 || d[3] != 0x9d || d[4] != 0x01 || d[5] != 0x2a {
		return 0, 0, errUnreadable
	}
	w, h := binary.LittleEndian.Uint16(d[6:8]), binary.LittleEndian.Uint16(d[8:10])
	if w>>14 != 0 || h>>14 != 0 {
		return 0, 0, errUnreadable
	}
	return int(w), int(h), nil
}

// vp8lSize reads a lossless header: a signature byte, then 14 bits of width-1,
// 14 of height-1, an alpha hint and a 3-bit version that must be 0.
func vp8lSize(d []byte) (int, int, error) {
	if len(d) < 5 || d[0] != 0x2f {
		return 0, 0, errUnreadable
	}
	bits := binary.LittleEndian.Uint32(d[1:5])
	if bits>>29 != 0 {
		return 0, 0, errUnreadable
	}
	return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, nil
}
