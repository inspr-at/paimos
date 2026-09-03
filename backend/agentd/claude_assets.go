// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion   = "0.3.251"
	claudeAgentSDKSHA256    = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion = "2.1.251"
	claudeBridgeSHA256      = "cd6b29607ccd919d2458e6f92d102ef0e90666537d0562e215b68793735c94bc"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
)
