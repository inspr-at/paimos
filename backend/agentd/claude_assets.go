// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import _ "embed"

const (
	claudeAgentSDKVersion     = "0.3.251"
	claudeAgentSDKSHA256      = "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93"
	claudeMinimumCLIVersion   = "2.1.251"
	claudeBridgeSHA256        = "f1f3ca2a7c81def3770f291eea27c2b02f6ac869d40bb892080aaadc2e26c2aa"
	claudeMessageSchemaSHA256 = "cd2fb323b484064de4c3c5e09265bdd12dbb5fa610ea3e1bfbb2f4a1e1db3910"
)

var (
	//go:embed claudeassets/bridge.mjs
	claudeAgentSDKBridge []byte
	//go:embed claudeassets/native-message-schema.mjs
	claudeNativeMessageSchema []byte
)
