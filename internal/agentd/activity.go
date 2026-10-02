// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/agentactivity"
)

func toolActivityEvent(tool string, input json.RawMessage) AdapterEvent {
	return AdapterEvent{Kind: "tool", ToolActivity: &agentactivity.Activity{
		Text: agentactivity.FromInput(tool, input), Source: "auto", At: time.Now().UTC(),
	}}
}

func mustActivityJSON(in any) []byte { raw, _ := json.Marshal(in); return raw }
