// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion   = "0.3.251"
	claudeAgentSDKSHA256    = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion = "2.1.251"
	claudeBridgeSHA256      = "b516fd4dcef367db8347832722a7d2f9034b0ff37ff419f4a79c83923d71b0e2"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
)
