// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func testWebSocketFrame(opcode byte, fin bool, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	frame := []byte{first}
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) <= 65535:
		frame = append(frame, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		frame = append(frame, 127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	return append(frame, payload...)
}

type testWebSocketStream struct {
	*bytes.Reader
	closed bool
}

func (s *testWebSocketStream) Read(p []byte) (int, error) {
	// Even one-byte TCP reads must not release partial credential bytes.
	if len(p) > 1 {
		p = p[:1]
	}
	return s.Reader.Read(p)
}
func (*testWebSocketStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *testWebSocketStream) Close() error              { s.closed = true; return nil }

func TestWebSocketScanFragmentationControlAndLimits(t *testing.T) {
	credential := []byte(mockCredential)
	fragmented := append(testWebSocketFrame(1, false, credential[:10]), testWebSocketFrame(0, true, credential[10:])...)
	withPing := append(testWebSocketFrame(1, false, credential[:10]), testWebSocketFrame(9, true, []byte("ping"))...)
	withPing = append(withPing, testWebSocketFrame(0, true, credential[10:])...)
	masked := testWebSocketFrame(1, true, []byte("masked"))
	masked[1] |= 0x80
	compressed := testWebSocketFrame(1, true, []byte("compressed"))
	compressed[0] |= 0x40
	tooLarge := []byte{0x82, 127}
	tooLarge = binary.BigEndian.AppendUint64(tooLarge, websocketMessageLimit+1)
	tooMany := testWebSocketFrame(1, false, nil)
	for range 1024 {
		tooMany = append(tooMany, testWebSocketFrame(0, false, nil)...)
	}
	for name, wire := range map[string][]byte{
		"text":                 testWebSocketFrame(1, true, credential),
		"binary":               testWebSocketFrame(2, true, credential),
		"ping":                 testWebSocketFrame(9, true, credential),
		"close":                testWebSocketFrame(8, true, credential),
		"fragmented":           fragmented,
		"interleaved_ping":     withPing,
		"masked":               masked,
		"compressed":           compressed,
		"oversized":            tooLarge,
		"too_many_fragments":   tooMany,
		"invalid_continuation": testWebSocketFrame(0, true, []byte("unexpected")),
	} {
		t.Run(name, func(t *testing.T) {
			stream := &testWebSocketStream{Reader: bytes.NewReader(wire)}
			guard := &guardedWebSocket{ReadWriteCloser: stream, credential: credential}
			var output bytes.Buffer
			_, err := io.CopyBuffer(&output, guard, make([]byte, 1))
			if err == nil || !stream.closed || bytes.Contains(output.Bytes(), credential[:10]) {
				t.Fatal("unsafe output released or stream left open")
			}
		})
	}
	for _, wire := range [][]byte{
		testWebSocketFrame(1, true, []byte("safe")),
		testWebSocketFrame(2, true, bytes.Repeat([]byte{4}, 126)),
		testWebSocketFrame(2, true, bytes.Repeat([]byte{4}, 65536)),
		append(testWebSocketFrame(1, false, []byte("first")), testWebSocketFrame(0, true, []byte("second"))...),
	} {
		guard := &guardedWebSocket{ReadWriteCloser: &testWebSocketStream{Reader: bytes.NewReader(wire)}, credential: credential}
		out, err := io.ReadAll(guard)
		if err != nil || !bytes.Equal(out, wire) {
			t.Fatal("safe WebSocket wire changed")
		}
	}
}
