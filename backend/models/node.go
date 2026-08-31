// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package models

// Node is the additive Paimos 6 view over an existing issues row. Kind is
// always "node"; CosmeticTypeLabel is presentation metadata derived from the
// legacy issue type and is never written back to storage.
type Node struct {
	NodeID            int64  `json:"node_id"`
	ProjectID         int64  `json:"project_id"`
	NodeKey           string `json:"node_key"`
	Kind              string `json:"kind"`
	CosmeticTypeLabel string `json:"cosmetic_type_label"`
	ParentNodeID      *int64 `json:"parent_node_id"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Status            string `json:"status"`
	Priority          string `json:"priority"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type NodeLabelConfig struct {
	Precedence       string            `json:"precedence"`
	GlobalDefaults   map[string]string `json:"global_defaults"`
	ProjectOverrides map[string]string `json:"project_overrides"`
	Resolved         map[string]string `json:"resolved"`
}
