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
