// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/rules"
)

var ErrRulesDeliveryMismatch = errors.New("rules_delivery_hash_mismatch")

type rulesDeliveryAPI interface {
	PrepareRulesDelivery(context.Context, HarnessSession, rules.RulesDeliveryPrepare) (rules.RulesDeliveryManifest, error)
	AcknowledgeRulesDelivery(context.Context, HarnessSession, rules.RulesDeliveryAck) error
}

func rulesDeliveryHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (r *Remote) PrepareRulesDelivery(ctx context.Context, session HarnessSession, in rules.RulesDeliveryPrepare) (rules.RulesDeliveryManifest, error) {
	var out rules.RulesDeliveryManifest
	err := r.harnessWorker(ctx, session, "/rules-delivery-manifest", in, &out)
	return out, err
}
func (r *Remote) AcknowledgeRulesDelivery(ctx context.Context, session HarnessSession, in rules.RulesDeliveryAck) error {
	return r.harnessWorker(ctx, session, "/rules-delivery-evidence", in, nil)
}

func (s *Supervisor) prepareRulesDelivery(ctx context.Context, entry *owned, run Run, body string, repository repositoryRules) error {
	api, ok := s.api.(rulesDeliveryAPI)
	if !ok {
		return errors.New("rules delivery API unavailable")
	}
	manifest, err := api.PrepareRulesDelivery(ctx, entry.harness, rules.RulesDeliveryPrepare{RunID: run.ID, Generation: s.generation, BodySHA256: rulesDeliveryHash(body), Repository: repository})
	if err != nil {
		return err
	}
	if manifest.RunID != run.ID || manifest.SessionID != entry.harness.ID || manifest.Generation != s.generation || manifest.BodySHA256 != rulesDeliveryHash(body) || manifest.ByteSize != len(body) || manifest.RepositoryCommit != repository.Commit || !manifest.ExpiresAt.After(time.Now()) {
		return ErrRulesDeliveryMismatch
	}
	entry.rulesDelivery = &manifest
	return nil
}

// Called once after Start has confirmed readiness. The Process exposes only
// the bridge-computed hash, never the rule body or the daemon credential.
func (s *Supervisor) acknowledgeRulesDelivery(ctx context.Context, entry *owned, proc Process, body string) error {
	if body == "" {
		return nil
	}
	manifest := entry.rulesDelivery
	bridge, ok := proc.(interface{ RulesDeliveryHash() string })
	if manifest == nil || !ok || bridge.RulesDeliveryHash() != rulesDeliveryHash(body) || manifest.BodySHA256 != bridge.RulesDeliveryHash() {
		return errors.Join(ErrTelemetryProtocol, ErrRulesDeliveryMismatch)
	}
	api, ok := s.api.(rulesDeliveryAPI)
	if !ok {
		return errors.Join(ErrTelemetryProtocol, errors.New("rules delivery API unavailable"))
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := api.AcknowledgeRulesDelivery(ctx, entry.harness, rules.RulesDeliveryAck{ManifestID: manifest.ID, RunID: manifest.RunID, Generation: manifest.Generation, BodySHA256: manifest.BodySHA256, BridgeSHA256: bridge.RulesDeliveryHash(), AcknowledgedAt: time.Now().UTC()})
	if err != nil {
		return errors.Join(ErrTelemetryProtocol, err)
	}
	return nil
}
