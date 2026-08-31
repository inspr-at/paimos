// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// StructuredKnowledgeSnapshot is the closed Paimos 6 project knowledge
// projection. Compact is always a product-session identity; legacy issue-backed
// knowledge is carried explicitly instead of being silently treated as compact.
type StructuredKnowledgeSnapshot struct {
	SchemaVersion           int                              `json:"schema_version"`
	ProjectID               int64                            `json:"project_id"`
	ShortBodyLimitBytes     int                              `json:"short_body_limit_bytes"`
	CompactProductSessionID *string                          `json:"compact_product_session_id"`
	Entries                 []StructuredKnowledgeEntry       `json:"entries"`
	Legacy                  []LegacyStructuredKnowledgeEntry `json:"legacy"`
	Proposals               []StructuredKnowledgeProposal    `json:"proposals"`
}

type StructuredKnowledgeEntry struct {
	KnowledgeID              int64                         `json:"knowledge_id"`
	Level                    string                        `json:"level"`
	Type                     string                        `json:"type"`
	Slug                     string                        `json:"slug"`
	Title                    string                        `json:"title"`
	Purpose                  string                        `json:"purpose"`
	ShortBody                string                        `json:"short_body"`
	AuthoredProductSessionID string                        `json:"authored_product_session_id"`
	Revision                 int64                         `json:"revision"`
	Links                    []StructuredKnowledgeLink     `json:"links"`
	Validation               StructuredKnowledgeValidation `json:"validation"`
	CreatedAt                string                        `json:"created_at"`
	UpdatedAt                string                        `json:"updated_at"`
}

// StructuredKnowledgeLink is the focal-entry projection of one canonical DB
// row. A stored parent row projects as parent from the child and child from the
// parent; see_also projects identically from either endpoint.
type StructuredKnowledgeLink struct {
	LinkID          int64  `json:"link_id"`
	Relation        string `json:"relation"`
	TargetIssueID   int64  `json:"target_issue_id"`
	TargetKnowledge bool   `json:"target_knowledge"`
	TargetType      string `json:"target_type"`
	TargetSlug      string `json:"target_slug"`
	TargetTitle     string `json:"target_title"`
}

type StructuredKnowledgeValidation struct {
	Flags               []string `json:"flags"`
	LikelyDuplicateIDs  []int64  `json:"likely_duplicate_ids"`
	BodyBytes           int      `json:"body_bytes"`
	ShortBodyLimitBytes int      `json:"short_body_limit_bytes"`
}

// LegacyStructuredKnowledgeEntry is deliberately not a lossy body projection.
// Clients receive enough identity to open the 5.x editor plus validation facts,
// while purpose/short_body remain absent until an explicit adoption succeeds.
type LegacyStructuredKnowledgeEntry struct {
	KnowledgeID int64                         `json:"knowledge_id"`
	Type        string                        `json:"type"`
	Slug        string                        `json:"slug"`
	Title       string                        `json:"title"`
	BodyBytes   int                           `json:"body_bytes"`
	Validation  StructuredKnowledgeValidation `json:"validation"`
	UpdatedAt   string                        `json:"updated_at"`
}

type StructuredKnowledgeProposal struct {
	ProposalID          string                        `json:"proposal_id"`
	SourceKind          string                        `json:"source_kind"`
	ProductSessionID    string                        `json:"product_session_id"`
	Type                string                        `json:"type"`
	Slug                string                        `json:"slug"`
	Title               string                        `json:"title"`
	Purpose             string                        `json:"purpose"`
	CandidateBody       string                        `json:"candidate_body"`
	State               string                        `json:"state"`
	PromotedKnowledgeID *int64                        `json:"promoted_knowledge_id"`
	Validation          StructuredKnowledgeValidation `json:"validation"`
	CreatedAt           string                        `json:"created_at"`
	UpdatedAt           string                        `json:"updated_at"`
}

type StructuredKnowledgePromotionLinkResult struct {
	OriginalLinkID  int64  `json:"original_link_id"`
	Outcome         string `json:"outcome"`
	ResultingLinkID *int64 `json:"resulting_link_id"`
	Reason          string `json:"reason"`
}

type StructuredKnowledgePromotionResult struct {
	PromotionID string                                   `json:"promotion_id"`
	FromLevel   string                                   `json:"from_level"`
	ToLevel     string                                   `json:"to_level"`
	Entry       StructuredKnowledgeEntry                 `json:"entry"`
	Links       []StructuredKnowledgePromotionLinkResult `json:"links"`
	ActorUserID int64                                    `json:"-"`
}
