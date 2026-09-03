// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion   = "0.3.251"
	claudeAgentSDKSHA256    = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion = "2.1.251"
	claudeBridgeSHA256      = "9c4e3b60d7632751ad0cf6f00846ab8d0a113dc9182a8c44b0ae4c6ec9e001fb"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
)
