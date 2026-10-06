// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hooknote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Session is one credential-free exchange with agentd. It never reads a token,
// config file or lease, and it never sends an Authorization header. The daemon
// pin is checked before the first request byte, so a pathname-only fake socket
// cannot read a note request.
type Session struct {
	conn net.Conn
	r    *bufio.Reader
	peer Process
}

func Dial(ctx context.Context, socket string, pin DaemonPin) (*Session, error) {
	return dialWithTrust(ctx, socket, pin, authenticateDaemon)
}

func dialWithTrust(ctx context.Context, socket string, pin DaemonPin, trust func(context.Context, Process) error) (*Session, error) {
	if !Enabled() || !pin.Valid() || socket == "" || socket[0] != '/' {
		return nil, ErrUnavailable
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, ErrPeer
	}
	peer, err := Snapshot(conn)
	if err != nil || !MatchesPin(peer, pin) || trust == nil || trust(ctx, peer) != nil || Recheck(conn, peer) != nil {
		conn.Close()
		return nil, ErrPeer
	}
	return &Session{conn: conn, r: bufio.NewReader(conn), peer: peer}, nil
}

func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Session) Offer(ctx context.Context, claim Claim) (Note, string, error) {
	if s == nil {
		return Note{}, "", ErrPeer
	}
	var fresh [32]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return Note{}, "", ErrPeer
	}
	challenge := hex.EncodeToString(fresh[:])
	var begun wireBegin
	status, err := s.post(ctx, wireRequest{Op: "begin", Challenge: challenge, Event: claim.Event, VendorRef: claim.VendorRef, AssertedSession: claim.AssertedSession, EnvSession: claim.EnvSession, Subagent: claim.Subagent}, &begun)
	if err != nil || status != 200 || begun.Challenge != challenge || !ValidID(begun.Generation) || begun.Epoch.Generation != begun.Generation || begun.Epoch.Counter == 0 {
		return Note{}, "", ErrPeer
	}
	var out wireOffer
	status, err = s.post(ctx, wireRequest{Op: "offer", Challenge: challenge, Generation: begun.Generation, Epoch: begun.Epoch}, &out)
	if err != nil {
		return Note{}, "", err
	}
	if status == 204 {
		return Note{}, "", ErrEmpty
	}
	if status == 404 {
		return Note{}, "", ErrUnavailable
	}
	if status != 200 {
		return Note{}, "", ErrPeer
	}
	if !ValidNonce(out.Nonce) {
		return Note{}, "", ErrMalformedNonce
	}
	if Expired(out.Note, time.Now()) {
		// The body stays here. The nonce lets the caller settle uncertain.
		return Note{}, out.Nonce, ErrExpired
	}
	if out.Note.Origin == OriginAgent {
		out.Note.Body = ""
		out.Note.Owner = ""
	}
	return out.Note, out.Nonce, nil
}

func (s *Session) Settle(ctx context.Context, nonce, outcome string) error {
	if s == nil {
		return ErrPeer
	}
	if !ValidNonce(nonce) || !ValidOutcome(outcome) {
		return ErrMalformedNonce
	}
	status, err := s.post(ctx, wireRequest{Op: "settle", Nonce: nonce, Outcome: outcome}, nil)
	if err != nil {
		return err
	}
	if status == 400 {
		return ErrMalformedNonce
	}
	if status != 204 && status != 200 {
		return ErrPeer
	}
	return nil
}

func (s *Session) post(ctx context.Context, payload any, dest any) (int, error) {
	if err := Recheck(s.conn, s.peer); err != nil {
		return 0, ErrPeer
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	{
		_ = s.conn.SetDeadline(deadline)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, ErrPeer
	}
	var req bytes.Buffer
	req.WriteString("POST /v1/inbox-hook HTTP/1.1\r\nHost: agentd\r\nContent-Type: application/json\r\nContent-Length: ")
	req.WriteString(strconv.Itoa(len(body)))
	req.WriteString("\r\nConnection: keep-alive\r\n\r\n")
	req.Write(body)
	if n, writeErr := s.conn.Write(req.Bytes()); writeErr != nil || n != req.Len() {
		return 0, ErrPeer
	}
	status, raw, err := readHTTP(s.r)
	if Recheck(s.conn, s.peer) != nil {
		return 0, ErrPeer
	}
	if err != nil {
		return 0, ErrPeer
	}
	if dest != nil && len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(dest) != nil || dec.Decode(&struct{}{}) != io.EOF {
			return status, ErrPeer
		}
	}
	return status, nil
}

func readHTTP(r *bufio.Reader) (int, []byte, error) {
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodPost})
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	const maxBody = 16 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, err
	}
	if len(body) > maxBody {
		return 0, nil, ErrLimit
	}
	return resp.StatusCode, body, nil
}
