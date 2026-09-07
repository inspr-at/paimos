// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Reader splits records on LF only. Unicode line separators U+2028 and U+2029
// inside JSON strings are preserved; optional trailing CR before LF is stripped.
type Reader struct {
	r   io.Reader
	buf []byte
	tmp [4096]byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

func (r *Reader) ReadLine() ([]byte, error) {
	for {
		if idx := bytes.IndexByte(r.buf, '\n'); idx >= 0 {
			line := r.buf[:idx]
			r.buf = r.buf[idx+1:]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) > MaxFrameBytes {
				return nil, ErrFrameTooLarge
			}
			return line, nil
		}
		n, err := r.r.Read(r.tmp[:])
		if n > 0 {
			r.buf = append(r.buf, r.tmp[:n]...)
			if len(r.buf) > MaxFrameBytes+1 {
				return nil, ErrFrameTooLarge
			}
			continue
		}
		if err == io.EOF {
			if len(r.buf) == 0 {
				return nil, io.EOF
			}
			line := r.buf
			r.buf = nil
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) > MaxFrameBytes {
				return nil, ErrFrameTooLarge
			}
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// WriteLine marshals value as one JSON object followed by LF.
func WriteLine(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode pi rpc frame")
	}
	if len(body) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	body = append(body, '\n')
	_, err = w.Write(body)
	return err
}
