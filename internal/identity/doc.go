// SPDX-License-Identifier: AGPL-3.0-only

// Package identity provides the internal, synchronous sign-in account
// provisioning port. It is deliberately separate from OIDC authentication and
// the sandboxed plugin system. The default driver is none. The Zitadel driver
// uses the v2 User API: POST /v2/users to search by exact email within one
// organization, POST /v2/users/new to create an unverified human, and POST
// /v2/users/{user_id}/invite_code with sendCode to have Zitadel deliver setup
// email. See https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.ListUsers,
// https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.CreateUser,
// and https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.CreateInviteCode.
//
// The coordinator wires FromEnv into authz.NewWithProvisioner. Enabling the
// Zitadel driver in an environment is a separate operator step: create a
// service account with user.read and user.write on only the target organization,
// place its PAT in a protected file from the canonical credential store, and
// set AEON_IDENTITY_PROVISIONER=zitadel, AEON_IDENTITY_TENANT_ID (the one
// Aeon tenant allowed to use this organization), AEON_ZITADEL_URL,
// AEON_ZITADEL_ORG_ID, and AEON_ZITADEL_TOKEN_FILE. No instance-wide role or
// credential belongs in the application repository. AEON sends no email and
// does not mark the address verified. Existing users are never changed.
package identity
