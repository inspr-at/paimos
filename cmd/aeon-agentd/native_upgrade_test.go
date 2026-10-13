// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativedrain"
)

type upgradeRuntime struct {
	flushErr error
	flushes  int
}

func (*upgradeRuntime) List(context.Context, int) ([]nativecontract.Binding, bool, error) {
	return nil, false, nil
}
func (*upgradeRuntime) QuiesceAtBoundary(context.Context, nativecontract.Binding) (nativecontract.WriterRecord, error) {
	panic("no owned writer")
}
func (*upgradeRuntime) Close(context.Context, nativecontract.WriterRecord) (nativecontract.ExitEvidence, error) {
	panic("no owned writer")
}
func (*upgradeRuntime) ConfirmExit(context.Context, nativecontract.WriterRecord, nativecontract.ExitEvidence) error {
	panic("no owned writer")
}
func (*upgradeRuntime) Checkpoint(context.Context, nativecontract.WriterRecord) error {
	panic("no owned writer")
}
func (r *upgradeRuntime) Flush(context.Context) error { r.flushes++; return r.flushErr }

// Risk: a readiness helper must expose a failed/partial drain and preserve the
// coordinator's pilot freeze instead of telling the operator to restart.
func TestNativeUpgradeReportsDeferredFlushAndPilotFreeze(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		approved       bool
		flushErr, want error
		reason         string
	}{
		{"pilot frozen", false, nil, nativedrain.ErrPilotFrozen, "pilot_frozen"},
		{"API unavailable", true, errors.New("synthetic outage; private diagnostic"), nativedrain.ErrFlushUnconfirmed, "flush_unconfirmed"},
		{"confirmed empty drain", true, nil, nil, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runtime := &upgradeRuntime{flushErr: scenario.flushErr}
			drain, err := nativedrain.New(nativedrain.Config{Runtime: runtime, CoordinatorApproved: scenario.approved})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = prepareNativeUpgrade(t.Context(), drain, &output)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("upgrade error=%v, want %v", err, scenario.want)
			}
			if scenario.want != nil {
				if !strings.Contains(output.String(), "native upgrade: deferred; reason="+scenario.reason) || strings.Contains(output.String(), "native upgrade: ready") || strings.Contains(output.String(), "private diagnostic") {
					t.Fatal("upgrade status hides failure or leaks backend diagnostics", output.String())
				}
			} else if !strings.Contains(output.String(), "native upgrade: ready; reason=; admission_closed=true; pending_launches=0; flush_confirmed=true") {
				t.Fatal("confirmed drain was not reported", output.String())
			}
			if !scenario.approved && runtime.flushes != 0 {
				t.Fatal("helper touched frozen pilot")
			}
		})
	}
}
