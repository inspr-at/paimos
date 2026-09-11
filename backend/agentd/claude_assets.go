// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion   = "0.3.251"
	claudeAgentSDKSHA256    = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion = "2.1.251"
	claudeBridgeSHA256      = "45a281d5c7de411c6be0fbfd6de49adac959da1f61913d5bcb0ee16219fcf931"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
)
