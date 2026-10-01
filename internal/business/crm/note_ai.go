// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// NoteToolID is the optional, admin-invoked CRM agent tool.
const NoteToolID = "crm_note_optimize"

// NotePrompt is the bounded customer context sent to the selected model.
type NotePrompt struct {
	CustomerName string
	CurrentNotes string
	Instruction  string
}

// NoteGeneration carries the text and bounded run-style usage evidence. A host
// adapter supplies model execution; CRM never reads credentials or runs a CLI.
type NoteGeneration struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
	RunID        string
}

// NoteGenerator is supplied by the coordinator. Tests use an in-memory fake.
// The model profile is an immutable pin from the tenant's model registry.
type NoteGenerator interface {
	GenerateNote(context.Context, modelregistry.Profile, NotePrompt) (NoteGeneration, error)
}

// WorkspaceNoteGenerator resolves in-app model settings instead of a harness
// scout route. Existing host generators keep their original registry contract.
type WorkspaceNoteGenerator interface {
	NoteGenerator
	NoteModel(context.Context, pgx.Tx) (modelregistry.Profile, error)
}

func (m *module) selectedNoteModel(ctx context.Context, tx pgx.Tx) (modelregistry.Profile, error) {
	if provider, ok := m.noteGenerator.(WorkspaceNoteGenerator); ok {
		return provider.NoteModel(ctx, tx)
	}
	return noteModel(ctx, tx)
}

type noteTool struct{ generator NoteGenerator }

func (t noteTool) Invoke(ctx context.Context, call plugins.Call, id string, input any) (any, error) {
	if id != NoteToolID || !call.Grant.Allows(fence.PermToolsInvoke) || call.Principal.Kind != tenant.Person || authz.Require(ctx, "crm.manage", authz.Scope{}) != nil {
		return nil, plugins.ErrDenied
	}
	if t.generator == nil {
		return nil, plugins.ErrClosed
	}
	in, ok := input.(noteToolInput)
	if !ok || in.Profile.ID == "" || len(in.Prompt.CurrentNotes) > 20000 {
		return nil, plugins.ErrDenied
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return t.generator.GenerateNote(callCtx, in.Profile, in.Prompt)
}

type noteToolInput struct {
	Profile modelregistry.Profile
	Prompt  NotePrompt
}

// PluginWithNoteGenerator declares the same manifest as Plugin and supplies
// the in-process agent tool implementation. The installation must explicitly
// grant tools.invoke, in addition to its ordinary CRM permissions.
func PluginWithNoteGenerator(generator NoteGenerator) (plugins.Plugin, error) {
	p, err := Plugin()
	if err == nil {
		p.Tools = noteTool{generator: generator}
	}
	return p, err
}

// NewWithNoteGenerator returns the CRM HTTP module with optional AI execution.
func NewWithNoteGenerator(pool *pgxpool.Pool, reg *plugins.Registry, generator NoteGenerator) httpapi.Module {
	return &module{pool: pool, reg: reg, noteGenerator: generator}
}

// PluginWithProvidersAndNoteGenerator combines both optional host adapters.
func PluginWithProvidersAndNoteGenerator(providers map[string]Provider, generator NoteGenerator) (plugins.Plugin, error) {
	p, err := PluginWithNoteGenerator(generator)
	if err == nil {
		p.Integrations = providerIntegration{providers: providers}
	}
	return p, err
}

// NewWithProvidersAndNoteGenerator mounts the combined CRM module.
func NewWithProvidersAndNoteGenerator(pool *pgxpool.Pool, reg *plugins.Registry, providers map[string]Provider, generator NoteGenerator) httpapi.Module {
	return &module{pool: pool, reg: reg, providers: providers, noteGenerator: generator}
}

type noteAIState struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// noteModel follows the configured scout route order without seeding a catalog.
// An empty model registry therefore leaves this optional action disabled.
func noteModel(ctx context.Context, tx pgx.Tx) (modelregistry.Profile, error) {
	var p modelregistry.Profile
	err := tx.QueryRow(ctx, `SELECT p.id::text,p.slug,p.version,p.harness,p.family,p.model,p.effort,p.tier,p.enabled,p.created_at
		FROM model_role_routes r JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.profile_id
		WHERE r.role='scout' AND p.enabled AND (r.state='available' OR r.valid_until<=now())
		ORDER BY r.priority LIMIT 1`).Scan(&p.ID, &p.Slug, &p.Version, &p.Harness, &p.Family, &p.Model, &p.Effort, &p.Tier, &p.Enabled, &p.CreatedAt)
	return p, err
}

func (m *module) noteState(ctx context.Context, p tenant.Principal, id string) (noteAIState, modelregistry.Profile, Customer, error) {
	state := noteAIState{Reason: "AI note rewriting is not enabled for this workspace."}
	var profile modelregistry.Profile
	var c Customer
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		c, err = customer(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if strings.TrimSpace(c.CustomerNotes) == "" {
			state.Reason = "Add customer notes before asking AI to rewrite them."
			return nil
		}
		if m.noteGenerator == nil || m.reg == nil {
			return nil
		}
		plug, registered := m.reg.Lookup(ID)
		tool, executable := plug.Tools.(noteTool)
		if !registered || !executable || tool.generator == nil {
			return nil
		}
		if err = m.gate(ctx, tx, p.TenantID, fence.PermToolsInvoke); err != nil {
			if errors.Is(err, errClosed) {
				return nil
			}
			return err
		}
		profile, err = m.selectedNoteModel(ctx, tx)
		if errors.Is(err, pgx.ErrNoRows) {
			state.Reason = "No model is configured for AI note rewriting."
			return nil
		}
		if err != nil {
			return err
		}
		state.Enabled, state.Reason = true, ""
		return nil
	})
	return state, profile, c, err
}

func (m *module) noteAIStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, err := pathUUID(r, "organisationId")
	if err != nil {
		writeErr(w, err)
		return
	}
	state, _, _, err := m.noteState(r.Context(), p, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, state)
}

func (m *module) generateNote(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, err := pathUUID(r, "organisationId")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err = decodeCRM(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	state, profile, c, err := m.noteState(r.Context(), p, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !state.Enabled {
		writeErr(w, &httpError{status: http.StatusConflict, code: "ai_unavailable", message: state.Reason})
		return
	}
	if in.ExpectedRevision != c.Revision {
		writeErr(w, errConflict)
		return
	}
	tool := plugins.NewWithRegistry(m.pool, m.reg)
	result, err := tool.InvokeTool(r.Context(), p, ID, NoteToolID, noteToolInput{Profile: profile, Prompt: NotePrompt{
		CustomerName: c.Name, CurrentNotes: c.CustomerNotes,
		Instruction: "Improve clarity and wording of these internal customer notes. Preserve every fact, name, date, qualification and uncertainty. Add no facts. Return only the revised notes as Markdown.",
	}})
	if err != nil {
		writeErr(w, &httpError{status: http.StatusServiceUnavailable, code: "ai_unavailable", message: "AI note rewriting is unavailable. The notes were not changed."})
		return
	}
	generated, ok := result.(NoteGeneration)
	if !ok || strings.TrimSpace(generated.Text) == "" || len(generated.Text) > 20000 || generated.InputTokens < 0 || generated.OutputTokens < 0 || generated.CostMicros < 0 {
		writeErr(w, &httpError{status: http.StatusBadGateway, code: "invalid_generation", message: "The generated notes were invalid. The notes were not changed."})
		return
	}
	if generated.Text == c.CustomerNotes {
		writeErr(w, &httpError{status: http.StatusConflict, code: "unchanged_generation", message: "The model suggested no change to the notes."})
		return
	}
	var draftID string
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermToolsInvoke); err != nil {
			return err
		}
		current, err := customer(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		if current.Revision != c.Revision || current.CustomerNotes != c.CustomerNotes {
			return errConflict
		}
		selected, err := m.selectedNoteModel(r.Context(), tx)
		if err != nil || selected.ID != profile.ID || selected.Version != profile.Version {
			return errConflict
		}
		if generated.RunID != "" {
			if _, ok := parseUUID(generated.RunID); !ok {
				return errConflict
			}
			var completed bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1::uuid AND model_profile_id=$2::uuid AND status='completed')`, generated.RunID, profile.ID).Scan(&completed); err != nil {
				return err
			}
			if !completed {
				return errConflict
			}
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO crm_note_rewrite_drafts(tenant_id,organisation_node_id,base_revision,proposed_text,proposed_by_principal_id) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid) RETURNING id::text`, p.TenantID, id, c.Revision, generated.Text, p.ID).Scan(&draftID)
		if err != nil {
			return err
		}
		evidence := map[string]any{
			"draft_id": draftID, "base_revision": c.Revision, "source": "ai", "model_profile_id": profile.ID,
			"run_id": generated.RunID, "input_tokens": generated.InputTokens, "output_tokens": generated.OutputTokens, "cost_micros": generated.CostMicros,
		}
		if _, workspace := m.noteGenerator.(WorkspaceNoteGenerator); workspace {
			delete(evidence, "model_profile_id")
			evidence["workspace_provider_id"] = profile.ID
			evidence["workspace_provider_revision"] = profile.Version
		}
		return appendCRM(r.Context(), tx, p, id, "crm.note_rewrite_drafted", nil, evidence)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"id": draftID, "organisation_node_id": id, "draft_text": generated.Text, "base_revision": c.Revision, "applied": false})
}
