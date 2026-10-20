// SPDX-License-Identifier: AGPL-3.0-only

// Package statusnames holds a workspace's own words for its statuses
// (AEON-1401). It is a display layer only: stored states, transitions, checks,
// the API, --json, webhooks and export keys keep the canonical values. A name
// is optional per UI locale; an empty one reads as the built-in name.
package statusnames

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MaxRunes bounds one display name, in characters after trimming.
const MaxRunes = 24

// Locales are the UI languages a name can be given for.
var Locales = []string{"en", "de"}

// Keys are the renamable statuses in workflow order, plus queued: lists show
// it like a status. Recurring is a marker and is not renamable.
var Keys = []string{"new", "backlog", "open", "blocked", "in_progress", "qa", "done", "delivered", "accepted", "cancelled", "archived", "queued"}

// Name is one status's display name per locale; empty uses the built-in.
type Name struct {
	EN string `json:"en,omitempty"`
	DE string `json:"de,omitempty"`
}

// Names maps a key from Keys to its display names. Only renamed keys appear.
type Names map[string]Name

var builtIn = map[string]Name{
	"new": {"New", "Neu"}, "backlog": {"Backlog", "Backlog"}, "open": {"Open", "Offen"},
	"blocked": {"Blocked", "Blockiert"}, "in_progress": {"In progress", "In Arbeit"}, "qa": {"QA", "QA"},
	"done": {"Done", "Erledigt"}, "delivered": {"Delivered", "Ausgeliefert"}, "accepted": {"Accepted", "Abgenommen"},
	"cancelled": {"Cancelled", "Abgebrochen"}, "archived": {"Archived", "Archiviert"}, "queued": {"Queued", "Eingereiht"},
}

func (n Name) get(locale string) string {
	if locale == "de" {
		return n.DE
	}
	return n.EN
}

func (n *Name) set(locale, value string) {
	if locale == "de" {
		n.DE = value
	} else {
		n.EN = value
	}
}

// BuiltIn is a key's shipped name in a locale ("" for an unknown key).
func BuiltIn(key, locale string) string { return builtIn[key].get(locale) }

// Key maps a stored state to its key, accepting the spellings imported data
// uses (in-progress, active, canceled). Unknown states return "".
func Key(state string) string {
	s := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(state)), "-", "_")
	switch s {
	case "active":
		return "in_progress"
	case "canceled":
		return "cancelled"
	}
	if _, ok := builtIn[s]; ok {
		return s
	}
	return ""
}

// Renamed is the workspace's own name for a state in a locale, or "" when the
// state keeps its built-in name there.
func (n Names) Renamed(state, locale string) string { return n[Key(state)].get(locale) }

// Display is the name a person reads for a state: the workspace's own name,
// else the built-in one. Unknown states read as given.
func (n Names) Display(state, locale string) string {
	key := Key(state)
	if key == "" {
		return state
	}
	if name := n[key].get(locale); name != "" {
		return name
	}
	return builtIn[key].get(locale)
}

// Load reads the workspace's names in the caller's tenant transaction. No row
// reads as no renames; revision 0 means nothing was ever saved.
func Load(ctx context.Context, tx pgx.Tx) (int64, Names, error) {
	var revision int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,names FROM status_names WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, Names{}, nil
	}
	if err != nil {
		return 0, nil, err
	}
	names := Names{}
	if err := json.Unmarshal(raw, &names); err != nil {
		return 0, nil, err
	}
	return revision, names, nil
}

// Only keeps the given statuses, for a page that names just those (the public
// portal and roadmap show In progress). Nil when none of them is renamed.
func (n Names) Only(keys ...string) Names {
	var out Names
	for _, key := range keys {
		if name, ok := n[key]; ok {
			if out == nil {
				out = Names{}
			}
			out[key] = name
		}
	}
	return out
}

// Error is a refused name, with the field it belongs to.
type Error struct {
	Code   string // status_name_unknown, status_name_invalid, status_name_too_long, status_name_taken
	Key    string
	Locale string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Field is the request path of the refused value, such as names.in_progress.en.
func (e *Error) Field() string {
	if e.Locale == "" {
		return "names." + e.Key
	}
	return "names." + e.Key + "." + e.Locale
}

// Normalize trims every name, drops empty ones (they read as the built-in)
// and names equal to their own built-in, then validates the result: known
// keys, at most MaxRunes characters, printable text, and no name another
// status reads as in the same locale (built-in or renamed, ignoring case).
func Normalize(in Names) (Names, error) {
	out := Names{}
	for key, name := range in {
		if _, ok := builtIn[key]; !ok {
			return nil, &Error{Code: "status_name_unknown", Key: key, Msg: fmt.Sprintf("%q is not a renamable status", key)}
		}
		var clean Name
		for _, locale := range Locales {
			value := strings.TrimSpace(name.get(locale))
			if value == "" || value == builtIn[key].get(locale) {
				continue
			}
			if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == utf8.RuneError }) {
				return nil, &Error{Code: "status_name_invalid", Key: key, Locale: locale, Msg: "Names are plain text."}
			}
			if utf8.RuneCountInString(value) > MaxRunes {
				return nil, &Error{Code: "status_name_too_long", Key: key, Locale: locale, Msg: fmt.Sprintf("At most %d characters.", MaxRunes)}
			}
			clean.set(locale, value)
		}
		if clean != (Name{}) {
			out[key] = clean
		}
	}
	for _, locale := range Locales {
		seen := map[string]string{}
		// Renamed keys first, so the refusal names the rename, not the built-in it collides with.
		ordered := make([]string, 0, len(Keys))
		for _, key := range Keys {
			if out[key].get(locale) != "" {
				ordered = append(ordered, key)
			}
		}
		for _, key := range Keys {
			if out[key].get(locale) == "" {
				ordered = append(ordered, key)
			}
		}
		for _, key := range ordered {
			name := out.Display(key, locale)
			fold := strings.ToLower(name)
			if other, taken := seen[fold]; taken {
				blame := key
				if out[key].get(locale) == "" {
					blame = other
				}
				return nil, &Error{Code: "status_name_taken", Key: blame, Locale: locale, Msg: fmt.Sprintf("“%s” is already the name of another status.", name)}
			}
			seen[fold] = key
		}
	}
	return out, nil
}
