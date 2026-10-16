// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/base64"
	"encoding/json"
)

type cursorEnv struct {
	V int             `json:"v"`
	T string          `json:"t"`
	P json.RawMessage `json:"p"`
}

type listMark struct {
	KindID      *string `json:"kind_id"`
	State       *string `json:"state"`
	ParentSet   bool    `json:"parent_set"`
	ParentID    *string `json:"parent_id"`
	Descendants bool    `json:"descendants"`
	Sort        string  `json:"sort"`
	Direction   string  `json:"direction"`
	Value       string  `json:"value"`
	ID          string  `json:"id"`
}

type treeMark struct {
	RootID   *string  `json:"root_id"`
	DepthSet bool     `json:"depth_set"`
	MaxDepth int      `json:"max_depth"`
	PosPath  []string `json:"pos_path"`
	IDPath   []string `json:"id_path"`
}

func encodeTyped(kind string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(cursorEnv{V: 1, T: kind, P: raw})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func openCursor(s string) (cursorEnv, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorEnv{}, badRequest("invalid cursor")
	}
	var env cursorEnv
	if err := json.Unmarshal(b, &env); err != nil || env.V != 1 || (env.T != "list" && env.T != "tree") {
		return cursorEnv{}, badRequest("invalid cursor")
	}
	return env, nil
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
