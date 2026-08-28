// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package harness defines the versioned delivery socket used by PAIMOS
// harness adapters. Core message policy and persistence remain outside this
// package; plugins own target shape and the documented handoff primitive.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const InterfaceVersion = 1

const (
	LevelSimple = "simple"
	LevelSteer  = "steer"

	ModeLocal  = "local"
	ModeServer = "server"
)

const (
	CodeUnsupported             = "agent_message_adapter_unsupported"
	CodeTargetKindInvalid       = "agent_message_target_kind_invalid"
	CodeTargetLevelInvalid      = "agent_message_target_level_invalid"
	CodeTargetRefInvalid        = "agent_message_target_ref_invalid"
	CodeTargetSecretRequired    = "agent_message_target_secret_required"
	CodeTargetSecretInvalid     = "agent_message_target_secret_invalid"
	CodeTargetSecretUnsupported = "agent_message_target_secret_unsupported"
	CodeWebhookInvalid          = "agent_message_target_webhook_invalid"
	CodeWebhookHostDenied       = "agent_message_target_webhook_host_denied"
	CodeWebhookDNSFailed        = "agent_message_target_webhook_dns_failed"
	CodeWebhookAddressDenied    = "agent_message_target_webhook_address_denied"
)

// MaxSecretBytes bounds any per-target sender secret before a plugin sees it.
const MaxSecretBytes = 1024

var ErrUnsupported = errors.New("UNSUPPORTED")

// Error is a stable, non-secret plugin error. Plugins must never include a
// target reference in Message.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == CodeUnsupported {
		return "UNSUPPORTED: " + e.Message
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e.Code == CodeUnsupported {
		return ErrUnsupported
	}
	return nil
}

// ErrorCode returns a stable plugin error code, if present.
func ErrorCode(err error) string {
	var pluginErr *Error
	if errors.As(err, &pluginErr) {
		return pluginErr.Code
	}
	return ""
}

// UnavailableError reports that a documented local primitive cannot be used.
type UnavailableError struct{ Message string }

func (e *UnavailableError) Error() string { return e.Message }

type DeliverRequest struct {
	Level         string
	Body          string
	TargetRef     string
	Stdout        io.Writer
	Stderr        io.Writer
	ClientVersion string
}

type DeliverResult struct {
	EffectiveLevel string
	FallbackReason string
	Primitive      string
}

type Plugin interface {
	Name() string
	Kind() string
	MaximumLevel() string
	Mode() string
	ValidateTarget(context.Context, string) error
	Deliver(context.Context, DeliverRequest) (DeliverResult, error)
}

// SecretHeaderPlugin is an optional capability for server-mode plugins whose
// vendor authenticates every wake request with a receiver-owned sender secret
// carried in exactly one HTTP header (Grok Bot routine webhooks:
// `Authorization: Bearer <sender key>`). Core stores that secret as
// domain-separated ciphertext, never lists, logs, or discloses it through
// listen, and renders it only onto the outbound request. A plugin that
// implements this interface requires the secret at registration: a target
// whose wake cannot authenticate is not a usable target and fails closed.
type SecretHeaderPlugin interface {
	Plugin
	// SecretHeader returns the request header name and the value prefix that
	// precedes the raw secret, for example ("Authorization", "Bearer ").
	SecretHeader() (name, prefix string)
	// ValidateSecret checks the raw secret shape. It must never echo the value.
	ValidateSecret(secret string) error
}

type registeredPlugin struct {
	plugin                    Plugin
	name, kind, maximum, mode string
}

func (p registeredPlugin) Name() string         { return p.name }
func (p registeredPlugin) Kind() string         { return p.kind }
func (p registeredPlugin) MaximumLevel() string { return p.maximum }
func (p registeredPlugin) Mode() string         { return p.mode }
func (p registeredPlugin) ValidateTarget(ctx context.Context, ref string) error {
	return p.plugin.ValidateTarget(ctx, ref)
}
func (p registeredPlugin) Deliver(ctx context.Context, req DeliverRequest) (DeliverResult, error) {
	return p.plugin.Deliver(ctx, req)
}

type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
	aliases map[string]string
}

func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]Plugin), aliases: make(map[string]string)}
}

// Register adds one canonical plugin. Names and kinds are deliberately
// conservative so malformed adapters cannot become durable registry keys.
func (r *Registry) Register(plugin Plugin) error {
	if plugin == nil || isNilPlugin(plugin) {
		return fmt.Errorf("register harness plugin: plugin is nil")
	}
	name, kind, maximum, mode := plugin.Name(), plugin.Kind(), plugin.MaximumLevel(), plugin.Mode()
	if !validKey(name) {
		return fmt.Errorf("register harness plugin: name must be a lowercase registry key")
	}
	if !validKey(kind) {
		return fmt.Errorf("register harness plugin %q: kind must be a lowercase registry key", name)
	}
	if maximum != LevelSimple && maximum != LevelSteer {
		return fmt.Errorf("register harness plugin %q: maximum level must be simple or steer", name)
	}
	if mode != ModeLocal && mode != ModeServer {
		return fmt.Errorf("register harness plugin %q: mode must be local or server", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("register harness plugin %q: already registered", name)
	}
	if _, exists := r.aliases[name]; exists {
		return fmt.Errorf("register harness plugin %q: conflicts with an alias", name)
	}
	r.plugins[name] = registeredPlugin{plugin: plugin, name: name, kind: kind, maximum: maximum, mode: mode}
	return nil
}

func isNilPlugin(plugin Plugin) bool {
	value := reflect.ValueOf(plugin)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validKey(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ToLower(value) != value || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && r == '_') {
			continue
		}
		return false
	}
	return true
}

func (r *Registry) RegisterAlias(alias, name string) error {
	if !validKey(alias) || !validKey(name) {
		return fmt.Errorf("register harness alias: alias and name must be lowercase registry keys")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; !exists {
		return fmt.Errorf("register harness alias %q: plugin is not registered", alias)
	}
	if _, exists := r.plugins[alias]; exists {
		return fmt.Errorf("register harness alias %q: conflicts with a plugin", alias)
	}
	if _, exists := r.aliases[alias]; exists {
		return fmt.Errorf("register harness alias %q: already registered", alias)
	}
	r.aliases[alias] = name
	return nil
}

func (r *Registry) Lookup(name string) (Plugin, error) {
	r.mu.RLock()
	plugin := r.plugins[name]
	r.mu.RUnlock()
	if plugin == nil {
		return nil, &Error{Code: CodeUnsupported, Message: "harness adapter is not registered"}
	}
	return plugin, nil
}

// Resolve accepts either a canonical plugin name or a CLI alias.
func (r *Registry) Resolve(name string) (Plugin, error) {
	r.mu.RLock()
	canonical := name
	if mapped := r.aliases[name]; mapped != "" {
		canonical = mapped
	}
	plugin := r.plugins[canonical]
	r.mu.RUnlock()
	if plugin == nil {
		return nil, &Error{Code: CodeUnsupported, Message: "harness adapter is not registered"}
	}
	return plugin, nil
}

func (r *Registry) ValidateTarget(ctx context.Context, name, ref string) error {
	plugin, err := r.Lookup(name)
	if err != nil {
		return err
	}
	return sanitizeTargetError(plugin.ValidateTarget(ctx, ref), ref)
}

// ValidateBinding checks the plugin-owned durable target fields without
// exposing the target reference in errors.
func (r *Registry) ValidateBinding(ctx context.Context, name, kind, maximumLevel, ref string) error {
	plugin, err := r.Lookup(name)
	if err != nil {
		return err
	}
	if kind != plugin.Kind() {
		return &Error{Code: CodeTargetKindInvalid, Message: "target_kind does not match the harness adapter"}
	}
	if maximumLevel != LevelSimple && maximumLevel != LevelSteer {
		return &Error{Code: CodeTargetLevelInvalid, Message: "target maximum_level is unsupported"}
	}
	if maximumLevel == LevelSteer && plugin.MaximumLevel() != LevelSteer {
		return &Error{Code: CodeTargetLevelInvalid, Message: "target maximum_level exceeds the harness adapter capability"}
	}
	return sanitizeTargetError(plugin.ValidateTarget(ctx, ref), ref)
}

// Deliver invokes a plugin and rejects invalid outcomes, including any attempt
// to escalate a simple request or a simple-only plugin to steer.
func (r *Registry) Deliver(ctx context.Context, name string, req DeliverRequest) (DeliverResult, error) {
	plugin, err := r.Lookup(name)
	if err != nil {
		return DeliverResult{}, err
	}
	if req.Level != LevelSimple && req.Level != LevelSteer {
		return DeliverResult{}, &Error{Code: CodeUnsupported, Message: "delivery level is unsupported"}
	}
	if req.TargetRef != "" {
		if err := sanitizeTargetError(plugin.ValidateTarget(ctx, req.TargetRef), req.TargetRef); err != nil {
			return DeliverResult{}, err
		}
	}
	result, err := plugin.Deliver(ctx, req)
	if err != nil {
		return DeliverResult{}, sanitizeTargetError(err, req.TargetRef)
	}
	if result.EffectiveLevel != LevelSimple && result.EffectiveLevel != LevelSteer {
		return DeliverResult{}, &Error{Code: CodeUnsupported, Message: "adapter returned an unsupported effective level"}
	}
	if result.EffectiveLevel == LevelSteer && (req.Level != LevelSteer || plugin.MaximumLevel() != LevelSteer) {
		return DeliverResult{}, &Error{Code: CodeUnsupported, Message: "adapter attempted an unsupported steer escalation"}
	}
	if plugin.MaximumLevel() == LevelSimple && req.Level == LevelSteer && result.FallbackReason == "" {
		result.FallbackReason = "unsupported"
	}
	validFallback := map[string]bool{"": true, "unsupported": true, "policy_capped": true, "idle": true, "not_steerable": true, "target_missing": true, "transport_error": true}
	if !validFallback[result.FallbackReason] {
		return DeliverResult{}, &Error{Code: CodeUnsupported, Message: "adapter returned an unsupported fallback reason"}
	}
	return result, nil
}

// secretCapability unwraps the registry envelope and reports whether the
// underlying plugin sends a per-target sender secret.
func secretCapability(plugin Plugin) (SecretHeaderPlugin, bool) {
	if wrapped, ok := plugin.(registeredPlugin); ok {
		plugin = wrapped.plugin
	}
	capable, ok := plugin.(SecretHeaderPlugin)
	return capable, ok
}

// SecretHeader reports whether the named plugin requires a per-target sender
// secret and, if so, which header and value prefix carry it on the wire.
func (r *Registry) SecretHeader(name string) (headerName, prefix string, required bool, err error) {
	plugin, err := r.Lookup(name)
	if err != nil {
		return "", "", false, err
	}
	capable, ok := secretCapability(plugin)
	if !ok {
		return "", "", false, nil
	}
	headerName, prefix = capable.SecretHeader()
	if strings.TrimSpace(headerName) == "" {
		return "", "", false, &Error{Code: CodeUnsupported, Message: "harness adapter declared an empty secret header"}
	}
	return headerName, prefix, true, nil
}

// ValidateSecret enforces the plugin's sender-secret policy without echoing
// the value: a plugin with the capability requires a well-formed secret, and
// every other plugin rejects one so a credential can never be stored for an
// adapter that has no header to put it in.
func (r *Registry) ValidateSecret(name, secret string) error {
	plugin, err := r.Lookup(name)
	if err != nil {
		return err
	}
	capable, ok := secretCapability(plugin)
	if !ok {
		if secret != "" {
			return &Error{Code: CodeTargetSecretUnsupported, Message: "this harness adapter sends no sender secret; omit target_secret"}
		}
		return nil
	}
	if secret == "" {
		return &Error{Code: CodeTargetSecretRequired, Message: "this harness adapter requires the receiver-owned sender secret (CLI: --target-key-file)"}
	}
	if len([]byte(secret)) > MaxSecretBytes || !utf8.ValidString(secret) || strings.ContainsAny(secret, "\x00\r\n") {
		return &Error{Code: CodeTargetSecretInvalid, Message: "target_secret must be one line of at most 1024 safe UTF-8 bytes"}
	}
	err = capable.ValidateSecret(secret)
	if err == nil {
		return nil
	}
	code := ErrorCode(err)
	if code == "" {
		code = CodeTargetSecretInvalid
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		message = "target secret is invalid"
	}
	return &Error{Code: code, Message: message}
}

func sanitizeTargetError(err error, ref string) error {
	if err == nil || ref == "" || !strings.Contains(err.Error(), ref) {
		return err
	}
	code := ErrorCode(err)
	if code == "" {
		code = CodeTargetRefInvalid
	}
	return &Error{Code: code, Message: "target reference is invalid"}
}

// Names returns canonical plugin names matching the requested mode and kind.
// Empty filters match every registered plugin.
func (r *Registry) Names(mode, kind string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name, plugin := range r.plugins {
		if (mode == "" || plugin.Mode() == mode) && (kind == "" || plugin.Kind() == kind) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

var defaultRegistry = NewRegistry()

// RegisterBuiltins installs the shipped adapters into a fresh registry. It is
// primarily useful to isolated callers and tests; each built-in also registers
// itself with the process registry from its own file.
func RegisterBuiltins(registry *Registry) error {
	for _, plugin := range []Plugin{CodexPlugin{}, ClaudePlugin{}, ClaudePlugin{Channel: true}, GrokRoutinePlugin{}} {
		if err := registry.Register(plugin); err != nil {
			return err
		}
	}
	return registry.RegisterAlias("claude", AdapterClaudeResume)
}

func Register(plugin Plugin) error           { return defaultRegistry.Register(plugin) }
func RegisterAlias(alias, name string) error { return defaultRegistry.RegisterAlias(alias, name) }
func Lookup(name string) (Plugin, error)     { return defaultRegistry.Lookup(name) }
func Resolve(name string) (Plugin, error)    { return defaultRegistry.Resolve(name) }
func ValidateTarget(ctx context.Context, name, ref string) error {
	return defaultRegistry.ValidateTarget(ctx, name, ref)
}
func ValidateBinding(ctx context.Context, name, kind, maximumLevel, ref string) error {
	return defaultRegistry.ValidateBinding(ctx, name, kind, maximumLevel, ref)
}
func SecretHeader(name string) (headerName, prefix string, required bool, err error) {
	return defaultRegistry.SecretHeader(name)
}
func ValidateSecret(name, secret string) error { return defaultRegistry.ValidateSecret(name, secret) }
func Deliver(ctx context.Context, name string, req DeliverRequest) (DeliverResult, error) {
	return defaultRegistry.Deliver(ctx, name, req)
}
func Names(mode, kind string) []string { return defaultRegistry.Names(mode, kind) }
