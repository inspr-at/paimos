// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsecurity"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/stepup"
)

func pairedStepUp(root string, c agentsetup.RuntimeConfig, remote *agentd.Remote) (*agentd.StepUpManager, error) {
	_, proof, err := agentsetup.ReadAttachProof(root, c)
	if err != nil {
		return nil, fmt.Errorf("paired Touch ID proof in %s: %w", root, err)
	}
	if remote == nil || remote.Client == nil || remote.Client.HTTP == nil {
		return nil, errors.New("paired Touch ID transport unavailable")
	}
	// Freeze both credentials and origin at startup; credentials and the key ID
	// remain in the signed daemon, never in local responses or child environments.
	hc := *remote.Client.HTTP
	hc.Timeout = 15 * time.Second
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	fetch := stepUpFetcher(&hc, c.Origin, c.ComputerID, remote.Client.Token, string(proof))
	return agentd.NewStepUpManager(agentd.StepUpConfig{Origin: c.Origin, ComputerID: c.ComputerID, Ready: c.LocalAuthKeyID != "",
		Fetch: fetch,
		Sign: func(ctx context.Context, hash []byte, reason string) (string, error) {
			if c.LocalAuthKeyID == "" {
				return "", agentsecurity.ErrUnavailable
			}
			return agentsecurity.DefaultSigner().Sign(ctx, c.LocalAuthKeyID, hash, reason)
		},
	})
}

func stepUpFetcher(hc *http.Client, origin, computer, bearer, proof string) func(context.Context, string) (stepup.Challenge, error) {
	return func(ctx context.Context, id string) (stepup.Challenge, error) {
		var out stepup.Challenge
		fail := errors.New("paired Touch ID challenge unavailable")
		if !stepup.ValidID(id) {
			return out, fail
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/agentd/step-ups/"+id, nil)
		if err != nil {
			return out, fail
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Aeon-Computer-ID", computer)
		req.Header.Set("Aeon-Device-Proof", proof)
		req.Header.Set("Accept", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			return out, fail
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return out, fail
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 4097))
		if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &out) != nil {
			return stepup.Challenge{}, fail
		}
		return out, nil
	}
}
