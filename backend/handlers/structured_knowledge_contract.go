// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

// Production-pinned reviewed Compact body limit, measured in UTF-8 bytes.
// Proposal candidates remain bounded separately at 64 KiB so validation can
// explain why a candidate must be shortened before it becomes durable.
const structuredKnowledgeShortBodyLimitBytes = 1200
