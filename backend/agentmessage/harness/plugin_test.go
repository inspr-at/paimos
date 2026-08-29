// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakePlugin struct {
	name, kind, maximum, mode string
	validate                  func(context.Context, string) error
	deliver                   func(context.Context, DeliverRequest) (DeliverResult, error)
}

func (p fakePlugin) Name() string         { return p.name }
func (p fakePlugin) Kind() string         { return p.kind }
func (p fakePlugin) MaximumLevel() string { return p.maximum }
func (p fakePlugin) Mode() string         { return p.mode }
func (p fakePlugin) ValidateTarget(ctx context.Context, ref string) error {
	if p.validate != nil {
		return p.validate(ctx, ref)
	}
	return nil
}
func (p fakePlugin) Deliver(ctx context.Context, req DeliverRequest) (DeliverResult, error) {
	if p.deliver != nil {
		return p.deliver(ctx, req)
	}
	return DeliverResult{EffectiveLevel: LevelSimple}, nil
}

func TestRegisterBuiltinsAndLookupByName(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]struct{ kind, maximum, mode string }{
		AdapterCodex:          {KindCodexThread, LevelSteer, ModeLocal},
		AdapterAgentdCodex:    {KindAgentdSession, LevelSteer, ModeLocal},
		AdapterAgentdClaude:   {KindAgentdSession, LevelSteer, ModeLocal},
		AdapterClaudeResume:   {KindClaudeSession, LevelSimple, ModeLocal},
		AdapterClaudeChannel:  {KindClaudeSession, LevelSimple, ModeLocal},
		AdapterGrokBotRoutine: {KindHTTPSWebhook, LevelSimple, ModeServer},
	} {
		plugin, err := registry.Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if plugin.Kind() != want.kind || plugin.MaximumLevel() != want.maximum || plugin.Mode() != want.mode {
			t.Fatalf("Lookup(%q)=%s/%s/%s want %s/%s/%s", name, plugin.Kind(), plugin.MaximumLevel(), plugin.Mode(), want.kind, want.maximum, want.mode)
		}
	}
	alias, err := registry.Resolve("claude")
	if err != nil || alias.Name() != AdapterClaudeResume {
		t.Fatalf("Resolve(claude)=%v,%v", alias, err)
	}
}

func TestRegistryDeliversSimpleThroughLocalPlugin(t *testing.T) {
	registry := NewRegistry()
	called := false
	plugin := fakePlugin{name: "fake_local", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	plugin.deliver = func(_ context.Context, req DeliverRequest) (DeliverResult, error) {
		called = req.Level == LevelSimple && req.Body == "framed body" && req.TargetRef == "opaque-ref"
		return DeliverResult{EffectiveLevel: LevelSimple}, nil
	}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Deliver(context.Background(), plugin.Name(), DeliverRequest{Level: LevelSimple, Body: "framed body", TargetRef: "opaque-ref"})
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.EffectiveLevel != LevelSimple || result.FallbackReason != "" {
		t.Fatalf("called=%v result=%+v", called, result)
	}
}

func TestRegistrySimplePluginSteerFailsClosed(t *testing.T) {
	registry := NewRegistry()
	plugin := fakePlugin{name: "simple_only", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	plugin.deliver = func(context.Context, DeliverRequest) (DeliverResult, error) {
		return DeliverResult{EffectiveLevel: LevelSimple}, nil
	}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Deliver(context.Background(), plugin.Name(), DeliverRequest{Level: LevelSteer, TargetRef: "opaque-ref"})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLevel != LevelSimple || result.FallbackReason != "unsupported" {
		t.Fatalf("steer result=%+v", result)
	}
}

func TestRegistryAcceptsTransportFallbackReason(t *testing.T) {
	registry := NewRegistry()
	plugin := fakePlugin{name: "transport_fallback", kind: "fake_ref", maximum: LevelSteer, mode: ModeLocal}
	plugin.deliver = func(context.Context, DeliverRequest) (DeliverResult, error) {
		return DeliverResult{EffectiveLevel: LevelSimple, FallbackReason: "transport_error"}, nil
	}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Deliver(context.Background(), plugin.Name(), DeliverRequest{Level: LevelSteer, TargetRef: "opaque-ref"})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLevel != LevelSimple || result.FallbackReason != "transport_error" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRegistryRejectsMalformedHostilePlugins(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(fakePlugin{kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}); err == nil {
		t.Fatal("empty plugin name registered")
	}
	var nilPlugin *fakePlugin
	if err := registry.Register(nilPlugin); err == nil {
		t.Fatal("typed nil plugin registered")
	}
	if _, err := registry.Lookup("missing"); !errors.Is(err, ErrUnsupported) || ErrorCode(err) != CodeUnsupported {
		t.Fatalf("unknown lookup err=%v", err)
	}
	base := fakePlugin{name: "base", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	if err := registry.Register(base); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterAlias("claimed_alias", base.Name()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakePlugin{name: "claimed_alias", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}); err == nil {
		t.Fatal("plugin registered over an existing alias")
	}

	escalator := fakePlugin{name: "escalator", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	escalator.deliver = func(context.Context, DeliverRequest) (DeliverResult, error) {
		return DeliverResult{EffectiveLevel: LevelSteer}, nil
	}
	if err := registry.Register(escalator); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Deliver(context.Background(), escalator.Name(), DeliverRequest{Level: LevelSteer, TargetRef: "opaque-ref"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("steer escalation err=%v want UNSUPPORTED", err)
	}

	if err := registry.ValidateBinding(context.Background(), escalator.Name(), "wrong_kind", LevelSimple, "opaque-ref"); ErrorCode(err) != CodeTargetKindInvalid {
		t.Fatalf("kind mismatch err=%v code=%q", err, ErrorCode(err))
	}
}

func TestRegistryDoesNotEchoTargetRefsInErrors(t *testing.T) {
	const secretRef = "fixture-secret-ref-never-echo"
	registry := NewRegistry()
	leaky := fakePlugin{name: "leaky", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	leaky.validate = func(_ context.Context, ref string) error {
		return fmt.Errorf("hostile validator included %s", ref)
	}
	if err := registry.Register(leaky); err != nil {
		t.Fatal(err)
	}
	err := registry.ValidateBinding(context.Background(), leaky.Name(), leaky.Kind(), LevelSimple, secretRef)
	if err == nil || strings.Contains(err.Error(), secretRef) {
		t.Fatalf("validation error leaked target ref: %v", err)
	}

	leakyDelivery := fakePlugin{name: "leaky_delivery", kind: "fake_ref", maximum: LevelSimple, mode: ModeLocal}
	leakyDelivery.deliver = func(_ context.Context, req DeliverRequest) (DeliverResult, error) {
		return DeliverResult{}, fmt.Errorf("hostile delivery included %s", req.TargetRef)
	}
	if err := registry.Register(leakyDelivery); err != nil {
		t.Fatal(err)
	}
	_, err = registry.Deliver(context.Background(), leakyDelivery.Name(), DeliverRequest{Level: LevelSimple, TargetRef: secretRef})
	if err == nil || strings.Contains(err.Error(), secretRef) {
		t.Fatalf("delivery error leaked target ref: %v", err)
	}
}

type fakeSecretPlugin struct {
	fakePlugin
	header, prefix string
	validateSecret func(string) error
}

func (p fakeSecretPlugin) SecretHeader() (string, string) { return p.header, p.prefix }
func (p fakeSecretPlugin) ValidateSecret(secret string) error {
	if p.validateSecret != nil {
		return p.validateSecret(secret)
	}
	return nil
}

func TestRegistrySecretHeaderCapabilityFailsClosed(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	name, prefix, required, err := registry.SecretHeader(AdapterGrokBotRoutine)
	if err != nil || !required || name != "Authorization" || prefix != "Bearer " {
		t.Fatalf("grok secret header=%q prefix=%q required=%v err=%v", name, prefix, required, err)
	}
	for _, adapter := range []string{AdapterCodex, AdapterClaudeResume, AdapterClaudeChannel} {
		if _, _, required, err := registry.SecretHeader(adapter); err != nil || required {
			t.Fatalf("%s must not require a sender secret: required=%v err=%v", adapter, required, err)
		}
		if err := registry.ValidateSecret(adapter, "crsr_must_not_be_stored"); ErrorCode(err) != CodeTargetSecretUnsupported {
			t.Fatalf("%s accepted a sender secret: %v", adapter, err)
		}
		if err := registry.ValidateSecret(adapter, ""); err != nil {
			t.Fatalf("%s without a secret must be valid: %v", adapter, err)
		}
	}
	if err := registry.ValidateSecret(AdapterGrokBotRoutine, ""); ErrorCode(err) != CodeTargetSecretRequired {
		t.Fatalf("grok without a sender key err=%v", err)
	}
	for _, bad := range []string{
		"Bearer crsr_prebuilt_header_value",
		"Authorization: Bearer crsr_prebuilt_header_value",
		"crsr_with a space",
		"crsr_tab\tseparated",
		"short",
		strings.Repeat("a", 513),
		"crsr_non_ascii_ü",
	} {
		err := registry.ValidateSecret(AdapterGrokBotRoutine, bad)
		if ErrorCode(err) != CodeTargetSecretInvalid {
			t.Fatalf("secret %q err=%v want %s", bad, err, CodeTargetSecretInvalid)
		}
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("validation error echoed the secret: %v", err)
		}
	}
	if err := registry.ValidateSecret(AdapterGrokBotRoutine, "crsr_fixture_sender_key_0001"); err != nil {
		t.Fatalf("raw sender key rejected: %v", err)
	}
	if _, _, _, err := registry.SecretHeader("missing"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown adapter err=%v", err)
	}
}

func TestRegistryDoesNotEchoSecretsFromHostilePlugins(t *testing.T) {
	const secret = "crsr_fixture_secret_never_echo"
	registry := NewRegistry()
	leaky := fakeSecretPlugin{
		fakePlugin: fakePlugin{name: "leaky_secret", kind: "fake_ref", maximum: LevelSimple, mode: ModeServer},
		header:     "X-Fake-Key",
		validateSecret: func(value string) error {
			return fmt.Errorf("hostile validator included %s", value)
		},
	}
	if err := registry.Register(leaky); err != nil {
		t.Fatal(err)
	}
	name, prefix, required, err := registry.SecretHeader(leaky.Name())
	if err != nil || !required || name != "X-Fake-Key" || prefix != "" {
		t.Fatalf("capability lookup through the registry envelope failed: %q %q %v %v", name, prefix, required, err)
	}
	err = registry.ValidateSecret(leaky.Name(), secret)
	if err == nil || strings.Contains(err.Error(), secret) || ErrorCode(err) != CodeTargetSecretInvalid {
		t.Fatalf("hostile validator error leaked or lost its code: %v", err)
	}
	empty := fakeSecretPlugin{fakePlugin: fakePlugin{name: "empty_header", kind: "fake_ref", maximum: LevelSimple, mode: ModeServer}}
	if err := registry.Register(empty); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := registry.SecretHeader(empty.Name()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("empty header name must fail closed: %v", err)
	}
}
