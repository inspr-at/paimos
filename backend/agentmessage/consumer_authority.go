// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package agentmessage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
)

const ConsumerLeaseHeader = "X-Paimos-Consumer-Lease"
const ConsumerAttemptHeader = "X-Paimos-Consumer-Attempt"

var (
	ErrConsumerInvalid     = errors.New("consumer_invalid")
	ErrConsumerUnavailable = errors.New("consumer_unavailable")
	ErrConsumerConflict    = errors.New("consumer_conflict")
	ErrConsumerHandoff     = errors.New("consumer_handoff_required")
	ErrConsumerUnknown     = errors.New("consumer_outcome_unknown")
	ErrConsumerStorage     = errors.New("consumer_storage_unavailable")
)

type ConsumerCredentials struct {
	Principal                                 auth.Principal
	RuntimeLease, ConsumerLease, AttemptNonce string
}
type ConsumerRegistration struct {
	RuntimeID         string `json:"runtime_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	SessionID         string `json:"session_id"`
	SessionGeneration string `json:"session_generation"`
	Generation        string `json:"consumer_generation"`
	Kind              string `json:"kind"`
	TargetID          string `json:"target_id"`
	TargetVersion     int64  `json:"target_version"`
}
type ConsumerStream struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	Generation    string `json:"generation"`
	Kind          string `json:"kind"`
	ExpiresAt     string `json:"expires_at"`
}
type consumerOwner struct {
	Registration                      ConsumerRegistration `json:"registration"`
	ProjectID, AgentID, UserID, KeyID int64
	Address                           string
}
type ownedStream struct {
	ConsumerStream
	consumerOwner
	digest []byte
}

func consumerStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func consumerExpired(v string) bool {
	t, e := time.Parse(time.RFC3339Nano, v)
	return e != nil || !t.After(time.Now())
}
func consumerJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func consumerProof(v string) bool {
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == v
}
func consumerDigest(v string) []byte {
	d := sha256.Sum256([]byte("paimos-consumer-v1\x00" + v))
	return d[:]
}
func consumerID(v string) bool {
	return len(v) == 36 && uuid.Validate(v) == nil && strings.ToLower(v) == v
}
func consumerPrincipal(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64) error {
	u, current, e := auth.ReauthorizePrincipalTx(ctx, tx, p, time.Now())
	if e != nil || !auth.IsSuperAdmin(u) || current.Kind() != auth.PrincipalAPIKey || current.Impersonated() || !current.HasScope(auth.ScopeAgentControlsRunner) {
		return ErrConsumerUnavailable
	}
	var yes int
	if tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=? AND status='active'`, project).Scan(&yes) != nil {
		return ErrConsumerUnavailable
	}
	return nil
}

// Same exact M176 reporter credential and lease domain. Public UUIDs do not
// authenticate an owner. Historical completion replays still reauthorize the
// credential, but cannot mutate any current cursor or replacement generation.
func consumerRuntime(ctx context.Context, tx *sql.Tx, c ConsumerCredentials, project int64, runtimeID, generation string, live bool) (string, string, error) {
	if e := consumerPrincipal(ctx, tx, c.Principal, project); e != nil {
		return "", "", e
	}
	var user, key int64
	var host, gen, deadline, raw string
	var digest []byte
	e := tx.QueryRowContext(ctx, `SELECT user_id,api_key_id,machine_id,generation,expires_at,lease_digest,registration_json FROM lifecycle_runtimes WHERE id=? AND project_id=?`, runtimeID, project).Scan(&user, &key, &host, &gen, &deadline, &digest, &raw)
	d := sha256.Sum256([]byte("paimos-lifecycle-runtime-v1\x00" + generation + "\x00" + c.RuntimeLease))
	if e != nil || user != c.Principal.UserID() || key != c.Principal.APIKeyID() || gen != generation || !consumerProof(c.RuntimeLease) || subtle.ConstantTimeCompare(digest, d[:]) != 1 || (live && consumerExpired(deadline)) {
		return "", "", ErrConsumerUnavailable
	}
	var registration struct {
		AccountLabel string `json:"account_label"`
	}
	if json.Unmarshal([]byte(raw), &registration) != nil {
		return "", "", ErrConsumerStorage
	}
	return host, registration.AccountLabel, nil
}
func consumerBinding(ctx context.Context, tx *sql.Tx, c ConsumerCredentials, project int64, r ConsumerRegistration, live bool) (consumerOwner, error) {
	host, account, e := consumerRuntime(ctx, tx, c, project, r.RuntimeID, r.RuntimeGeneration, live)
	if e != nil {
		return consumerOwner{}, e
	}
	var agentID int64
	var name, harness, phase string
	e = tx.QueryRowContext(ctx, `SELECT s.project_agent_id,s.agent_name,s.harness,s.phase FROM harness_sessions s JOIN lifecycle_runtime_sessions own ON own.session_id=s.id WHERE s.id=? AND s.project_id=? AND own.runtime_id=? AND own.generation=? AND s.management_mode='managed' AND s.host=? AND s.account_label=?`, r.SessionID, project, r.RuntimeID, r.SessionGeneration, host, account).Scan(&agentID, &name, &harness, &phase)
	if e != nil || (live && phase == "stopped") {
		return consumerOwner{}, ErrConsumerUnavailable
	}
	out := consumerOwner{Registration: r, ProjectID: project, AgentID: agentID, UserID: c.Principal.UserID(), KeyID: c.Principal.APIKeyID(), Address: harness + ":" + name}
	if !live {
		return out, nil
	}
	target, e := GetTargetTx(ctx, tx, project, r.TargetID)
	if e != nil || !target.Enabled || target.Instance != instanceName() || target.Address != out.Address || int64(target.Version) != r.TargetVersion {
		return consumerOwner{}, ErrConsumerUnavailable
	}
	if r.Kind == "fallback" {
		if target.Adapter != AdapterCodex || target.Role != "simple_fallback" || target.MaximumLevel != "simple" {
			return consumerOwner{}, ErrConsumerUnavailable
		}
	} else if r.Kind == "attention" {
		selected, _, _, selectionErr := selectAttentionTarget(ctx, tx, project, out.Address)
		if selectionErr != nil || selected != target.ID || !isAttentionWakeAdapter(target.Adapter) {
			return consumerOwner{}, ErrConsumerUnavailable
		}
		if _, _, e = resolveAuthorizedAttentionReceiverTx(ctx, tx, project, out.Address, name); e != nil {
			return consumerOwner{}, ErrConsumerUnavailable
		}
	} else {
		return consumerOwner{}, ErrConsumerInvalid
	}
	return out, nil
}
func loadConsumer(ctx context.Context, tx *sql.Tx, project int64, id string) (ownedStream, error) {
	var out ownedStream
	var raw string
	e := tx.QueryRowContext(ctx, `SELECT id,revision,generation,kind,expires_at,registration_json,agent_id,address,user_id,api_key_id,lease_digest FROM agent_consumer_streams WHERE id=? AND project_id=?`, id, project).Scan(&out.ID, &out.Revision, &out.Generation, &out.Kind, &out.ExpiresAt, &raw, &out.AgentID, &out.Address, &out.UserID, &out.KeyID, &out.digest)
	if e != nil {
		return out, ErrConsumerUnavailable
	}
	if json.Unmarshal([]byte(raw), &out.Registration) != nil {
		return out, ErrConsumerStorage
	}
	out.ProjectID = project
	out.SchemaVersion = 1
	return out, nil
}
func authorizeConsumer(ctx context.Context, tx *sql.Tx, c ConsumerCredentials, project int64, id string, revision int64) (ownedStream, error) {
	if e := consumerPrincipal(ctx, tx, c.Principal, project); e != nil {
		return ownedStream{}, e
	}
	out, e := loadConsumer(ctx, tx, project, id)
	if e != nil {
		return out, e
	}
	if out.UserID != c.Principal.UserID() || out.KeyID != c.Principal.APIKeyID() || out.Revision != revision || consumerExpired(out.ExpiresAt) || !consumerProof(c.ConsumerLease) || subtle.ConstantTimeCompare(out.digest, consumerDigest(c.ConsumerLease)) != 1 {
		return ownedStream{}, ErrConsumerUnavailable
	}
	_, e = consumerBinding(ctx, tx, c, project, out.Registration, true)
	return out, e
}
func (s *Service) RegisterConsumer(ctx context.Context, c ConsumerCredentials, project int64, r ConsumerRegistration) (ConsumerStream, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ConsumerStream{}, ErrConsumerStorage
	}
	defer tx.Rollback()
	if e = consumerPrincipal(ctx, tx, c.Principal, project); e != nil {
		return ConsumerStream{}, e
	}
	for _, id := range []string{r.RuntimeID, r.RuntimeGeneration, r.SessionID, r.SessionGeneration, r.Generation, r.TargetID} {
		if !consumerID(id) {
			return ConsumerStream{}, ErrConsumerInvalid
		}
	}
	if r.TargetVersion <= 0 || !consumerProof(c.ConsumerLease) {
		return ConsumerStream{}, ErrConsumerInvalid
	}
	owner, e := consumerBinding(ctx, tx, c, project, r, true)
	if e != nil {
		return ConsumerStream{}, e
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id FROM agent_consumer_streams WHERE project_id=? AND agent_id=? AND kind=?`, project, owner.AgentID, r.Kind).Scan(&id)
	revision := int64(1)
	if e == nil {
		old, err := loadConsumer(ctx, tx, project, id)
		if err != nil {
			return ConsumerStream{}, err
		}
		if consumerJSON(old.Registration) == consumerJSON(r) && old.UserID == owner.UserID && old.KeyID == owner.KeyID && subtle.ConstantTimeCompare(old.digest, consumerDigest(c.ConsumerLease)) == 1 {
			if consumerExpired(old.ExpiresAt) {
				return ConsumerStream{}, ErrConsumerUnavailable
			}
			revision = old.Revision
		} else {
			if !consumerExpired(old.ExpiresAt) {
				return ConsumerStream{}, ErrConsumerConflict
			}
			var openID string
			err = tx.QueryRowContext(ctx, `SELECT id FROM agent_consumer_attempts WHERE stream_id=? AND state IN ('claimed','executing','outcome_unknown')`, id).Scan(&openID)
			if err == nil {
				attempt, loadErr := loadAttempt(ctx, tx, openID)
				if loadErr != nil {
					return ConsumerStream{}, loadErr
				}
				if loadErr = expireConsumerAttempt(ctx, tx, &attempt); loadErr != nil {
					return ConsumerStream{}, loadErr
				}
				if attempt.State != "released" {
					if tx.Commit() != nil {
						return ConsumerStream{}, ErrConsumerStorage
					}
					return ConsumerStream{}, ErrConsumerHandoff
				}
			} else if err != sql.ErrNoRows {
				return ConsumerStream{}, ErrConsumerStorage
			}
			revision = old.Revision + 1
			if old.Generation == r.Generation {
				return ConsumerStream{}, ErrConsumerUnavailable
			}
		}
	} else if e == sql.ErrNoRows {
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_consumer_streams WHERE project_id=?`, project).Scan(&count) != nil {
			return ConsumerStream{}, ErrConsumerStorage
		}
		if count >= 256 {
			return ConsumerStream{}, ErrConsumerConflict
		}
		id = uuid.NewString()
	} else {
		return ConsumerStream{}, ErrConsumerStorage
	}
	// Never steal an ambiguous legacy handoff, even after its time lease expired.
	var legacy int
	if r.Kind == "fallback" {
		e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_message_deliveries d JOIN agent_messages m ON m.id=d.message_row_id WHERE m.to_agent_id=? AND m.to_address=? AND d.state='leased' AND `+selectedDeliveryTargetSQL+`=? AND NOT EXISTS(SELECT 1 FROM agent_consumer_attempts a WHERE a.resource_id=d.delivery_id AND a.stream_id=?)`, owner.AgentID, owner.Address, r.TargetID, id).Scan(&legacy)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attention_batches b WHERE receiver_project_agent_id=? AND state='leased' AND NOT EXISTS(SELECT 1 FROM agent_consumer_attempts a WHERE a.resource_id=b.batch_id AND a.stream_id=?)`, owner.AgentID, id).Scan(&legacy)
	}
	if e != nil {
		return ConsumerStream{}, ErrConsumerStorage
	}
	if legacy > 0 {
		return ConsumerStream{}, ErrConsumerHandoff
	}
	deadline := consumerStamp(time.Now().Add(120 * time.Second))
	_, e = tx.ExecContext(ctx, `INSERT INTO agent_consumer_streams(id,project_id,agent_id,address,kind,revision,generation,registration_json,runtime_id,runtime_generation,session_id,session_generation,user_id,api_key_id,target_id,target_version,lease_digest,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,generation=excluded.generation,registration_json=excluded.registration_json,runtime_id=excluded.runtime_id,runtime_generation=excluded.runtime_generation,session_id=excluded.session_id,session_generation=excluded.session_generation,user_id=excluded.user_id,api_key_id=excluded.api_key_id,target_id=excluded.target_id,target_version=excluded.target_version,lease_digest=excluded.lease_digest,expires_at=excluded.expires_at,address=excluded.address`, id, project, owner.AgentID, owner.Address, r.Kind, revision, r.Generation, consumerJSON(r), r.RuntimeID, r.RuntimeGeneration, r.SessionID, r.SessionGeneration, owner.UserID, owner.KeyID, r.TargetID, r.TargetVersion, consumerDigest(c.ConsumerLease), deadline)
	if e != nil || tx.Commit() != nil {
		return ConsumerStream{}, ErrConsumerStorage
	}
	return ConsumerStream{SchemaVersion: 1, ID: id, Revision: revision, Generation: r.Generation, Kind: r.Kind, ExpiresAt: deadline}, nil
}
