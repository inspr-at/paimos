// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const websocketMessageLimit = 1 << 20

// guardedWebSocket preserves the upstream wire frames but withholds each
// complete data message until its decoded payload has passed the credential
// check. Fragment boundaries and small transport reads cannot defeat scanning.
// Extensions and masked server frames are refused rather than copied unscanned.
type guardedWebSocket struct {
	io.ReadWriteCloser
	credential []byte
	ready      []byte
	frames     []byte
	payload    []byte
	fragmented bool
	frameCount int
}

func (g *guardedWebSocket) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(g.ready) == 0 {
		if err := g.readFrame(); err != nil {
			_ = g.Close()
			return 0, err
		}
	}
	n := copy(p, g.ready)
	g.ready = g.ready[n:]
	return n, nil
}

func (g *guardedWebSocket) readFrame() error {
	unsafe := func() error { return errors.New("unsafe WebSocket output") }
	header := make([]byte, 2, 10)
	if _, err := io.ReadFull(g.ReadWriteCloser, header); err != nil {
		return err
	}
	fin, opcode := header[0]&0x80 != 0, header[0]&0x0f
	if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
		return unsafe()
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		header = header[:4]
		if _, err := io.ReadFull(g.ReadWriteCloser, header[2:]); err != nil {
			return err
		}
		length = uint64(binary.BigEndian.Uint16(header[2:]))
		if length < 126 {
			return unsafe()
		}
	case 127:
		header = header[:10]
		if _, err := io.ReadFull(g.ReadWriteCloser, header[2:]); err != nil {
			return err
		}
		length = binary.BigEndian.Uint64(header[2:])
		if length < 65536 {
			return unsafe()
		}
	}
	control := opcode >= 8
	if length > websocketMessageLimit || control && (!fin || length > 125) {
		return unsafe()
	}
	switch opcode {
	case 0:
		if !g.fragmented {
			return unsafe()
		}
	case 1, 2:
		if g.fragmented {
			return unsafe()
		}
	case 8, 9, 10:
	default:
		return unsafe()
	}
	if !control && (len(g.payload)+int(length) > websocketMessageLimit || g.frameCount >= 1024) {
		return unsafe()
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(g.ReadWriteCloser, body); err != nil {
		return err
	}
	if control {
		if bytes.Contains(body, g.credential) {
			return unsafe()
		}
		g.ready = append(header, body...)
		return nil
	}
	g.frameCount++
	g.frames = append(g.frames, header...)
	g.frames = append(g.frames, body...)
	// Rescan only the previous credential-length suffix plus this fragment;
	// a long fragmented message must not cause quadratic scanning work.
	start := max(0, len(g.payload)-len(g.credential)+1)
	g.payload = append(g.payload, body...)
	if bytes.Contains(g.payload[start:], g.credential) {
		return unsafe()
	}
	g.fragmented = !fin
	if fin {
		g.ready, g.frames, g.payload = g.frames, nil, nil
		g.frameCount = 0
	}
	return nil
}
