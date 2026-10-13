// SPDX-License-Identifier: AGPL-3.0-only

package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	n "github.com/inspr-at/paimos/internal/nativecontract"
)

type textInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type sandboxPolicy struct {
	Type          string   `json:"type"`
	WritableRoots []string `json:"writableRoots,omitempty"`
	NetworkAccess *bool    `json:"networkAccess,omitempty"`
}
type threadParams struct {
	ThreadID       string            `json:"threadId,omitempty"`
	Cwd            string            `json:"cwd"`
	Model          string            `json:"model"`
	ApprovalPolicy string            `json:"approvalPolicy"`
	Sandbox        string            `json:"sandbox"`
	Ephemeral      *bool             `json:"ephemeral,omitempty"`
	Config         map[string]string `json:"config"`
}
type turnParams struct {
	ThreadID       string        `json:"threadId"`
	Input          []textInput   `json:"input"`
	Model          string        `json:"model"`
	Effort         *string       `json:"effort,omitempty"`
	ApprovalPolicy string        `json:"approvalPolicy"`
	SandboxPolicy  sandboxPolicy `json:"sandboxPolicy"`
}
type steerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []textInput `json:"input"`
}
type interruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}
type threadResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model          string        `json:"model"`
	ModelProvider  string        `json:"modelProvider"`
	ApprovalPolicy string        `json:"approvalPolicy"`
	Sandbox        sandboxPolicy `json:"sandbox"`
	Cwd            string        `json:"cwd"`
}
type nativeItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Text   string `json:"text"`
	Phase  string `json:"phase"`
	Status string `json:"status"`
}
type nativeTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}
type nativeFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ThreadID   string     `json:"threadId"`
		TurnID     string     `json:"turnId"`
		Turn       nativeTurn `json:"turn"`
		Item       nativeItem `json:"item"`
		ItemID     string     `json:"itemId"`
		Delta      string     `json:"delta"`
		AuthMode   *string    `json:"authMode"`
		TokenUsage struct {
			Total struct {
				Total     *int64 `json:"totalTokens"`
				Input     *int64 `json:"inputTokens"`
				Cached    *int64 `json:"cachedInputTokens"`
				Output    *int64 `json:"outputTokens"`
				Reasoning *int64 `json:"reasoningOutputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
		RateLimits struct {
			LimitID   string      `json:"limitId"`
			Primary   *rateWindow `json:"primary"`
			Secondary *rateWindow `json:"secondary"`
		} `json:"rateLimits"`
	} `json:"params"`
}
type rateWindow struct {
	Used    *float64 `json:"usedPercent"`
	Minutes *int64   `json:"windowDurationMins"`
	Resets  *int64   `json:"resetsAt"`
}

func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// Check size/UTF-8/depth/duplicate keys before typed decode. Unknown fields
// never enter any exported projection, including errors.
func decode(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > n.NativeFrameBeforeDecodeBytes || !utf8.Valid(raw) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > n.NativeFrameDecodeDepth {
			return ErrProtocol
		}
		t, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				keys := make(map[string]bool)
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return ErrProtocol
					}
					key, ok := k.(string)
					if !ok || keys[key] {
						return ErrProtocol
					}
					keys[key] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return ErrProtocol
				}
			case '[':
				for d.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return ErrProtocol
				}
			default:
				return ErrProtocol
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrProtocol
	}
	return nil
}

func validRef(v string) bool {
	return v != "" && len(v) <= 128 && utf8.ValidString(v) && strings.TrimSpace(v) == v && !strings.ContainsFunc(v, unicode.IsControl)
}
func validText(v string) bool {
	return len(v) > 0 && len(v) <= n.InputUTF8Bytes && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}

// Provider references are domain separated per owned runtime before export.
func opaque(b n.Binding, domain, raw string) string {
	h := sha256.Sum256([]byte(b.TenantID + "\x00" + b.OwnedSessionID + "\x00" + b.RuntimeEpoch + "\x00" + domain + "\x00" + raw))
	return hex.EncodeToString(h[:])
}
func opaqueUUID(b n.Binding, domain, raw string) string {
	v, _ := hex.DecodeString(opaque(b, domain, raw))
	v[6] = v[6]&15 | 80
	v[8] = v[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", v[:4], v[4:6], v[6:8], v[8:10], v[10:16])
}
