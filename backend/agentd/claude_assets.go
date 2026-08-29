// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion   = "0.3.251"
	claudeAgentSDKSHA256    = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion = "2.1.251"
	claudeBridgeSHA256      = "316f8e1dd3cd04a574014beedc29c019cd4dd009eb36acee3304df413b739fd0"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
)
