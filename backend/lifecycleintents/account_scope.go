// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"bytes"
	"encoding/json"
	"io"
)

const AccountScopeSchemaV3 = 3

// AccountScope binds one closed account class to the named keys and catalog
// profiles that class may actually start. Keys stay distinct from class,
// harness, generation, host and profile identity.
type AccountScope struct {
	AccountLabel string          `json:"account_label"`
	Accounts     []AccountChoice `json:"accounts,omitempty"`
	Profiles     []Profile       `json:"profiles"`
}

type registrationLegacyJSON struct {
	Generation    string          `json:"generation"`
	Host          string          `json:"host"`
	AccountLabel  string          `json:"account_label"`
	Accounts      []AccountChoice `json:"accounts,omitempty"`
	Workspaces    []Workspace     `json:"workspaces"`
	Profiles      []Profile       `json:"profiles"`
	SchemaVersion int             `json:"schema_version,omitempty"`
}

type registrationV3JSON struct {
	Generation    string         `json:"generation"`
	Host          string         `json:"host"`
	Workspaces    []Workspace    `json:"workspaces"`
	AccountScopes []AccountScope `json:"account_scopes"`
	SchemaVersion int            `json:"schema_version"`
}

type runtimeLegacyJSON struct {
	ID            string              `json:"id"`
	ProjectID     int64               `json:"project_id"`
	Generation    string              `json:"generation"`
	MachineID     string              `json:"machine_id"`
	AccountLabel  string              `json:"account_label"`
	Accounts      []AccountChoice     `json:"accounts,omitempty"`
	Workspaces    []Workspace         `json:"workspaces"`
	Profiles      []Profile           `json:"profiles"`
	ExpiresAt     string              `json:"expires_at"`
	Sessions      []SessionProjection `json:"sessions"`
	SchemaVersion int                 `json:"schema_version,omitempty"`
}

type runtimeV3JSON struct {
	ID            string              `json:"id"`
	ProjectID     int64               `json:"project_id"`
	Generation    string              `json:"generation"`
	MachineID     string              `json:"machine_id"`
	Workspaces    []Workspace         `json:"workspaces"`
	AccountScopes []AccountScope      `json:"account_scopes"`
	ExpiresAt     string              `json:"expires_at"`
	Sessions      []SessionProjection `json:"sessions"`
	SchemaVersion int                 `json:"schema_version"`
}

func (r Registration) MarshalJSON() ([]byte, error) {
	if r.SchemaVersion == AccountScopeSchemaV3 {
		return json.Marshal(registrationV3JSON{
			Generation: r.Generation, Host: r.Host, Workspaces: r.Workspaces,
			AccountScopes: r.AccountScopes, SchemaVersion: AccountScopeSchemaV3,
		})
	}
	return json.Marshal(registrationLegacyJSON{
		Generation: r.Generation, Host: r.Host, AccountLabel: r.AccountLabel,
		Accounts: r.Accounts, Workspaces: r.Workspaces, Profiles: r.Profiles,
		SchemaVersion: r.SchemaVersion,
	})
}

func (r Runtime) MarshalJSON() ([]byte, error) {
	sessions := r.Sessions
	if sessions == nil {
		sessions = []SessionProjection{}
	}
	if r.SchemaVersion == AccountScopeSchemaV3 {
		return json.Marshal(runtimeV3JSON{
			ID: r.ID, ProjectID: r.ProjectID, Generation: r.Generation, MachineID: r.MachineID,
			Workspaces: r.Workspaces, AccountScopes: r.AccountScopes, ExpiresAt: r.ExpiresAt,
			Sessions: sessions, SchemaVersion: AccountScopeSchemaV3,
		})
	}
	return json.Marshal(runtimeLegacyJSON{
		ID: r.ID, ProjectID: r.ProjectID, Generation: r.Generation, MachineID: r.MachineID,
		AccountLabel: r.AccountLabel, Accounts: r.Accounts, Workspaces: r.Workspaces,
		Profiles: r.Profiles, ExpiresAt: r.ExpiresAt, Sessions: sessions, SchemaVersion: r.SchemaVersion,
	})
}

func (r *Registration) UnmarshalJSON(data []byte) error {
	raw, err := decodeClosedObject(data)
	if err != nil {
		return err
	}
	if leftover(raw, "generation", "host", "account_label", "accounts", "workspaces", "profiles", "schema_version", "account_scopes") {
		return ErrInvalid
	}
	version, err := optionalInt(raw, "schema_version")
	if err != nil {
		return err
	}
	generation, err := requiredString(raw, "generation")
	if err != nil {
		return err
	}
	host, err := requiredString(raw, "host")
	if err != nil {
		return err
	}
	workspaces, err := requiredWorkspaces(raw)
	if err != nil {
		return err
	}
	if version == AccountScopeSchemaV3 {
		if fieldPresent(raw, "account_label") || fieldPresent(raw, "accounts") || fieldPresent(raw, "profiles") {
			return ErrInvalid
		}
		scopes, err := requiredAccountScopes(raw)
		if err != nil {
			return err
		}
		*r = Registration{Generation: generation, Host: host, Workspaces: workspaces, AccountScopes: scopes, SchemaVersion: AccountScopeSchemaV3}
		return nil
	}
	if fieldPresent(raw, "account_scopes") {
		return ErrInvalid
	}
	label, err := requiredString(raw, "account_label")
	if err != nil {
		return err
	}
	profiles, err := requiredProfiles(raw)
	if err != nil {
		return err
	}
	accounts, err := optionalAccounts(raw)
	if err != nil {
		return err
	}
	*r = Registration{Generation: generation, Host: host, AccountLabel: label, Accounts: accounts, Workspaces: workspaces, Profiles: profiles, SchemaVersion: version}
	return nil
}

func decodeClosedObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return nil, ErrInvalid
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalid
	}
	return raw, nil
}

func leftover(raw map[string]json.RawMessage, names ...string) bool {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for name := range raw {
		if !allowed[name] {
			return true
		}
	}
	return false
}

func fieldPresent(raw map[string]json.RawMessage, name string) bool {
	_, ok := raw[name]
	return ok
}

func jsonNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func requiredString(raw map[string]json.RawMessage, name string) (string, error) {
	value, ok := raw[name]
	if !ok || jsonNull(value) {
		return "", ErrInvalid
	}
	var out string
	if json.Unmarshal(value, &out) != nil {
		return "", ErrInvalid
	}
	return out, nil
}

func optionalInt(raw map[string]json.RawMessage, name string) (int, error) {
	value, ok := raw[name]
	if !ok {
		return 0, nil
	}
	if jsonNull(value) {
		return 0, ErrInvalid
	}
	var out int
	if json.Unmarshal(value, &out) != nil {
		return 0, ErrInvalid
	}
	return out, nil
}

func requiredWorkspaces(raw map[string]json.RawMessage) ([]Workspace, error) {
	value, ok := raw["workspaces"]
	if !ok || jsonNull(value) {
		return nil, ErrInvalid
	}
	var out []Workspace
	if json.Unmarshal(value, &out) != nil || out == nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func requiredProfiles(raw map[string]json.RawMessage) ([]Profile, error) {
	value, ok := raw["profiles"]
	if !ok || jsonNull(value) {
		return nil, ErrInvalid
	}
	var out []Profile
	if json.Unmarshal(value, &out) != nil || out == nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func requiredAccountScopes(raw map[string]json.RawMessage) ([]AccountScope, error) {
	value, ok := raw["account_scopes"]
	if !ok || jsonNull(value) {
		return nil, ErrInvalid
	}
	var out []AccountScope
	if json.Unmarshal(value, &out) != nil || out == nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func optionalAccounts(raw map[string]json.RawMessage) ([]AccountChoice, error) {
	value, ok := raw["accounts"]
	if !ok {
		return nil, nil
	}
	if jsonNull(value) {
		return nil, ErrInvalid
	}
	var out []AccountChoice
	if json.Unmarshal(value, &out) != nil || out == nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func (s *AccountScope) UnmarshalJSON(data []byte) error {
	raw, err := decodeClosedObject(data)
	if err != nil {
		return err
	}
	if leftover(raw, "account_label", "accounts", "profiles") {
		return ErrInvalid
	}
	label, err := requiredString(raw, "account_label")
	if err != nil {
		return err
	}
	profiles, err := requiredProfiles(raw)
	if err != nil {
		return err
	}
	accounts, err := optionalAccounts(raw)
	if err != nil {
		return err
	}
	*s = AccountScope{AccountLabel: label, Accounts: accounts, Profiles: profiles}
	return nil
}

func classAllowsNamedAccounts(class string) bool {
	switch class {
	case "chatgpt", "api_key", "pi_context", "cursor_context":
		return true
	}
	return false
}

func classRequiresNamedAccounts(class string) bool {
	return class == "pi_context" || class == "cursor_context"
}

func profileHarnessForClass(class string) string {
	switch class {
	case "chatgpt", "api_key":
		return "codex"
	case "claude_ai_max", "claude_ai_pro", "claude_ai_team", "claude_ai_enterprise", "console":
		return "claude"
	case "pi_context":
		return "pi"
	case "cursor_context":
		return "cursor"
	}
	return ""
}

func (s AccountScope) hasProfile(id, version string) bool {
	for _, profile := range s.Profiles {
		if profile.ID == id && profile.Version == version {
			return true
		}
	}
	return false
}

func (s AccountScope) matchKey(key string) bool {
	if len(s.Accounts) == 0 {
		return key == ""
	}
	if key == "" {
		return false
	}
	for _, choice := range s.Accounts {
		if choice.Key == key {
			return true
		}
	}
	return false
}

func (r Registration) Scopes() []AccountScope {
	if r.SchemaVersion == AccountScopeSchemaV3 {
		return r.AccountScopes
	}
	return []AccountScope{{AccountLabel: r.AccountLabel, Accounts: r.Accounts, Profiles: r.Profiles}}
}

func (r Runtime) registration() Registration {
	return Registration{
		Generation: r.Generation, Host: r.MachineID, AccountLabel: r.AccountLabel, Accounts: r.Accounts,
		Workspaces: r.Workspaces, Profiles: r.Profiles, SchemaVersion: r.SchemaVersion, AccountScopes: r.AccountScopes,
	}
}

// MatchScope reports whether class, key and optional profile resolve to exactly
// one advertised scope. Repair may omit the profile; start/readiness must not.
func (r Registration) MatchScope(class, key, profileID, version string, requireProfile bool) bool {
	matches := 0
	for _, scope := range r.Scopes() {
		if scope.AccountLabel != class || !scope.matchKey(key) {
			continue
		}
		if requireProfile && !scope.hasProfile(profileID, version) {
			continue
		}
		matches++
	}
	return matches == 1
}

func (r Runtime) MatchScope(class, key, profileID, version string, requireProfile bool) bool {
	return r.registration().MatchScope(class, key, profileID, version, requireProfile)
}

// MatchRepair reports whether class uniquely identifies a scope and an optional
// named key belongs to that scope. Empty key remains valid on named v1/v2
// runtimes because repair does not start a model turn.
func (r Registration) MatchRepair(class, key string) bool {
	matches := 0
	for _, scope := range r.Scopes() {
		if scope.AccountLabel != class {
			continue
		}
		if key != "" && !scope.matchKey(key) {
			continue
		}
		matches++
	}
	return matches == 1
}

func (r Runtime) MatchRepair(class, key string) bool {
	return r.registration().MatchRepair(class, key)
}

func ValidateAccountScopes(in Registration) error {
	if in.SchemaVersion != AccountScopeSchemaV3 {
		if len(in.AccountScopes) != 0 {
			return ErrInvalid
		}
		return nil
	}
	if in.AccountLabel != "" || in.Accounts != nil || in.Profiles != nil || len(in.AccountScopes) == 0 || len(in.AccountScopes) > 16 {
		return ErrInvalid
	}
	classes, keys, labels := map[string]bool{}, map[string]bool{}, map[string]bool{}
	accounts, profiles := 0, 0
	for _, scope := range in.AccountScopes {
		if !validAccount(scope.AccountLabel) || classes[scope.AccountLabel] || scope.Profiles == nil || len(scope.Profiles) == 0 {
			return ErrInvalid
		}
		classes[scope.AccountLabel] = true
		if classRequiresNamedAccounts(scope.AccountLabel) && len(scope.Accounts) == 0 {
			return ErrInvalid
		}
		if !classAllowsNamedAccounts(scope.AccountLabel) && len(scope.Accounts) > 0 {
			return ErrInvalid
		}
		seenProfiles := map[string]bool{}
		for _, profile := range scope.Profiles {
			identity := profile.ID + "@" + profile.Version
			if seenProfiles[identity] {
				return ErrInvalid
			}
			seenProfiles[identity] = true
			resolved, err := resolveProfile(profile.ID, profile.Version)
			if err != nil {
				return ErrUnavailable
			}
			if resolved.Harness != profileHarnessForClass(scope.AccountLabel) {
				return ErrInvalid
			}
			profiles++
		}
		for _, choice := range scope.Accounts {
			if !validAccountKey(choice.Key) || !ValidAccountChoiceLabel(choice.Label) || keys[choice.Key] || labels[choice.Label] {
				return ErrInvalid
			}
			if AccountChoiceDimensionCollisionCause(choice.Key, in.Generation, in.Host, scope.AccountLabel, scope.Profiles) != "" {
				return ErrInvalid
			}
			if AccountChoiceDimensionCollisionCause(choice.Label, in.Generation, in.Host, scope.AccountLabel, scope.Profiles) != "" {
				return ErrInvalid
			}
			keys[choice.Key] = true
			labels[choice.Label] = true
			accounts++
		}
	}
	if accounts > maxAdvertisedAccounts || profiles > 16 {
		return ErrInvalid
	}
	for _, scope := range in.AccountScopes {
		for _, choice := range scope.Accounts {
			if choice.Label != choice.Key && keys[choice.Label] {
				return ErrInvalid
			}
		}
	}
	return nil
}

func ValidateRegistration(in Registration) error { return validateRegistration(in) }
