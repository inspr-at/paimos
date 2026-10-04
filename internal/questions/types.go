// SPDX-License-Identifier: AGPL-3.0-only

// Package questions owns Decision Desk questions and human answer authority.
// P1 stores pending effects; delivery and outcome adapters consume them later.
package questions

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxBody = 64 << 10

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var optionRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

type Option struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Answer      string `json:"answer"`
}
type DoctrineTarget struct {
	SourceID string `json:"source_id"`
	Path     string `json:"path"`
	RuleKey  string `json:"rule_key"`
	RuleSHA  string `json:"rule_sha256"`
	TLDREN   string `json:"tldr_en,omitempty"`
	TLDRDE   string `json:"tldr_de,omitempty"`
}

func (d *DoctrineTarget) validate() error {
	if d == nil {
		return nil
	}
	if !uuidRE.MatchString(d.SourceID) || !bounded(d.Path, 300, true) || !bounded(d.RuleKey, 200, true) || !digestRE.MatchString(d.RuleSHA) || !bounded(d.TLDREN, 300, false) || !bounded(d.TLDRDE, 300, false) {
		return fmt.Errorf("invalid doctrine source/rule/base mapping")
	}
	d.SourceID = strings.ToLower(d.SourceID)
	return nil
}

type OutcomeAvailability struct {
	MappingPresent bool   `json:"mapping_present,omitempty"`
	Outcome        string `json:"outcome"`
	Available      bool   `json:"available"`
	Why            string `json:"why"`
}
type Input struct {
	Doctrine         *DoctrineTarget `json:"doctrine,omitempty"`
	RequestID        string          `json:"request_id"`
	Question         string          `json:"question"`
	Context          string          `json:"context,omitempty"`
	Findings         string          `json:"findings,omitempty"`
	Options          []Option        `json:"options"`
	Recommend        string          `json:"recommend,omitempty"`
	Why              string          `json:"why,omitempty"`
	Meanwhile        string          `json:"meanwhile"`
	MeanwhileText    string          `json:"meanwhile_text,omitempty"`
	BlockedNodeIDs   []string        `json:"blocked_node_ids,omitempty"`
	SuggestedOutcome string          `json:"suggested_outcome,omitempty"`
	TicketID         string          `json:"ticket_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	SourceRequestID  string          `json:"source_request_id,omitempty"`
	SourceHandoverID string          `json:"source_handover_id,omitempty"`
	AnywayReason     string          `json:"anyway_reason,omitempty"`
}
type DecisionInput struct {
	Doctrine         *DoctrineTarget `json:"doctrine,omitempty"`
	TicketRevision   *time.Time      `json:"ticket_revision,omitempty"`
	RequestID        string          `json:"request_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	OptionID         string          `json:"option_id,omitempty"`
	Answer           string          `json:"answer,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Outcome          string          `json:"outcome"`
}
type Asker struct {
	ID            string    `json:"id"`
	PrincipalID   string    `json:"principal_id"`
	RequestID     string    `json:"request_id"`
	SessionID     string    `json:"session_id,omitempty"`
	ReplyRootID   string    `json:"reply_root_id"`
	CommentNodeID string    `json:"comment_node_id"`
	CreatedAt     time.Time `json:"created_at"`
	Input         Input     `json:"input"`
	FromRecord    *Reuse    `json:"from_record,omitempty"`
}
type Reuse struct {
	Label      string `json:"label"`
	DecisionID string `json:"decision_id"`
	Revision   int64  `json:"revision"`
}
type Answer struct {
	Replaces     string    `json:"replaces,omitempty"`
	ID           string    `json:"id"`
	Revision     int64     `json:"revision"`
	OptionID     string    `json:"option_id,omitempty"`
	Answer       string    `json:"answer"`
	Reason       string    `json:"reason,omitempty"`
	Outcome      string    `json:"outcome"`
	DecidedBy    string    `json:"decided_by"`
	CreatedAt    time.Time `json:"created_at"`
	DeliverAfter time.Time `json:"deliver_after"`
}
type Pending struct {
	DoctrineState     string     `json:"doctrine_state,omitempty"`
	ErrorMessage      string     `json:"error_message,omitempty"`
	EffectData        EffectData `json:"effect_data,omitempty"`
	ReceiptState      string     `json:"receipt_state,omitempty"`
	ReceiptFailure    string     `json:"receipt_failure,omitempty"`
	DeliverySessionID string     `json:"delivery_session_id,omitempty"`
	ID                string     `json:"id"`
	AskerID           string     `json:"asker_id,omitempty"`
	Revision          int64      `json:"revision"`
	Kind              string     `json:"kind"`
	State             string     `json:"state"`
	DeliverAfter      time.Time  `json:"deliver_after"`
	EffectRef         string     `json:"effect_ref,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
}
type Question struct {
	Outcomes         []OutcomeAvailability `json:"outcomes"`
	ID               string                `json:"id"`
	ProjectID        string                `json:"project_id"`
	Revision         int64                 `json:"revision"`
	State            string                `json:"state"`
	Input            Input                 `json:"input"`
	SuggestedOutcome string                `json:"suggested_outcome"`
	SuggestionReason string                `json:"suggestion_reason"`
	Askers           []Asker               `json:"askers"`
	Answer           *Answer               `json:"answer,omitempty"`
	Pending          []Pending             `json:"pending"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}
type Page struct {
	Items      []Question `json:"items"`
	HasMore    bool       `json:"has_more"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

func bounded(s string, max int, required bool) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= max && (!required || strings.TrimSpace(s) != "")
}
func outcome(s string) bool {
	return s == "once" || s == "always" || s == "requirement" || s == "doctrine"
}

// Validate is shared by HTTP, CLI and MCP. IDs are normalized before hashing.
func (in *Input) Validate() error {
	if err := in.Doctrine.validate(); err != nil {
		return err
	}
	if !uuidRE.MatchString(in.RequestID) {
		return fmt.Errorf("request_id must be a UUID")
	}
	in.RequestID = strings.ToLower(in.RequestID)
	for _, id := range []*string{&in.TicketID, &in.SessionID, &in.SourceRequestID, &in.SourceHandoverID} {
		if *id != "" && !uuidRE.MatchString(*id) {
			return fmt.Errorf("invalid reference UUID")
		}
		*id = strings.ToLower(*id)
	}
	if !bounded(in.Question, 2000, true) || !bounded(in.Context, 16000, false) || !bounded(in.Findings, 8000, false) || !bounded(in.Why, 2000, false) || !bounded(in.MeanwhileText, 2000, false) || !bounded(in.AnywayReason, 2000, in.AnywayReason != "") {
		return fmt.Errorf("question/context text is empty, invalid or too large")
	}
	if len(in.Options) < 1 || len(in.Options) > 9 {
		return fmt.Errorf("provide 1 to 9 options")
	}
	seen := map[string]bool{}
	for _, o := range in.Options {
		if !optionRE.MatchString(o.ID) || seen[o.ID] || !bounded(o.Title, 200, true) || !bounded(o.Description, 1000, false) || !bounded(o.Answer, 4000, true) {
			return fmt.Errorf("invalid or duplicate option")
		}
		seen[o.ID] = true
	}
	if in.Recommend != "" && (!seen[in.Recommend] || strings.TrimSpace(in.Why) == "") {
		return fmt.Errorf("recommend must name an option and include why")
	}
	if in.Meanwhile != "carries_on" && in.Meanwhile != "parked" && in.Meanwhile != "paused" && in.Meanwhile != "stopped" {
		return fmt.Errorf("invalid meanwhile state")
	}
	if in.SuggestedOutcome != "" && !outcome(in.SuggestedOutcome) {
		return fmt.Errorf("invalid suggested_outcome")
	}
	if len(in.BlockedNodeIDs) > 20 {
		return fmt.Errorf("too many blocked nodes")
	}
	seen = map[string]bool{}
	for i, id := range in.BlockedNodeIDs {
		id = strings.ToLower(id)
		if !uuidRE.MatchString(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate blocked node")
		}
		seen[id] = true
		in.BlockedNodeIDs[i] = id
	}
	return nil
}
func (in *DecisionInput) validate() error {
	if err := in.Doctrine.validate(); err != nil {
		return err
	}
	if !uuidRE.MatchString(in.RequestID) || in.ExpectedRevision < 1 {
		return fmt.Errorf("request_id and expected_revision are required")
	}
	in.RequestID = strings.ToLower(in.RequestID)
	if !outcome(in.Outcome) || (in.OptionID != "" && !optionRE.MatchString(in.OptionID)) || !bounded(in.Answer, 8000, in.OptionID == "") || !bounded(in.Reason, 2000, false) {
		return fmt.Errorf("invalid answer")
	}
	return nil
}
