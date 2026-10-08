// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hooknote"
)

// LocalServer exposes fenced local control through an owner-only Unix socket.
// Socket mode and a random bearer token are both required.
type LocalServer struct {
	Server    *http.Server
	Listener  net.Listener
	Socket    string
	TokenFile string
	cleanup   func() error
	release   func()
	closeOnce sync.Once
	closeErr  error
	hooks     *hooknote.Server
}

func ServeLocal(s *Supervisor, socket string, attachments ...*AttachManager) (_ *LocalServer, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("start local socket %s: %w", socket, resultErr)
		}
	}()
	if s == nil || !filepath.IsAbs(socket) {
		return nil, errors.New("invalid local socket")
	}
	dir, err := agentsetup.OpenStore(filepath.Dir(socket), false)
	if err != nil {
		return nil, err
	}
	lock, err := dir.LockSocket(filepath.Base(socket))
	if err != nil {
		dir.Close()
		return nil, err
	}
	complete := false
	var info, tokenInfo os.FileInfo
	var listener net.Listener
	cleanup := func() error {
		return dir.CleanupSocket(filepath.Base(socket), lock, info, tokenInfo)
	}
	defer func() {
		if !complete {
			if listener != nil {
				_ = listener.Close()
			}
			_ = cleanup()
			lock.Close()
			dir.Close()
		}
	}()
	if _, err := os.Lstat(socket); err == nil {
		return nil, errors.New("local socket already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	token, err := randomID()
	if err != nil {
		return nil, err
	}
	tokenFile := socket + ".token"
	f, err := os.OpenFile(tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create socket token %s: %w", tokenFile, err)
	}
	tokenInfo, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat socket token %s: %w", tokenFile, err)
	}
	if _, err = f.WriteString(token); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write socket token %s: %w", tokenFile, err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sync socket token %s: %w", tokenFile, err)
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("close socket token %s: %w", tokenFile, err)
	}
	listener, err = net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err = os.Chmod(socket, 0600); err != nil {
		return nil, err
	}
	info, err = os.Lstat(socket)
	if err != nil {
		return nil, err
	}
	hooks := hooknote.NewServer()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/inbox-hook", hooks)
	if s.stepUps != nil {
		mux.HandleFunc("GET /v1/step-up", func(w http.ResponseWriter, r *http.Request) { s.stepUps.serve(w, r, token) })
		mux.HandleFunc("POST /v1/step-up", func(w http.ResponseWriter, r *http.Request) { s.stepUps.serve(w, r, token) })
	}
	mux.HandleFunc("POST /v1/account-link", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var req AccountLinkRequest
		if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid account link", 400)
			return
		}
		out, err := s.AccountLink(r.Context(), req)
		if err != nil {
			http.Error(w, "account linking unavailable", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /v1/statusline", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var req StatuslineRequest
		if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF {
			http.Error(w, "invalid statusline", 400)
			return
		}
		out, err := s.ReportStatusline(r.Context(), req, time.Now().UTC())
		if err != nil {
			http.Error(w, "statusline unavailable", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	if len(attachments) == 1 && attachments[0] != nil {

		s.recoveryMu.Lock()
		s.attachedHookVerifier = attachments[0]
		s.recoveryMu.Unlock()
		mux.HandleFunc("POST /v1/attached-hook", func(w http.ResponseWriter, r *http.Request) {
			if !authorized(r, token) {
				http.Error(w, "unauthorized", 403)
				return
			}
			// Binding reporting is not watch consent. Require the unchanged
			// kernel peer; no browser, TTY or user-supplied helper PID grants it.
			peer, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
			if !ok {
				http.Error(w, "kernel peer required", 403)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			var in AttachedHookRequest
			if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
				http.Error(w, "invalid hook binding", 400)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()

			if in.Operation != "" {
				out, err := s.serviceAttachedHook(ctx, peer, in)
				if err != nil {
					code := 409
					if errors.Is(err, ErrNotOwned) {
						code = 404
					}
					http.Error(w, "attached hook unavailable", code)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				_ = json.NewEncoder(w).Encode(out)
				return
			}
			if s.bindAttachedHook(ctx, peer, in) != nil {
				http.Error(w, "attached hook binding refused", 409)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("{}"))
		})
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
		if r.URL.Query().Get("include_readiness") != "1" {
			status.AccountStatuses, status.VerificationReasons = nil, nil
		}
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
		status.AccountStatuses, status.VerificationReasons = nil, nil
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
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ConnContext: localConnContext}
	local := &LocalServer{Server: server, Listener: listener, Socket: socket, TokenFile: tokenFile, cleanup: cleanup, hooks: hooks}
	local.release = func() {
		_ = lock.Close()
		_ = dir.Close()
	}
	go func() { _ = server.Serve(listener) }()
	complete = true
	return local, nil
}

func localConnContext(ctx context.Context, c net.Conn) context.Context {
	return hooknote.Annotate(attachConnContext(ctx, c), c)
}

// SetHookPeer installs the attached-note source. The socket token is not
// consulted for this route. A nil source stays unavailable and releases nothing.
func (l *LocalServer) SetHookPeer(src hooknote.NoteSource, grants *hooknote.Registry) {
	if l == nil || l.hooks == nil {
		return
	}
	l.hooks.Set(src, grants)
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
	l.closeOnce.Do(func() {
		if l.release != nil {
			defer l.release()
		}
		l.closeErr = l.Server.Close()
		// Serve runs in a goroutine and may not have registered the listener
		// with http.Server yet when startup is immediately followed by Close.
		_ = l.Listener.Close()
		if l.cleanup != nil {
			l.closeErr = errors.Join(l.closeErr, l.cleanup())
		}
	})
	return l.closeErr
}
