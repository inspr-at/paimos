// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

type CommandPaletteSettings struct {
	SchemaVersion     int     `json:"schema_version"`
	DefaultShortcut   string  `json:"default_shortcut"`
	InstanceShortcut  *string `json:"instance_shortcut"`
	UserShortcut      *string `json:"user_shortcut"`
	EffectiveShortcut string  `json:"effective_shortcut"`
	Source            string  `json:"source"`
}

type CommandPaletteSessionResult struct {
	ProductSessionID string `json:"product_session_id"`
	Title            string `json:"title"`
	Summary          string `json:"summary"`
	UpdatedAt        string `json:"updated_at"`
}

type CommandPaletteNodeResult struct {
	NodeID    int64  `json:"node_id"`
	NodeKey   string `json:"node_key"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	TypeLabel string `json:"type_label"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

type CommandPaletteKnowledgeResult struct {
	KnowledgeID int64  `json:"knowledge_id"`
	Type        string `json:"type"`
	TypeLabel   string `json:"type_label"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	UpdatedAt   string `json:"updated_at"`
}

type CommandPaletteSearchResponse struct {
	SchemaVersion int                             `json:"schema_version"`
	Query         string                          `json:"query"`
	Sessions      []CommandPaletteSessionResult   `json:"sessions"`
	Nodes         []CommandPaletteNodeResult      `json:"nodes"`
	Knowledge     []CommandPaletteKnowledgeResult `json:"knowledge"`
}
