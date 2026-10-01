// SPDX-License-Identifier: AGPL-3.0-only

package crm

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
)

// WorkspaceNotes binds the existing draft-only CRM tool to workspace AI. The
// plugin installation and crm.manage checks remain in the existing HTTP/tool path.
type WorkspaceNotes struct{ Provider *modelprovider.Service }

func (w WorkspaceNotes) NoteModel(ctx context.Context, tx pgx.Tx) (modelregistry.Profile, error) {
	c, err := modelprovider.Load(ctx, tx)
	if err != nil {
		return modelregistry.Profile{}, err
	}
	if w.Provider == nil || !c.Enabled || !c.Features.CRMNoteRewrite || c.ChatModel == "" {
		return modelregistry.Profile{}, pgx.ErrNoRows
	}
	return modelregistry.Profile{ID: c.ProviderID, Version: strconv.FormatInt(c.Revision, 10), Model: c.ChatModel, Enabled: true}, nil
}

func (w WorkspaceNotes) GenerateNote(ctx context.Context, profile modelregistry.Profile, prompt NotePrompt) (NoteGeneration, error) {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.Kind != tenant.Person || w.Provider == nil {
		return NoteGeneration{}, modelprovider.ErrDisabled
	}
	revision, err := strconv.ParseInt(profile.Version, 10, 64)
	if err != nil {
		return NoteGeneration{}, err
	}
	result, err := w.Provider.Chat(ctx, p.TenantID, profile.ID, revision, []modelprovider.Message{
		{Role: "system", Content: prompt.Instruction},
		{Role: "user", Content: "Customer: " + prompt.CustomerName + "\n\nNotes:\n" + prompt.CurrentNotes},
	})
	return NoteGeneration{Text: result.Text, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens}, err
}
