// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// LocalServer exposes fenced local control through an owner-only Unix socket.
// Socket mode and a random bearer token are both required.
type LocalServer struct {
	Server    *http.Server
	Listener  net.Listener
	Socket    string
	TokenFile string
	info      os.FileInfo
	tokenInfo os.FileInfo
}

func ServeLocal(s *Supervisor, socket string, attachments ...*AttachManager) (*LocalServer, error) {
	if s == nil || !filepath.IsAbs(socket) {
		return nil, errors.New("invalid local socket")
	}
	if _, err := os.Lstat(socket); err == nil {
		return nil, errors.New("local socket already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if info, err := os.Stat(filepath.Dir(socket)); err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("local socket directory must be private")
	}
	token, err := randomID()
	if err != nil {
		return nil, err
	}
	tokenFile := socket + ".token"
	f, err := os.OpenFile(tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(tokenFile)
		}
	}()
	if _, err = f.WriteString(token); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	tokenInfo, err := os.Lstat(tokenFile)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	info, err := os.Lstat(socket)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	if len(attachments) == 1 && attachments[0] != nil {
		mux.HandleFunc("POST /v1/attach", func(w http.ResponseWriter, r *http.Request) { attachments[0].serve(w, r, token) })
	}
	mux.HandleFunc("GET /v1/account-environment", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		value, err := s.AccountEnvironment(q.Get("account_id"), q.Get("daemon_id"), q.Get("harness"))
		if err != nil {
			http.Error(w, "local account unavailable", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	})
	mux.HandleFunc("GET /v1/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status := s.Lifecycle(r.URL.Query().Get("account_id"))
		if r.URL.Query().Get("include_capacity") == "1" {
			status.CapacityAccounts = s.CapacityAccounts(r.URL.Query().Get("account_id"))
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("POST /v1/drain", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var req DrainRequest
		if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid drain", http.StatusBadRequest)
			return
		}
		status, err := s.Drain(req)
		if err != nil {
			http.Error(w, "drain rejected", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"daemon_id": s.DaemonID(), "generation": s.Generation(), "runs": s.Status()})
	})
	mux.HandleFunc("POST /v1/runs/{runID}/control", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 70<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var req ControlRequest
		if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF || req.RunID != r.PathValue("runID") {
			http.Error(w, "invalid control", http.StatusBadRequest)
			return
		}
		receipt, err := s.Control(r.Context(), req)
		if err != nil {
			http.Error(w, "control rejected", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ConnContext: attachConnContext}
	local := &LocalServer{Server: server, Listener: listener, Socket: socket, TokenFile: tokenFile, info: info, tokenInfo: tokenInfo}
	go func() { _ = server.Serve(listener) }()
	complete = true
	return local, nil
}

func authorized(r *http.Request, token string) bool {
	provided := r.Header.Get("Authorization")
	if len(provided) != len(token)+7 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte("Bearer "+token)) == 1
}

func (l *LocalServer) Close() error {
	if l == nil {
		return nil
	}
	err := l.Server.Close()
	if info, e := os.Lstat(l.Socket); e == nil && os.SameFile(info, l.info) {
		_ = os.Remove(l.Socket)
	}
	if info, e := os.Lstat(l.TokenFile); e == nil && os.SameFile(info, l.tokenInfo) {
		_ = os.Remove(l.TokenFile)
	}
	return err
}
