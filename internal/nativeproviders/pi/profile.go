// SPDX-License-Identifier: AGPL-3.0-only

// Package pi implements the existing Pi RPC worker profile. Command success
// is never promoted to correlated chat delivery or a publishable final.
package pi

import (
	"strings"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeproviders/acp"
)

const Protocol = "pi-rpc/1"

func Matrix(q acp.Qualification) acp.MatrixRow {
	row := acp.MatrixRow{Harness: q.Harness, Binary: q.Binary, BusyInput: "unsupported", FinalCorrelation: "unsupported", Quota: "unsupported"}
	row.Capabilities = nc.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: q.QualificationDigest("aeon-pi-worker/1")}
	if q.Harness != "pi" || !q.Valid(Protocol) {
		return row
	}
	row.LivePersistence, row.GracefulExit = true, true
	row.BusyInput, row.FinalCorrelation = "next_step", "uncorrelated"
	row.Capabilities.Stop, row.Capabilities.EventsSupported = true, true
	row.Capabilities.Steer = "next_step"
	// Native RPC response ids correlate commands, but streamed message/agent
	// events do not reference the input. The public composer must stay closed.
	row.Capabilities.Input, row.Capabilities.ChatSupported, row.Capabilities.CorrelatedReceipts = false, false, false
	return row
}

func arguments(spec nc.LaunchSpec) ([]string, string, string, string, error) {
	provider, model, ok := strings.Cut(spec.Options.Model, "/")
	if !ok || !acp.ValidID(provider, 128) || !acp.ValidID(model, 128) || spec.Options.Effort == nil {
		return nil, "", "", "", acp.Refuse(nc.ReasonUnsupported)
	}
	effort := *spec.Options.Effort
	switch effort {
	case "off", "minimal", "low", "medium", "high", "xhigh":
	default:
		return nil, "", "", "", acp.Refuse(nc.ReasonUnsupported)
	}
	return []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--provider", provider, "--model", model, "--thinking", effort}, provider, model, effort, nil
}
