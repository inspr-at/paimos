// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import "context"

// RuntimeAuthority is the narrow daemon integration seam. Implementations bind
// an instance URL, authenticated API-key principal and private runtime lease;
// none of those are browser input. An HTTP adapter uses the v1 wire structs.
//
// A daemon journals the intent ID before requesting executing, and invokes its
// typed local operation at most once. Recovered executing work may only report
// a proved journal outcome or outcome_unknown; it must never spawn again.
// Cancellation wins until executing commits. Completion of start/restart also
// requires RegisterSession for the reserved NewGeneration. The server applies
// attach/reassign's binding CAS in the completed transition, so daemon code
// must not independently call the legacy binding endpoint for the same intent.
//
// RuntimeAuthority deliberately has no arbitrary command or prompt method.
// Local adapters resolve configured workspace handles, reverify provenance,
// and obtain canonical agent/ticket instructions through authorized resources.
type RuntimeAuthority interface {
	RegisterRuntime(context.Context, Registration) (Runtime, error)
	RegisterSession(context.Context, string, SessionRegistration, string) error
	Claim(context.Context, string) (*Intent, error)
	Transition(context.Context, string, Transition) (Intent, error)
}
