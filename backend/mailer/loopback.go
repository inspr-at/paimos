// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package mailer

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"sync"
	"time"
)

type LoopbackMode int

const (
	LoopbackOK LoopbackMode = iota
	LoopbackHangAfterDATA
	LoopbackSlowAfterDATA
	LoopbackRejectMAIL
)

// Loopback is a native 127.0.0.1 SMTP protocol server for tests. It never
// forwards mail off-box.
type Loopback struct {
	Addr      string
	Mode      LoopbackMode
	Slow      time.Duration
	mu        sync.Mutex
	messages  [][]byte
	dataCount int
	ln        net.Listener
	closed    chan struct{}
}

func StartLoopback(mode LoopbackMode, slow time.Duration) (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &Loopback{Addr: ln.Addr().String(), Mode: mode, Slow: slow, ln: ln, closed: make(chan struct{})}
	go l.serve()
	return l, nil
}

func (l *Loopback) Close() {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	_ = l.ln.Close()
}

func (l *Loopback) Messages() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]byte, len(l.messages))
	for i, m := range l.messages {
		out[i] = append([]byte(nil), m...)
	}
	return out
}

func (l *Loopback) DATACount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dataCount
}

func (l *Loopback) serve() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go l.handle(conn)
	}
}

func (l *Loopback) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	write("220 paimos-loopback SMTP")
	var data bytes.Buffer
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if inData && data.Len() > 0 {
				l.record(data.Bytes())
			}
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				l.record(data.Bytes())
				inData = false
				data.Reset()
				switch l.Mode {
				case LoopbackHangAfterDATA:
					<-l.closed
					return
				case LoopbackSlowAfterDATA:
					if l.Slow > 0 {
						timer := time.NewTimer(l.Slow)
						select {
						case <-timer.C:
						case <-l.closed:
							timer.Stop()
							return
						}
					}
				}
				write("250 OK")
				continue
			}
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			data.WriteString(line)
			continue
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			write("250-localhost")
			write("250 OK")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if l.Mode == LoopbackRejectMAIL {
				write("550 rejected")
				continue
			}
			write("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			write("250 OK")
		case cmd == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			inData = true
			data.Reset()
		case cmd == "QUIT":
			write("221 bye")
			return
		case cmd == "RSET", cmd == "NOOP":
			write("250 OK")
		default:
			write("250 OK")
		}
	}
}

func (l *Loopback) record(raw []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dataCount++
	l.messages = append(l.messages, append([]byte(nil), raw...))
}

func SplitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "25"
	}
	return h, p
}
