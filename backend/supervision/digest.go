// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/controlcontract"
	"github.com/inspr-at/paimos/backend/safetext"
)

var canonicalHeader = []byte{'P', 'A', 'I', 'M', 'O', 'S', '-', 'C', 'O', 'N', 'T', 'R', 'O', 'L', 0, 1}

type digestField struct {
	name  string
	value string
}

// canonicalDigest is deliberately not JSON. A versioned, byte-length framed
// encoding makes Unicode, empty values, and field boundaries unambiguous.
func canonicalDigest(operation string, fields ...digestField) [32]byte {
	all := append([]digestField{{name: "operation", value: operation}}, fields...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].name == all[j].name {
			return all[i].value < all[j].value
		}
		return all[i].name < all[j].name
	})
	var framed bytes.Buffer
	framed.Write(canonicalHeader)
	_ = binary.Write(&framed, binary.BigEndian, uint32(len(all)))
	for _, field := range all {
		writeFrame(&framed, field.name)
		writeFrame(&framed, field.value)
	}
	return sha256.Sum256(framed.Bytes())
}

func writeFrame(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len([]byte(value))))
	buffer.WriteString(value)
}

func operationKeyDigest(digest [32]byte) ([32]byte, error) {
	if digest == ([32]byte{}) {
		return [32]byte{}, domainError(ErrInvalid, CodeInvalidOperationKey)
	}
	return digest, nil
}

func digestHex(digest [32]byte) string { return fmt.Sprintf("%x", digest[:]) }

func digestActions(actions []Action) [32]byte {
	fields := make([]digestField, 0, len(actions))
	for i, action := range actions {
		fields = append(fields, digestField{name: fmt.Sprintf("action.%02d", i), value: string(action)})
	}
	return canonicalDigest("action-set", fields...)
}

func actionMembership() map[Action]bool {
	out := make(map[Action]bool, len(controlcontract.Actions()))
	for _, value := range controlcontract.Actions() {
		out[Action(value)] = true
	}
	return out
}

func enumMembership(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

var (
	knownActions          = actionMembership()
	knownStatuses         = enumMembership(controlcontract.CommandStatuses())
	knownOutcomes         = enumMembership(controlcontract.SafeOutcomes())
	knownReasons          = enumMembership(controlcontract.SafeReasons())
	knownTemplates        = enumMembership(controlcontract.ChallengeTemplates())
	knownInputKinds       = enumMembership(controlcontract.InputKinds())
	knownInputTemplates   = enumMembership(controlcontract.InputPromptTemplates())
	knownOperationKinds   = enumMembership(controlcontract.OperationKinds())
	knownRuntimeStates    = enumMembership(controlcontract.RuntimeStates())
	knownInputOptionCodes = enumMembership(controlcontract.InputOptionCodes())
)

func canonicalActions(actions []Action, runner bool) ([]Action, error) {
	requested := make(map[Action]bool, len(actions))
	for _, action := range actions {
		if !knownActions[action] {
			return nil, domainError(ErrInvalid, CodeInvalidAction)
		}
		if runner && action != "run.cancel.running" && action != "input.respond" && action != "run.pause" && action != "run.resume" {
			return nil, domainError(ErrInvalid, CodeInvalidAction)
		}
		requested[action] = true
	}
	if requested["run.pause"] != requested["run.resume"] {
		return nil, domainError(ErrInvalid, CodeInvalidAction)
	}
	ordered := make([]Action, 0, len(requested))
	for _, value := range controlcontract.Actions() {
		action := Action(value)
		if requested[action] {
			ordered = append(ordered, action)
		}
	}
	return ordered, nil
}

func intersectActions(left, right []Action) []Action {
	rightSet := make(map[Action]bool, len(right))
	for _, action := range right {
		rightSet[action] = true
	}
	out := make([]Action, 0, len(left))
	for _, action := range left {
		if rightSet[action] {
			out = append(out, action)
		}
	}
	if containsAction(out, "run.pause") != containsAction(out, "run.resume") {
		filtered := out[:0]
		for _, action := range out {
			if action != "run.pause" && action != "run.resume" {
				filtered = append(filtered, action)
			}
		}
		out = filtered
	}
	return out
}

func containsAction(actions []Action, wanted Action) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func validUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value && parsed.Version() == 4
}

func validateDevice(value string) error {
	if !utf8.ValidString(value) || len([]byte(value)) < 1 || len([]byte(value)) > 128 || strings.ContainsAny(value, "\x00\r\n") ||
		safetext.ContainsSecretLike(value) {
		return domainError(ErrInvalid, CodeInvalidDevice)
	}
	for i, r := range value {
		if i == 0 && !asciiAlphaNumeric(r) {
			return domainError(ErrInvalid, CodeInvalidDevice)
		}
		if !asciiAlphaNumeric(r) && !strings.ContainsRune("._:/-", r) {
			return domainError(ErrInvalid, CodeInvalidDevice)
		}
	}
	return nil
}

func asciiAlphaNumeric(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func intField(name string, value int64) digestField {
	return digestField{name: name, value: strconv.FormatInt(value, 10)}
}

func intValueField(name string, value int) digestField {
	return digestField{name: name, value: strconv.Itoa(value)}
}

func stringField(name, value string) digestField { return digestField{name: name, value: value} }

func actionFields(prefix string, actions []Action) []digestField {
	fields := make([]digestField, 0, len(actions))
	for index, action := range actions {
		fields = append(fields, stringField(fmt.Sprintf("%s.%02d", prefix, index), string(action)))
	}
	return fields
}
