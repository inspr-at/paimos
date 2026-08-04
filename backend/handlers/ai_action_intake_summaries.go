// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-709 (epic PAI-703). The intake_summaries action — ELI5 / ELI10 /
// ELI15 understanding checks over the current specification, so the
// speaker can verify at a glance that the system understood the idea at
// three levels of sophistication. Runs every other spec cycle (cost
// control); language follows the spec.

package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func init() {
	replaceAction(actionDescriptor{
		Key:         "intake_summaries",
		Label:       "Intake: understanding check (ELI5/10/15)",
		Surface:     "intake",
		Placement:   "text",
		Handler:     intakeSummariesHandler,
		Implemented: true,
	})
}

type intakeSummariesParams struct {
	Language string `json:"language"`
}

type intakeSummariesBody struct {
	ELI5  string `json:"eli5"`
	ELI10 string `json:"eli10"`
	ELI15 string `json:"eli15"`
}

func intakeSummariesHandler(ax *aiActionContext) (any, string, int, int, string, error) {
	if strings.TrimSpace(ax.Text) == "" {
		return nil, "", 0, 0, "", &userError{status: 400, msg: "intake_summaries requires a specification"}
	}
	var params intakeSummariesParams
	if len(ax.Params) > 0 {
		_ = json.Unmarshal(ax.Params, &params)
	}
	lang := params.Language
	if lang != "de" {
		lang = "en"
	}

	systemPrompt := resolveActionPromptWithPreset(ax, "intake_summaries")
	var u strings.Builder
	u.WriteString("Requested language: " + lang + "\n\nSPECIFICATION:\n")
	u.WriteString(ax.Text)
	u.WriteString("\n\nReturn the JSON object with the three summaries.")

	ctx, cancel := context.WithTimeout(ax.Ctx, 45*time.Second)
	defer cancel()
	var body intakeSummariesBody
	model, ptok, ctok, finish, err := callJSONAction(ctx, ax, systemPrompt, u.String(), 900, &body)
	if err != nil {
		return nil, model, ptok, ctok, finish, err
	}
	return body, model, ptok, ctok, finish, nil
}
