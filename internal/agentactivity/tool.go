// SPDX-License-Identifier: AGPL-3.0-only

package agentactivity

import (
	"encoding/json"
	"strings"
	"time"
)

// FromInput decodes only the fields needed for classification. Contents,
// environment and tool results are never part of the returned value.
func FromInput(name string, input json.RawMessage) string {
	var in struct {
		Path     string   `json:"path"`
		FilePath string   `json:"file_path"`
		Command  string   `json:"command"`
		Cmd      string   `json:"cmd"`
		Args     []string `json:"args"`
	}
	if len(input) <= 1<<20 {
		_ = json.Unmarshal(input, &in)
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "mcp__aeon__"), "aeon_")
	if name == "terminal" {
		name = "Bash"
		in.Command = strings.Join(append([]string{in.Command}, in.Args...), " ")
	}
	if in.Path == "" {
		in.Path = in.FilePath
	}
	if in.Command == "" {
		in.Command = in.Cmd
	}
	return ToolText(name, in.Path, in.Command)
}

// TranscriptTool observes fresh tool envelopes from an already approved tail.
// It never returns the transcript's messages, results or argument strings.
func TranscriptTool(raw []byte) *Activity {
	var frame struct {
		Type    string          `json:"type"`
		Parent  json.RawMessage `json:"parent_tool_use_id"`
		Message struct {
			Content []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
		Payload struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"payload"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &frame) != nil || len(frame.Parent) > 0 && string(frame.Parent) != "null" {
		return nil
	}
	text := ""
	if frame.Type == "assistant" {
		for _, block := range frame.Message.Content {
			if block.Type == "tool_use" {
				text = FromInput(block.Name, block.Input)
			}
		}
	} else if frame.Type == "response_item" && frame.Payload.Type == "function_call" {
		text = FromInput(frame.Payload.Name, json.RawMessage(frame.Payload.Arguments))
	}
	if text == "" {
		return nil
	}
	return &Activity{Text: text, Source: "auto", At: time.Now().UTC()}
}
