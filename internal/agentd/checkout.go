// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
)

const (
	// CheckoutHeldCause is the only daemon-wait reason. The run panel maps it
	// to the checkout sentence. It is not a capacity-wait code.
	CheckoutHeldCause = "checkout_held_by_earlier_pairing"
	// CheckoutHandedOverText is the pair-time sentence after proof replaces an
	// earlier owner. A second pair sees the new owner and says nothing.
	CheckoutHandedOverText = "Handed over"
	// CheckoutStillHeldText is the pair-time sentence when the earlier pairing
	// cannot be proved exited, or when its lock is still held.
	CheckoutStillHeldText = "Still held by an earlier pairing. Release it in Settings › Computers."

	handoverLogName  = "handover.log"
	handoverLogBound = 32 << 10
)

// errCheckoutHeldByEarlierPairing marks a well-formed earlier owner that this
// computer cannot prove has exited. It is always joined with ErrProcessesUnconfirmed.
var errCheckoutHeldByEarlierPairing = errors.New("checkout held by an earlier pairing")

type checkoutOwnerKind int

const (
	checkoutOwnerEmpty checkoutOwnerKind = iota
	checkoutOwnerSame
	checkoutOwnerEarlier
	checkoutOwnerMalformed
)

func checkoutHeldError() error {
	return fmt.Errorf("checkout prior writer exit is unconfirmed: %w", errors.Join(ErrProcessesUnconfirmed, errCheckoutHeldByEarlierPairing))
}

func checkoutUnconfirmedError() error {
	return fmt.Errorf("checkout prior writer exit is unconfirmed: %w", ErrProcessesUnconfirmed)
}

// classifyCheckoutOwner reads a checkout owner record. A missing file is empty.
// A whitespace-tolerant empty array is empty. The same 4-tuple, including one
// that differs only by JSON spacing, is a rewrite. Anything else well-formed
// is an earlier pairing. Malformed bytes stay a generic refusal.
func classifyCheckoutOwner(previous, current []byte) (checkoutOwnerKind, string, string) {
	if previous == nil {
		return checkoutOwnerEmpty, "", ""
	}
	fields, ok := parseCheckoutOwner(previous)
	if !ok {
		return checkoutOwnerMalformed, "", ""
	}
	if fields == nil {
		return checkoutOwnerEmpty, "", ""
	}
	if bytes.Equal(bytes.TrimSpace(previous), bytes.TrimSpace(current)) {
		return checkoutOwnerSame, fields[2], fields[3]
	}
	currentFields, currentOK := parseCheckoutOwner(current)
	if currentOK && currentFields != nil && sameCheckoutOwner(fields, currentFields) {
		return checkoutOwnerSame, fields[2], fields[3]
	}
	return checkoutOwnerEarlier, fields[2], fields[3]
}

func sameCheckoutOwner(left, right []string) bool {
	return len(left) == 4 && len(right) == 4 && left[0] == right[0] && left[1] == right[1] && left[2] == right[2] && left[3] == right[3]
}

// parseCheckoutOwner returns a 4-tuple, or nil fields with ok for an empty
// array. ok is false for null, the wrong length, trailing JSON, or an unsafe path.
func parseCheckoutOwner(raw []byte) ([]string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var fields []string
	if err := dec.Decode(&fields); err != nil {
		return nil, false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if len(fields) == 0 {
		return nil, true
	}
	if len(fields) != 4 {
		return nil, false
	}
	for _, field := range fields {
		if field == "" || strings.TrimSpace(field) != field || strings.ContainsAny(field, "\x00\r\n") {
			return nil, false
		}
	}
	if !validCheckoutDaemon(fields[2]) || !validCheckoutState(fields[3]) {
		return nil, false
	}
	return fields, true
}

func validCheckoutDaemon(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 128 && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func validCheckoutState(path string) bool {
	return path != "" && path != "/" && len(path) <= 4096 && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func validCheckoutIdentity(tenant, principal, daemon, statePath string) bool {
	if tenant == "" || principal == "" || strings.TrimSpace(tenant) != tenant || strings.TrimSpace(principal) != principal {
		return false
	}
	if strings.ContainsAny(tenant, "\x00\r\n") || strings.ContainsAny(principal, "\x00\r\n") || len(tenant) > 128 || len(principal) > 128 {
		return false
	}
	return validCheckoutDaemon(daemon) && validCheckoutState(statePath)
}

// adoptEarlierCheckout returns nil when the caller may write its owner.
// A proved earlier pairing writes the audit line first. Refusal writes nothing.
func (s *Supervisor) adoptEarlierCheckout(store *agentsetup.Store, previous, owner []byte) error {
	kind, fromDaemon, fromState := classifyCheckoutOwner(previous, owner)
	switch kind {
	case checkoutOwnerEmpty, checkoutOwnerSame:
		return nil
	case checkoutOwnerMalformed:
		return checkoutUnconfirmedError()
	default:
		ok, err := priorJournalConfirmed(fromDaemon, fromState)
		if err != nil || !ok {
			return checkoutHeldError()
		}
		if err := writeHandoverAudit(store, fromDaemon, fromState, s.daemonID, s.state.Path()); err != nil {
			return checkoutHeldError()
		}
		return nil
	}
}

// priorJournalConfirmed is true only when the earlier pairing's journal file
// exists on this computer and every recorded run is confirmed exited. A missing
// journal, a checkpoint alone, a corrupt file, or an unconfirmed record is not
// proof. Reading never creates the directory or rewrites either file.
func priorJournalConfirmed(daemonID, statePath string) (bool, error) {
	if !validCheckoutDaemon(daemonID) || !validCheckoutState(statePath) {
		return false, nil
	}
	store, err := agentsetup.OpenStoreReadOnly(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer store.Close()
	prefix := "aeon-agentd-" + daemonID
	journal, err := store.Read(prefix+".journal", 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	checkpoint, err := store.Read(prefix+".checkpoint.json", 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		checkpoint = nil
	} else if err != nil {
		return false, err
	}
	records, err := localjournal.Decode(proofJournalConfig(), checkpoint, journal)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if !noLocalProcess(record) {
			return false, nil
		}
	}
	return true, nil
}

func proofJournalConfig() localjournal.Config[Record] {
	return localjournal.Config[Record]{
		Version: RecordSchemaVersion, Migrations: recordMigrations(), MaxBytes: 4 << 20, MaxRecords: 4096,
		Key: func(r Record) (string, error) {
			if r.RunID == "" {
				return "", errors.New("missing run")
			}
			return r.RunID, nil
		},
		Validate: func(r Record) error {
			if r.RunID == "" || r.Generation == "" || r.Sequence < 0 || len(r.Controls) > 256 {
				return errors.New("invalid AEON journal binding")
			}
			return validateLaunchRecord(r)
		},
	}
}

func writeHandoverAudit(store *agentsetup.Store, fromDaemon, fromState, toDaemon, toState string) error {
	if store == nil || !validCheckoutDaemon(fromDaemon) || !validCheckoutDaemon(toDaemon) || !validCheckoutState(fromState) || !validCheckoutState(toState) {
		return errors.New("handover audit is unsafe")
	}
	line := "handover from_daemon=" + fromDaemon + " from_state=" + fromState + " to_daemon=" + toDaemon + " to_state=" + toState + "\n"
	if len(line) > handoverLogBound {
		return errors.New("handover audit exceeds bound")
	}
	existing, err := store.Read(handoverLogName, handoverLogBound)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body := append(append([]byte(nil), existing...), line...)
	if len(body) > handoverLogBound {
		body = body[len(body)-handoverLogBound:]
		if i := bytes.IndexByte(body, '\n'); i >= 0 && i+1 < len(body) {
			body = body[i+1:]
		}
		if len(body) == 0 || len(body) > handoverLogBound {
			return errors.New("handover audit exceeds bound")
		}
	}
	return store.Write(handoverLogName, body, false)
}

// HandoverCheckout reports pair-time fence state for one workspace. It takes
// the fence only when an earlier owner's journal proves every recorded run has
// exited, and it writes that audit before the owner swap. A free lock is not
// proof. A busy lock stays held and writes nothing. Errors are unexpected; the
// caller maps them to CheckoutStillHeldText.
func HandoverCheckout(workspace, tenant, principal, daemon, statePath string) (string, error) {
	if !validCheckoutIdentity(tenant, principal, daemon, statePath) {
		return CheckoutStillHeldText, nil
	}
	storePath := filepath.Clean(filepath.Join(workspace, ".aeon-agentd"))
	store, err := agentsetup.OpenStore(storePath, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return CheckoutStillHeldText, nil
	}
	defer store.Close()
	if err := store.Lock(); err != nil {
		return CheckoutStillHeldText, nil
	}
	owner, err := json.Marshal([]string{tenant, principal, daemon, statePath})
	if err != nil || len(owner) > 4096 {
		return CheckoutStillHeldText, nil
	}
	raw, err := store.Read("owner.json", 4096)
	var previous []byte
	if errors.Is(err, os.ErrNotExist) {
		previous = nil
	} else if err != nil {
		return CheckoutStillHeldText, nil
	} else {
		previous = raw
	}
	kind, fromDaemon, fromState := classifyCheckoutOwner(previous, owner)
	switch kind {
	case checkoutOwnerEmpty, checkoutOwnerSame, checkoutOwnerMalformed:
		return "", nil
	default:
		ok, proveErr := priorJournalConfirmed(fromDaemon, fromState)
		if proveErr != nil || !ok {
			return CheckoutStillHeldText, nil
		}
		if err := writeHandoverAudit(store, fromDaemon, fromState, daemon, statePath); err != nil {
			return CheckoutStillHeldText, nil
		}
		if err := store.Write("owner.json", owner, false); err != nil {
			return CheckoutStillHeldText, nil
		}
		return CheckoutHandedOverText, nil
	}
}
