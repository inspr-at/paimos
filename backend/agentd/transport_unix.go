//go:build (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris) && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxTransportBody = maxPromptBytes + 4096

type Client struct {
	socket string
	http   *http.Client
}

func NewClient(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) || strings.ContainsAny(socket, "\x00\r\n") {
		return nil, errors.New("agentd socket path must be absolute")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}}
	return &Client{socket: socket, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}, nil
}

func Serve(ctx context.Context, socket string, supervisor *Supervisor) error {
	if supervisor == nil || !filepath.IsAbs(socket) {
		return errors.New("agentd server configuration is invalid")
	}
	dir := filepath.Dir(socket)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("agentd socket directory must be a private 0700 directory")
	}
	if existing, statErr := os.Lstat(socket); statErr == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return errors.New("agentd socket path already exists and is not a socket")
		}
		probe, dialErr := net.DialTimeout("unix", socket, 250*time.Millisecond)
		if dialErr == nil {
			probe.Close()
			return errors.New("agentd is already running for this socket")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on agentd socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(socket)
		return err
	}
	ownedSocket, err := os.Lstat(socket)
	if err != nil {
		listener.Close()
		return err
	}
	defer func() {
		listener.Close()
		current, statErr := os.Lstat(socket)
		if statErr == nil && os.SameFile(ownedSocket, current) {
			_ = os.Remove(socket)
		}
	}()
	server := &http.Server{Handler: transportHandler(supervisor), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if server.Shutdown(shutdownCtx) != nil {
				_ = server.Close()
			}
		case <-done:
		}
	}()
	err = server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func transportHandler(supervisor *Supervisor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeTransportJSON(w, http.StatusOK, supervisor.Status())
	})
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var request StartRequest
		if err := decodeTransportJSON(w, r, &request); err != nil {
			return
		}
		session, err := supervisor.Start(r.Context(), request)
		writeTransportResult(w, session, err)
	})
	mux.HandleFunc("POST /v1/sessions/{id}/{operation}", func(w http.ResponseWriter, r *http.Request) {
		var request ControlRequest
		if err := decodeTransportJSON(w, r, &request); err != nil {
			return
		}
		var receipt Receipt
		var err error
		switch r.PathValue("operation") {
		case "steer":
			receipt, err = supervisor.Steer(r.Context(), r.PathValue("id"), request)
		case "interrupt":
			receipt, err = supervisor.Interrupt(r.Context(), r.PathValue("id"), request)
		case "stop":
			receipt, err = supervisor.Stop(r.Context(), r.PathValue("id"), request)
		default:
			writeTransportJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeTransportResult(w, receipt, err)
	})
	return mux
}

func decodeTransportJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxTransportBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeTransportJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeTransportJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return errors.New("trailing request data")
	}
	return nil
}

func writeTransportResult(w http.ResponseWriter, value any, err error) {
	if err == nil {
		writeTransportJSON(w, http.StatusOK, value)
		return
	}
	status, code := http.StatusBadRequest, "invalid_request"
	switch {
	case errors.Is(err, ErrSessionNotFound):
		status, code = http.StatusNotFound, "session_not_found"
	case errors.Is(err, ErrSessionNotRunning):
		status, code = http.StatusConflict, "session_not_running"
	case errors.Is(err, ErrControlScopeMismatch):
		status, code = http.StatusForbidden, "scope_mismatch"
	case errors.Is(err, ErrControlReplayConflict):
		status, code = http.StatusConflict, "control_replay_conflict"
	case errors.Is(err, ErrControlReplayCapacity):
		status, code = http.StatusTooManyRequests, "control_replay_capacity"
	case errors.Is(err, ErrAdapterUnsupported), errors.Is(err, ErrCapabilityMissing):
		status, code = http.StatusUnprocessableEntity, "unsupported"
	}
	writeTransportJSON(w, status, map[string]string{"error": code})
}

func writeTransportJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (c *Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://agentd"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&problem)
		if problem.Error == "session_not_found" {
			return ErrSessionNotFound
		}
		if problem.Error == "session_not_running" {
			return ErrSessionNotRunning
		}
		if problem.Error == "scope_mismatch" {
			return ErrControlScopeMismatch
		}
		if problem.Error == "control_replay_conflict" {
			return ErrControlReplayConflict
		}
		if problem.Error == "control_replay_capacity" {
			return ErrControlReplayCapacity
		}
		if problem.Error == "unsupported" {
			return ErrCapabilityMissing
		}
		return fmt.Errorf("agentd request failed with HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxTransportBody+1)).Decode(output)
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.request(ctx, http.MethodGet, "/v1/status", nil, &out)
	return out, err
}
func (c *Client) Start(ctx context.Context, request StartRequest) (Session, error) {
	var out Session
	err := c.request(ctx, http.MethodPost, "/v1/sessions", request, &out)
	return out, err
}
func (c *Client) Steer(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, http.MethodPost, "/v1/sessions/"+id+"/steer", request, &out)
	return out, err
}
func (c *Client) Interrupt(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, http.MethodPost, "/v1/sessions/"+id+"/interrupt", request, &out)
	return out, err
}
func (c *Client) Stop(ctx context.Context, id string, request ControlRequest) (Receipt, error) {
	var out Receipt
	err := c.request(ctx, http.MethodPost, "/v1/sessions/"+id+"/stop", request, &out)
	return out, err
}
