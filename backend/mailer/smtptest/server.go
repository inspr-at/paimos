// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package smtptest is a native 127.0.0.1 SMTP fixture for tests. The
// production paimos server does not import this package; it never
// listens unless a test calls Start. EHLO does not advertise 8BITMIME.
package smtptest

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"sync"
	"time"
)

type Mode int

const (
	OK Mode = iota
	HangAfterDATA
	SlowAfterDATA
	RejectMAIL
)

// Server is a loopback SMTP protocol fixture. It never forwards mail off-box.
type Server struct {
	Addr      string
	Mode      Mode
	Slow      time.Duration
	mu        sync.Mutex
	messages  [][]byte
	dataCount int
	ln        net.Listener
	closed    chan struct{}
}

func Start(mode Mode, slow time.Duration) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{Addr: ln.Addr().String(), Mode: mode, Slow: slow, ln: ln, closed: make(chan struct{})}
	go s.serve()
	return s, nil
}

func (s *Server) Close() {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	_ = s.ln.Close()
}

func (s *Server) Messages() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.messages))
	for i, m := range s.messages {
		out[i] = append([]byte(nil), m...)
	}
	return out
}

func (s *Server) DATACount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataCount
}

func SplitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "25"
	}
	return h, p
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
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
				s.record(data.Bytes())
			}
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				s.record(data.Bytes())
				inData = false
				data.Reset()
				switch s.Mode {
				case HangAfterDATA:
					<-s.closed
					return
				case SlowAfterDATA:
					if s.Slow > 0 {
						timer := time.NewTimer(s.Slow)
						select {
						case <-timer.C:
						case <-s.closed:
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
			if s.Mode == RejectMAIL {
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

func (s *Server) record(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dataCount++
	s.messages = append(s.messages, append([]byte(nil), raw...))
}
