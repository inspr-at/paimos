// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const grokProxyTarget = "cli-chat-proxy.grok.com:443"

type grokProxy struct {
	listener    net.Listener
	port        int
	violation   atomic.Bool
	once        sync.Once
	active      sync.Map
	connections atomic.Int32
}

func startGrokProxy() (*grokProxy, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("native Grok proxy unavailable")
	}
	proxy := &grokProxy{listener: listener, port: listener.Addr().(*net.TCPAddr).Port}
	go proxy.accept()
	return proxy, nil
}

func (p *grokProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		if p.connections.Add(1) > 16 {
			p.violation.Store(true)
			_ = client.Close()
			p.connections.Add(-1)
			continue
		}
		p.active.Store(client, struct{}{})
		go func() {
			defer p.connections.Add(-1)
			defer p.active.Delete(client)
			defer client.Close()
			p.handle(client)
		}()
	}
}

func (p *grokProxy) handle(client net.Conn) {
	_ = client.SetDeadline(time.Now().Add(180 * time.Second))
	reader := bufio.NewReaderSize(client, 4096)
	var head []byte
	for len(head) <= 4096 {
		part, err := reader.ReadBytes('\n')
		head = append(head, part...)
		if err != nil || len(head) > 4096 {
			p.violation.Store(true)
			return
		}
		if bytes.HasSuffix(head, []byte("\r\n\r\n")) {
			break
		}
	}
	if !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		p.violation.Store(true)
		return
	}
	lines := bytes.Split(head, []byte("\r\n"))
	if len(lines) < 3 || string(lines[0]) != "CONNECT "+grokProxyTarget+" HTTP/1.1" {
		p.violation.Store(true)
		_, _ = client.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	upstream, err := net.DialTimeout("tcp", grokProxyTarget, 5*time.Second)
	if err != nil {
		p.violation.Store(true)
		return
	}
	defer upstream.Close()
	p.active.Store(upstream, struct{}{})
	defer p.active.Delete(upstream)
	_ = upstream.SetDeadline(time.Now().Add(180 * time.Second))
	if _, err = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.violation.Store(true)
		return
	}
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_, copyErr := io.CopyN(upstream, reader, 10<<20+1)
		if copyErr == nil {
			p.violation.Store(true)
		}
		_ = upstream.Close()
	}()
	_, copyErr := io.CopyN(client, upstream, 10<<20+1)
	if copyErr == nil {
		p.violation.Store(true)
	}
	_ = client.Close()
	wait.Wait()
}

func (p *grokProxy) url() string { return "http://127.0.0.1:" + strconv.Itoa(p.port) }

func (p *grokProxy) stop() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		_ = p.listener.Close()
		p.active.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
	})
}
