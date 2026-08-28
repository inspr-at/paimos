// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"testing"
)

// thirdAdapterStub is intentionally vendor-neutral testdata. OpenCode and Pi
// are not shipped by PAI-829; a future adapter can live in one file like this
// and register without editing the message bus or ledger.
type thirdAdapterStub struct{}

func (thirdAdapterStub) Name() string                                 { return "third_adapter" }
func (thirdAdapterStub) Kind() string                                 { return "third_ref" }
func (thirdAdapterStub) MaximumLevel() string                         { return LevelSimple }
func (thirdAdapterStub) Mode() string                                 { return ModeLocal }
func (thirdAdapterStub) ValidateTarget(context.Context, string) error { return nil }
func (thirdAdapterStub) Deliver(context.Context, DeliverRequest) (DeliverResult, error) {
	return DeliverResult{EffectiveLevel: LevelSimple}, nil
}

func TestThirdAdapterRegistersWithoutCoreChanges(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(thirdAdapterStub{}); err != nil {
		t.Fatal(err)
	}
	plugin, err := registry.Lookup("third_adapter")
	if err != nil || plugin.Kind() != "third_ref" {
		t.Fatalf("third adapter lookup=%v err=%v", plugin, err)
	}
}
