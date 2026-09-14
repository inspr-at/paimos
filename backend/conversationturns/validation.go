// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package conversationturns

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var stableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var lowerDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func printable(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len([]byte(value)) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

func stable(value string, maximum int) bool {
	return len([]byte(value)) <= maximum && stableValue.MatchString(value)
}

func (actor Actor) Valid() bool {
	return printable(actor.Issuer, 256) && printable(actor.Subject, 256)
}

func validateRequest(in Request) error {
	if in.SchemaVersion != SchemaVersion || !stable(in.RequestID, 128) || uuid.Validate(in.BindingID) != nil ||
		in.BindingRevision < 1 || !in.Actor.Valid() || !printable(in.ProjectRef, 128) ||
		!stable(in.ConversationID, 128) || !stable(in.TurnID, 128) || in.TimeoutMS < 1 || in.TimeoutMS > MaximumTimeoutMS {
		return ErrInvalid
	}
	switch in.Purpose {
	case "chat", "understand", "interpret":
	default:
		return ErrInvalid
	}
	if in.Messages == nil || len(in.Messages) == 0 || len(in.Messages) > MaximumMessages || !utf8.ValidString(in.System) {
		return ErrInvalid
	}
	total := len([]byte(in.System))
	for _, message := range in.Messages {
		if (message.Role != "user" && message.Role != "assistant") || message.Content == "" || !utf8.ValidString(message.Content) {
			return ErrInvalid
		}
		total += len([]byte(message.Content))
		if total > MaximumInputBytes {
			return ErrTooLarge
		}
	}
	return nil
}

func normalizeLimits(in Limits) (Limits, error) {
	if in.MaxInputBytes == 0 {
		in.MaxInputBytes = MaximumInputBytes
	}
	if in.MaxMessages == 0 {
		in.MaxMessages = MaximumMessages
	}
	if in.MaxOutputBytes == 0 {
		in.MaxOutputBytes = MaximumOutputBytes
	}
	if in.MaxEventBytes == 0 {
		in.MaxEventBytes = MaximumEventBytes
	}
	if in.MaxEvents == 0 {
		in.MaxEvents = MaximumEvents
	}
	if in.MaxTimeoutMS == 0 {
		in.MaxTimeoutMS = MaximumTimeoutMS
	}
	if in.MaxInputBytes < 1 || in.MaxInputBytes > MaximumInputBytes ||
		in.MaxMessages < 1 || in.MaxMessages > MaximumMessages ||
		in.MaxOutputBytes < 1 || in.MaxOutputBytes > MaximumOutputBytes ||
		in.MaxEventBytes < 1 || in.MaxEventBytes > MaximumEventBytes ||
		in.MaxEvents < 2 || in.MaxEvents > MaximumEvents ||
		in.MaxTimeoutMS < 1 || in.MaxTimeoutMS > MaximumTimeoutMS {
		return Limits{}, ErrInvalid
	}
	return in, nil
}

func validateEnrollment(in Enrollment) error {
	if in.SchemaVersion != SchemaVersion || !printable(in.Name, 128) || in.ProjectID < 1 ||
		!printable(in.HostID, 128) || !printable(in.ProjectRef, 128) || uuid.Validate(in.RuntimeID) != nil ||
		uuid.Validate(in.RuntimeGeneration) != nil || !stable(in.AccountKey, 128) || in.AttachmentRevision < 1 ||
		!stable(in.DispatchProfileID, 128) || !stable(in.DispatchProfileVersion, 128) ||
		len(in.Actors) == 0 || len(in.Actors) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, actor := range in.Actors {
		identity := Actor{Issuer: actor.Issuer, Subject: actor.Subject}
		key := actor.Issuer + "\x00" + actor.Subject
		if !identity.Valid() || actor.UserID < 1 || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	_, err := normalizeLimits(in.Limits)
	return err
}

func digestJSON(value any, domain string) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	hash := sha256Domain(domain, body)
	return hash, nil
}

func sameDigest(left, right []byte) bool { return bytes.Equal(left, right) }

func validNativeID(value string) bool { return printable(value, 256) }

func validErrorCode(value string) bool {
	switch value {
	case "execution_failed", "deadline_exceeded", "cancelled", "malformed_completion", "output_limit", "event_limit", "runtime_unavailable", "authority_revoked", "outcome_unknown":
		return true
	default:
		return false
	}
}

func validateEventShape(event Event) error {
	if event.Sequence < 1 || event.Sequence > MaximumEvents {
		return ErrInvalid
	}
	switch event.Kind {
	case "started":
		if event.Text != "" || !validNativeID(event.ThreadID) || !validNativeID(event.TurnID) || event.OutputSHA256 != "" || event.ErrorCode != "" {
			return ErrInvalid
		}
	case "assistant_delta":
		if event.Text == "" || !utf8.ValidString(event.Text) || !validNativeID(event.ThreadID) || !validNativeID(event.TurnID) || event.OutputSHA256 != "" || event.ErrorCode != "" {
			return ErrInvalid
		}
	case "completed":
		if event.Text != "" || !validNativeID(event.ThreadID) || !validNativeID(event.TurnID) || !lowerDigest.MatchString(event.OutputSHA256) || event.ErrorCode != "" {
			return ErrInvalid
		}
	case "failed":
		if event.Text != "" || event.OutputSHA256 != "" || !validErrorCode(event.ErrorCode) {
			return ErrInvalid
		}
	case "cancelled":
		if event.Text != "" || event.OutputSHA256 != "" || event.ErrorCode != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
