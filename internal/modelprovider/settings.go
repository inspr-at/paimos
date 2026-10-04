// SPDX-License-Identifier: AGPL-3.0-only

// Package modelprovider supplies opt-in workspace AI through an OpenAI-compatible
// API. It has no vendor fallback and never starts an agent harness.
package modelprovider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
)

var ErrDisabled = errors.New("workspace model feature is disabled")

const credentialID = "workspace-model-provider"

type Features struct {
	ParentBenefits bool `json:"parent_benefits"`
	CRMNoteRewrite bool `json:"crm_note_rewrite"`
	Embeddings     bool `json:"embeddings"`
}

// Settings contains no credentials; it is safe to store as JSON.
type Settings struct {
	Enabled        bool     `json:"enabled"`
	BaseURL        string   `json:"base_url"`
	ChatModel      string   `json:"chat_model"`
	EmbeddingModel string   `json:"embedding_model"`
	Features       Features `json:"features"`
}

type Config struct {
	Settings
	ProviderID string `json:"provider_id"`
	Revision   int64  `json:"revision"`
	HasAPIKey  bool   `json:"has_api_key"`
}

// Secret cannot accidentally serialize or format its cleartext value.
type Secret string

func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"********"`), nil }
func (Secret) String() string               { return "********" }
func (Secret) GoString() string             { return "********" }

type Write struct {
	Settings
	ExpectedRevision int64   `json:"expected_revision"`
	APIKey           *Secret `json:"api_key,omitempty"`
}

type Service struct {
	pool   *pgxpool.Pool
	key    []byte
	client *http.Client
}

// New reuses the host session master and the existing AES-GCM vault with a
// provider-specific derivation and tenant-bound authenticated encryption.
func New(pool *pgxpool.Pool, master []byte) *Service {
	s := &Service{pool: pool, client: newClient()}
	if len(master) >= 32 {
		sum := sha256.Sum256(append([]byte("aeon/workspace-model-provider/v1\x00"), master...))
		s.key = sum[:]
	}
	return s
}

func Load(ctx context.Context, tx pgx.Tx) (Config, error) {
	c, _, err := load(ctx, tx)
	return c, err
}

func load(ctx context.Context, tx pgx.Tx) (Config, []byte, error) {
	var c Config
	var raw, sealed []byte
	err := tx.QueryRow(ctx, `SELECT id::text,settings,credential,revision FROM workspace_model_provider`).Scan(&c.ProviderID, &raw, &sealed, &c.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, nil
	}
	if err != nil {
		return c, nil, err
	}
	if err = json.Unmarshal(raw, &c.Settings); err != nil {
		return Config{}, nil, errors.New("invalid provider configuration")
	}
	c.HasAPIKey = len(sealed) > 0
	return c, sealed, nil
}

func (s Settings) validate() error {
	if s.BaseURL != "" {
		if _, err := endpoint(s.BaseURL, "chat/completions"); err != nil {
			return fault(400, "invalid provider base URL")
		}
	}
	for _, model := range []string{s.ChatModel, s.EmbeddingModel} {
		if len(model) > 200 || strings.ContainsAny(model, "\r\n\x00") || strings.TrimSpace(model) != model {
			return fault(400, "invalid model name")
		}
	}
	if s.Enabled && (s.BaseURL == "" || s.ChatModel == "") {
		return fault(400, "base URL and chat model are required")
	}
	if s.Features.Embeddings && s.EmbeddingModel == "" {
		return fault(400, "embedding model is required when embeddings are selected")
	}
	return nil
}

// Save is local-only: no endpoint probe or model request occurs here.
func (s *Service) Save(ctx context.Context, p tenant.Principal, in Write) (Config, error) {
	if err := in.Settings.validate(); err != nil {
		return Config{}, err
	}
	if in.ExpectedRevision < 0 || in.APIKey != nil && (len(*in.APIKey) > 16384 || *in.APIKey == "********" || strings.ContainsAny(string(*in.APIKey), " \t\r\n\x00")) {
		return Config{}, fault(400, "invalid provider settings")
	}
	var out Config
	err := db.InTenant(db.AllProjects(ctx, "workspace embedding reindex"), s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.LockMutation(ctx, tx); err != nil {
			return err
		}
		if p.Kind != tenant.Person {
			return fault(403, "person required")
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "model-provider/"+p.TenantID); err != nil {
			return err
		}
		prior, sealed, err := load(ctx, tx)
		if err != nil {
			return err
		}
		if prior.Revision != in.ExpectedRevision {
			return fault(409, "provider settings changed; reload before saving")
		}
		// A retained key must never travel to a newly selected endpoint.
		if prior.BaseURL != in.BaseURL {
			sealed = nil
		}
		if in.APIKey != nil {
			sealed = nil
			if *in.APIKey != "" {
				sealed, err = linkvault.Encrypt(s.key, p.TenantID, credentialID, string(*in.APIKey))
				if err != nil {
					return fault(503, "provider credential storage is unavailable")
				}
			}
		}
		raw, err := json.Marshal(in.Settings)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO workspace_model_provider(tenant_id,settings,credential) VALUES($1,$2,$3)
			ON CONFLICT(tenant_id) DO UPDATE SET settings=EXCLUDED.settings,credential=EXCLUDED.credential,revision=workspace_model_provider.revision+1,updated_at=now()`, p.TenantID, raw, sealed); err != nil {
			return err
		}
		// Reindex when enabling or selecting a different vector space. The queue
		// version also prevents an older in-flight job from overwriting this one.
		if in.Enabled && in.Features.Embeddings && (!prior.Enabled || !prior.Features.Embeddings || prior.BaseURL != in.BaseURL || prior.EmbeddingModel != in.EmbeddingModel) {
			if _, err := tx.Exec(ctx, `INSERT INTO node_embedding_jobs(tenant_id,node_id,status,attempts,last_error,queued_at,updated_at)
				SELECT tenant_id,id,'queued',0,NULL,clock_timestamp(),clock_timestamp() FROM nodes WHERE deleted_at IS NULL
				ON CONFLICT(tenant_id,node_id) DO UPDATE SET status='queued',attempts=0,last_error=NULL,queued_at=EXCLUDED.queued_at,updated_at=EXCLUDED.updated_at`); err != nil {
				return err
			}
		}
		out, err = Load(ctx, tx)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "tenant.model_provider_updated", After: map[string]any{
			"provider_id": out.ProviderID, "revision": out.Revision, "enabled": out.Enabled,
			"features": out.Features, "credential_changed": in.APIKey != nil || prior.BaseURL != in.BaseURL,
		}})
		return err
	})
	return out, err
}

func (s *Service) resolve(ctx context.Context, tenantID string) (Config, Secret, error) {
	var c Config
	var key Secret
	err := db.InTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var sealed []byte
		var err error
		c, sealed, err = load(ctx, tx)
		if err != nil || len(sealed) == 0 {
			return err
		}
		plain, err := linkvault.Decrypt(s.key, tenantID, credentialID, sealed)
		if err != nil {
			return fault(503, "provider credential is unavailable")
		}
		key = Secret(plain)
		return nil
	})
	return c, key, err
}
