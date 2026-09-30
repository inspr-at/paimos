// SPDX-License-Identifier: AGPL-3.0-only

package authz

// personAgentRoles are the legacy role tags a person-created agent or paired
// computer identity may carry. Every path a person can reach (Access → Agents
// creation, computer pairing, first key redemption) writes an empty role list;
// only Aeon's own services (system, importer, operator, embedding, the quote
// and portal services, whatever comes next) write a tag. So the set is empty and
// the classification is default-deny: a tag nobody listed here marks a service.
var personAgentRoles = map[string]bool{}

// IsPersonAgent reports whether an agent with these legacy roles is positively
// classified as a person-created agent or computer identity.
func IsPersonAgent(roles []string) bool {
	for _, v := range roles {
		if !personAgentRoles[v] {
			return false
		}
	}
	return true
}

// IsServiceIdentity reports whether an agent with these legacy roles is an
// internal service identity. Such identities belong to Aeon, not to a person's
// Access list: the directory flags them and the lifecycle refuses to deactivate
// them. It fails closed: every agent that is not a person agent is a service,
// including a role tag added in the future.
func IsServiceIdentity(roles []string) bool {
	return !IsPersonAgent(roles)
}
