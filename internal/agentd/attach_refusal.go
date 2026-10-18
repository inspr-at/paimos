// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

// ErrAttachExchange marks a failed remote registration or exchange. Local
// startup errors must never use it: they need a different recovery action.
var ErrAttachExchange = errors.New("attach exchange could not complete")

// Only the authenticated kernel-checked local helper gets these fixed next
// steps. Server text is used only for exact legacy matches, never interpolated.
func attachRefusal(err error) error {
	if local, ok := err.(*AttachLocalError); ok && local.Code == "attach_version_mismatch" {
		return local
	}
	code := "unknown"
	var status *client.StatusError
	var transport *url.Error
	if errors.Is(err, ErrDraining) || errors.Is(err, agentsetup.ErrAttachComputerDraining) {
		code = "draining"
	} else if errors.Is(err, agentsetup.ErrAttachPairingCleaned) {
		code = "pairing_revoked"
	} else if errors.Is(err, agentsetup.ErrAttachConfigMismatch) {
		code = "pairing_mismatch"
	} else if !errors.As(err, &status) {
		code = "local_unavailable"
		if errors.Is(err, ErrAttachExchange) || errors.As(err, &transport) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "offline"
		}
	} else {
		switch {
		case (status.Status == http.StatusTooManyRequests || status.Status == http.StatusForbidden) && status.Message == attachwatch.LiveLimitMessage:
			code = "live_limit"
		case status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden && status.AttachRefusal == attachwatch.RefusalPairing:
			code = "pairing_revoked"
		case status.Status == http.StatusConflict && status.AttachRefusal == attachwatch.RefusalVersion:
			code = "version_mismatch"
		case (status.Status == http.StatusForbidden || status.Status == http.StatusConflict) && status.AttachRefusal == attachwatch.RefusalTicket:
			code = "ticket_not_visible"
		case (status.Status == http.StatusForbidden || status.Status == http.StatusConflict) && status.AttachRefusal == attachwatch.RefusalDraining:
			code = "draining"
		case status.Status == http.StatusConflict && status.AttachRefusal == attachwatch.RefusalEnrollment:
			code = "enrollment_unavailable"
		case status.Status == http.StatusGone && status.AttachRefusal == attachwatch.RefusalExpired:
			code = "code_expired"
		case status.Status == http.StatusForbidden && status.AttachRefusal == attachwatch.RefusalPollKeyUnknown && status.ReasonCode == "":
			code = "poll_key_unknown"
		case status.Status == http.StatusForbidden && (status.ReasonCode == "missing_key_scope" || status.ReasonCode == "missing_role_permission" || status.ReasonCode == "missing_project_access"):
			code = "pairing_unavailable"
		default:
			code = legacyAttachRefusal(status)
		}
	}
	return &AttachLocalError{Code: "attach_" + code, Hint: attachRefusalHints[code]}
}

// Built once; callers receive only copies of the code list.
var attachRefusalHints = map[string]string{
	"live_limit":             fmt.Sprintf("%d attach requests already wait for approval in Aeon; approve or decline one there or let one expire, then run attach again", attachwatch.LiveMax),
	"pairing_revoked":        "This computer's pairing no longer authenticates. Check paired computers in Aeon; if revoked, pair this computer again, then run attach again.",
	"version_mismatch":       "Aeon and agentd use incompatible attach versions. Update Aeon and paimos-agentd, restart agentd, then run attach again; existing pairing keys remain valid.",
	"ticket_not_visible":     "The ticket or its project is unavailable to the pairing owner. Check the ticket belongs to the selected project and the owner has project access, then run attach again.",
	"code_expired":           "The attach code expired before activation. Run attach again and approve the new code in Aeon.",
	"draining":               "This computer or harness enrollment is disconnecting. For a harness drain, use aeon-agentd add-harness; the old enrollment may keep draining. For a computer drain, let owned work finish, then pair the computer again. Run attach again with fresh approval; a running process does not bypass disconnect.",
	"enrollment_unavailable": "This harness has no connected enrollment on the paired computer. Check its enrollments in Aeon, add the harness again with aeon-agentd add-harness if needed, then run attach again.",
	"poll_key_unknown":       "agentd re-registers automatically when attach is retried. Run attach again in a few seconds and give fresh approval in Aeon; a daemon restart is not required.",
	"registration_lost":      "Aeon no longer accepts this daemon's attach registration, for example after a server restart. When owned work permits, restart agentd, then run attach again and give fresh approval; re-pairing is not required.",
	"pairing_unavailable":    "The paired computer or its authorization is unavailable. Check the computer, its enrollments, runtime key scope and owner's account permissions in Aeon, then restart agentd and run attach again.",
	"scope_changed":          "The project, ticket or harness enrollment changed. Check the ticket's project, the owner's access and this computer's connected harness enrollments in Aeon, then run attach again.",
	"computer_limit":         fmt.Sprintf("This computer already has %d pending, approved or active attaches. Stop an attached helper, decline a pending request in Aeon or let it expire, then run attach again.", attachwatch.ComputerMax),
	"attempt_limit":          fmt.Sprintf("Too many attach attempts reached Aeon. Wait %g minutes before running attach again.", attachwatch.AttemptWindow.Minutes()),
	"registration_limit":     "Aeon's daemon registration capacity is full. Ask the Aeon administrator to check registration capacity, then restart agentd and run attach again.",
	"poll_limit":             "Attach polls reached Aeon too quickly or out of sequence. Update paimos-agentd, restart it when owned work permits, then run attach again.",
	"rate_limit":             "Aeon limited attach requests. Wait before retrying; if it continues, ask the Aeon administrator to check rate limits.",
	"snapshot_changed":       "The process snapshot or approval binding changed. Check the running process, approved folder and attach arguments, then run attach again for fresh approval.",
	"consent_required":       "The local confirmation or consent binding was refused. Update Aeon and paimos-agentd, restart agentd when owned work permits, then run attach again and complete fresh browser approval and Touch ID when requested.",
	"ended":                  "The attach request or session ended or was already used. Run attach again and approve the new request in Aeon.",
	"invalid_request":        "Aeon rejected the attach request format. Check the attach arguments and approved folder; update Aeon and paimos-agentd if needed, then run attach again.",
	"local_unavailable":      "Local attach startup could not complete. Check aeon-agentd status and the agentd log, then restart agentd when owned work permits and run attach again.",
	"pairing_mismatch":       "The paired origin or workspace configuration does not match this computer's approved pairing. Check aeon-agentd status and the agentd log; use the original approved setup and workspace, or pair the computer again for the intended origin and workspace, then restart agentd and run attach again.",
	"offline":                "The attach exchange with Aeon could not complete. Check the connection to the paired instance and aeon-agentd status, then run attach again; if agentd started while offline, restart it when owned work permits.",
	"server_unavailable":     "Aeon could not process the attach request. Check the paired instance's availability; ask its administrator to inspect the server logs if it persists, then run attach again.",
	"unknown":                "Aeon refused attach for an unrecognized reason. Check aeon-agentd status and ask the Aeon administrator to inspect the attach request in the server logs, then run attach again.",
}

// AttachRefusalCodes lists the fixed English repair codes for CLI/socket parity.
func AttachRefusalCodes() []string {
	codes := make([]string, 0, len(attachRefusalHints))
	for code := range attachRefusalHints {
		codes = append(codes, "attach_"+code)
	}
	slices.Sort(codes)
	return codes
}

func legacyAttachRefusal(status *client.StatusError) string {
	switch status.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return "invalid_request"
	case http.StatusForbidden:
		switch status.Message {
		case "daemon poll key rejected", "fresh daemon poll key required":
			return "registration_lost"
		case "paired daemon required", "computer proof rejected", "owner delegation no longer valid", "agent key scope required", "outside this computer's runtime authority", "permission denied", "forbidden":
			return "pairing_unavailable"
		case "attach proof rejected", "attach approval binding changed":
			return "snapshot_changed"
		case "boolean local confirmation is refused; upgrade agentd and re-pair",
			"local confirmation challenge already consumed or unavailable", "signed local confirmation required", "signed local confirmation rejected":
			return "consent_required"
		}
	case http.StatusConflict:
		switch status.Message {
		case "update agentd to attach protocol 2", "upgrade paimos-agentd to local consent proof v2",
			"update agentd to attach protocol 2; fresh approval required",
			"upgrade paimos-agentd to local consent proof v2 and restart; existing pairing keys remain valid; fresh approval required":
			return "version_mismatch"
		case "enrollment is draining; no new work":
			return "draining"
		case "project, ticket or harness enrollment changed":
			return "scope_changed"
		case "snapshot digest required", "attach snapshot is immutable", "process or approval snapshot changed",
			"snapshot platform does not match the paired computer":
			return "snapshot_changed"
		case "consent binding required or mismatched; update agentd and attach again", "consent binding mismatch":
			return "consent_required"
		case "attach request already consumed; create a new request":
			return "ended"
		case "poll sequence must increase":
			return "poll_limit"
		}
	case http.StatusGone:
		if status.Message == "pairing revoked or setup not redeemed" {
			return "pairing_revoked"
		}
		return "ended"
	case http.StatusTooManyRequests:
		switch status.Message {
		case "computer attach limit reached":
			return "computer_limit"
		case "attach attempt cap reached":
			return "attempt_limit"
		case "daemon registration capacity reached":
			return "registration_limit"
		case "poll at most once per second":
			return "poll_limit"
		}
		return "rate_limit"
	case http.StatusRequestTimeout:
		return "offline"
	}
	if status.Status >= 500 && status.Status <= 599 {
		return "server_unavailable"
	}
	return "unknown"
}
