// SPDX-License-Identifier: AGPL-3.0-only
package stepup

// OutcomeMessage is the code-owned result passed to the existing inbox recorder.
// It contains no proof material and never chooses a successor session.
type OutcomeMessage struct {
	ID, ProjectID, RecipientID, SessionID, Body string
}
