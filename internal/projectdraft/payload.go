// SPDX-License-Identifier: AGPL-3.0-only

// Package projectdraft receives a conversation handed over from START and
// turns it into a new project once a signed-in person confirms it (AEON-1441).
//
// The handoff is a public, top-level form POST of one field, payload, with the
// strict v1 document below. It stores an unclaimed draft in the one import
// tenant the instance configures and redirects to the assistant with a
// 256-bit capability in the URL fragment, so no server or proxy log sees it.
// Reading a draft never consumes it; only an explicit, authenticated create
// does, atomically, through the ordinary node, knowledge and attachment guards.
package projectdraft

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Contract v1 bounds (AEON-1441 v1.1). The wire bound is the decoded payload
// field; every other bound is checked before anything is stored.
const (
	MaxPayloadBytes = 512 << 10
	maxTitle        = 200
	maxSummary      = 4000
	maxItems        = 50
	maxItem         = 500
	maxTurns        = 2000
	maxHost         = 253
	maxSessionID    = 200
)

var (
	errVersion = errors.New("unsupported handoff version")
	errSchema  = errors.New("handoff does not match payload v1")

	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9.\-\[\]:]+$`)
	sessionPattern = regexp.MustCompile(`^[A-Za-z0-9._:\-]+$`)
)

// Source names where the conversation took place. It is shown as text and
// never chooses a tenant, project or destination.
type Source struct {
	Product   string `json:"product"`
	Host      string `json:"host"`
	SessionID string `json:"session_id"`
}

// Turn is one message of the conversation. At is optional: START may omit it.
type Turn struct {
	Role string     `json:"role"`
	Text string     `json:"text"`
	At   *time.Time `json:"at,omitempty"`
}

// Payload is a validated v1 handoff. Facts and open points are trimmed and
// blank items dropped; every other text stays as sent.
type Payload struct {
	Version    int      `json:"version"`
	Source     Source   `json:"source"`
	Language   string   `json:"language"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Facts      []string `json:"facts"`
	OpenPoints []string `json:"open_points"`
	Transcript []Turn   `json:"transcript"`
}

type wireTurn struct {
	Role *string `json:"role"`
	Text *string `json:"text"`
	At   *string `json:"at"`
}

type wirePayload struct {
	Version    *int        `json:"version"`
	Source     *Source     `json:"source"`
	Language   *string     `json:"language"`
	Title      *string     `json:"title"`
	Summary    *string     `json:"summary"`
	Facts      *[]string   `json:"facts"`
	OpenPoints *[]string   `json:"open_points"`
	Transcript *[]wireTurn `json:"transcript"`
}

// ParsePayload checks the version first, so a newer START gets its own
// answer, then decodes strictly: unknown fields, missing fields, wrong types,
// NUL characters and every v1 bound are errors.
func ParsePayload(raw []byte) (Payload, error) {
	if len(raw) == 0 || len(raw) > MaxPayloadBytes || !utf8.Valid(raw) {
		return Payload{}, errSchema
	}
	var head struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Payload{}, errSchema
	}
	var version int
	if len(head.Version) == 0 || json.Unmarshal(head.Version, &version) != nil {
		return Payload{}, errSchema
	}
	if version != 1 {
		return Payload{}, errVersion
	}
	var w wirePayload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil || dec.More() {
		return Payload{}, errSchema
	}
	if w.Source == nil || w.Language == nil || w.Title == nil || w.Summary == nil || w.Facts == nil || w.OpenPoints == nil || w.Transcript == nil {
		return Payload{}, errSchema
	}
	out := Payload{Version: 1, Source: *w.Source, Language: *w.Language, Title: strings.TrimSpace(*w.Title), Summary: strings.TrimSpace(*w.Summary)}
	switch {
	case out.Source.Product != "aithema",
		out.Source.Host == "" || len(out.Source.Host) > maxHost || !hostPattern.MatchString(out.Source.Host),
		out.Source.SessionID == "" || len(out.Source.SessionID) > maxSessionID || !sessionPattern.MatchString(out.Source.SessionID),
		out.Language != "de" && out.Language != "en",
		!text(out.Title, maxTitle), !text(out.Summary, maxSummary),
		len(*w.Facts) > maxItems, len(*w.OpenPoints) > maxItems, len(*w.Transcript) > maxTurns:
		return Payload{}, errSchema
	}
	var ok bool
	if out.Facts, ok = items(*w.Facts); !ok {
		return Payload{}, errSchema
	}
	if out.OpenPoints, ok = items(*w.OpenPoints); !ok {
		return Payload{}, errSchema
	}
	out.Transcript = make([]Turn, 0, len(*w.Transcript))
	for _, t := range *w.Transcript {
		if t.Role == nil || t.Text == nil || *t.Role != "user" && *t.Role != "assistant" || strings.ContainsRune(*t.Text, 0) {
			return Payload{}, errSchema
		}
		turn := Turn{Role: *t.Role, Text: *t.Text}
		if t.At != nil {
			at, err := time.Parse(time.RFC3339Nano, *t.At)
			if err != nil {
				return Payload{}, errSchema
			}
			at = at.UTC()
			turn.At = &at
		}
		out.Transcript = append(out.Transcript, turn)
	}
	return out, nil
}

func text(s string, limit int) bool {
	return utf8.RuneCountInString(s) <= limit && !strings.ContainsRune(s, 0)
}

func items(in []string) ([]string, bool) {
	out := make([]string, 0, len(in))
	for _, item := range in {
		if !text(item, maxItem) {
			return nil, false
		}
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out, true
}
