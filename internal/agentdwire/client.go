// SPDX-License-Identifier: AGPL-3.0-only

// Package agentdwire is the authenticated local client for AEON agentd.
package agentdwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

type Client struct {
	Socket    string
	TokenFile string
}

func OpenClient(state string) (Client, error) {
	s, err := agentsetup.OpenStore(state, false)
	if err != nil {
		return Client{}, err
	}
	defer s.Close()
	raw, err := s.Read("control.json", 4096)
	if err != nil {
		return Client{}, err
	}
	var ref agentsetup.ControlReference
	if json.Unmarshal(raw, &ref) != nil {
		return Client{}, errors.New("local daemon reference unavailable")
	}
	socket, err := agentsetup.ResolveSocketPath(state, &ref)
	if err != nil {
		return Client{}, err
	}
	if err := agentsetup.CheckSocket(socket); err != nil {
		return Client{}, err
	}
	return Client{Socket: socket, TokenFile: socket + ".token"}, nil
}

func (c Client) token() (string, error) {
	if !filepath.IsAbs(c.Socket) || !filepath.IsAbs(c.TokenFile) {
		return "", errors.New("local agentd path must be absolute")
	}
	store, err := agentsetup.OpenStore(filepath.Dir(c.TokenFile), false)
	if err != nil {
		return "", errors.New("agentd token file unavailable")
	}
	defer store.Close()
	raw, err := store.Read(filepath.Base(c.TokenFile), 32)
	if err != nil || len(raw) != 32 {
		return "", errors.New("agentd token file invalid")
	}
	return string(raw), nil
}

func (c Client) Lifecycle(ctx context.Context, accountID string) (agentd.LifecycleStatus, error) {
	var out agentd.LifecycleStatus
	err := c.lifecycleRequest(ctx, "GET", "/v1/lifecycle?account_id="+url.QueryEscape(accountID), nil, &out)
	return out, err
}

// CapacityAccounts explicitly opts into the additional lifecycle projection.
// Ordinary lifecycle consumers retain their strict response contract.
func (c Client) CapacityAccounts(ctx context.Context, accountID string) ([]agentd.CapacityAccountStatus, error) {
	var out agentd.LifecycleStatus
	if err := c.lifecycleRequest(ctx, "GET", "/v1/lifecycle?include_capacity=1&account_id="+url.QueryEscape(accountID), nil, &out); err != nil {
		return nil, err
	}
	if out.CapacityAccounts == nil {
		return nil, errors.New("capacity inventory not available from this daemon")
	}
	return out.CapacityAccounts, nil
}

func (c Client) Drain(ctx context.Context, req agentd.DrainRequest) (agentd.LifecycleStatus, error) {
	var out agentd.LifecycleStatus
	err := c.lifecycleRequest(ctx, "POST", "/v1/drain", req, &out)
	if err == nil && out.DaemonID != req.DaemonID {
		return agentd.LifecycleStatus{}, errors.New("local daemon ownership mismatch")
	}
	return out, err
}

func (c Client) lifecycleRequest(ctx context.Context, method, path string, body, dest any) error {
	token, err := c.token()
	if err != nil {
		return err
	}
	var raw []byte
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid local request")
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	hc := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(ctx, method, "http://agentd"+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid local request")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(r)
	if err != nil {
		return errors.New("local agentd unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("local lifecycle request rejected")
	}
	d := json.NewDecoder(io.LimitReader(res.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(dest) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid local lifecycle response")
	}
	return nil
}

func (c Client) Control(ctx context.Context, req agentd.ControlRequest) (agentd.Receipt, error) {
	token, err := c.token()
	if err != nil {
		return agentd.Receipt{}, err
	}
	if req.RunID == "" {
		return agentd.Receipt{}, errors.New("missing run ID")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return agentd.Receipt{}, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	r, err := http.NewRequestWithContext(ctx, "POST", "http://agentd/v1/runs/"+req.RunID+"/control", bytes.NewReader(body))
	if err != nil {
		return agentd.Receipt{}, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(r)
	if err != nil {
		return agentd.Receipt{}, errors.New("local agentd unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return agentd.Receipt{}, errors.New("local agentd rejected control")
	}
	var receipt agentd.Receipt
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || receipt.RunID != req.RunID || receipt.Generation != req.Generation || receipt.CorrelationID != req.CorrelationID {
		return agentd.Receipt{}, errors.New("local agentd returned an invalid receipt")
	}
	return receipt, nil
}

// Statusline sends only normalized quota observations over the private socket.
func (c Client) Statusline(ctx context.Context, req agentd.StatuslineRequest) (agentd.StatuslineResponse, error) {
	var out agentd.StatuslineResponse
	err := c.lifecycleRequest(ctx, "POST", "/v1/statusline", req, &out)
	return out, err
}
