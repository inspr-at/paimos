// SPDX-License-Identifier: AGPL-3.0-only

package acp

import (
	"strings"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	nc "github.com/inspr-at/paimos/internal/nativecontract"
)

const Protocol = "acp/1"
const codecRevision = "aeon-acp-worker/1"

func supported(harness string) bool {
	return harness == "cursor" || harness == "grok" || harness == "gemini" || harness == "opencode"
}

// Matrix has one row per exact local pin; unknown pins/providers stay closed.
// These claims describe codec fixtures. G_PROVIDER_PINS must separately prove
// that the owner enforces these policy digests on the actual OS/vendor build.
func Matrix(q Qualification) MatrixRow {
	row := MatrixRow{Harness: q.Harness, Binary: q.Binary, BusyInput: "unsupported", FinalCorrelation: "unsupported", Quota: "unsupported"}
	row.Capabilities = nc.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: q.QualificationDigest(codecRevision)}
	if !supported(q.Harness) || !q.Valid(Protocol) {
		return row
	}
	row.LivePersistence, row.GracefulExit = true, true
	row.BusyInput, row.FinalCorrelation = "queued", "prompt_response_only"
	row.Capabilities.Input, row.Capabilities.ChatSupported = true, true
	row.Capabilities.Steer = "queued"
	row.Capabilities.Interrupt = q.Harness != "grok"
	row.Capabilities.InterruptSupported = row.Capabilities.Interrupt
	row.Capabilities.Stop, row.Capabilities.EventsSupported, row.Capabilities.CorrelatedReceipts = true, true, true
	return row
}

func arguments(q Qualification, spec nc.LaunchSpec) ([]string, error) {
	switch q.Harness {
	case "cursor":
		if spec.Options.Effort != nil {
			return nil, Refuse(nc.ReasonUnsupported)
		}
		return []string{"--model", spec.Options.Model, "acp"}, nil
	case "grok":
		// No always-approve/dontAsk/permission bypass is added by this codec.
		if spec.Options.Effort != nil {
			return nil, Refuse(nc.ReasonUnsupported)
		}
		return []string{"agent", "stdio"}, nil
	case "gemini":
		if spec.Options.Effort == nil {
			return nil, Refuse(nc.ReasonUnsupported)
		}
		if _, err := harnesslaunch.GeminiBudgetForModel(spec.Options.Model, *spec.Options.Effort); err != nil {
			return nil, Refuse(nc.ReasonUnsupported)
		}
		configured := false
		for _, e := range spec.Profile.Environment {
			configured = configured || strings.HasPrefix(e, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=")
		}
		if !configured {
			return nil, Refuse(nc.ReasonQualificationMissing)
		}
		return []string{"--acp", "--model", spec.Options.Model}, nil
	case "opencode":
		provider, model, ok := strings.Cut(spec.Options.Model, "/")
		if !ok || !ValidID(provider, 128) || !ValidID(model, 128) {
			return nil, Refuse(nc.ReasonUnsupported)
		}
		return []string{"acp"}, nil
	default:
		return nil, Refuse(nc.ReasonUnsupported)
	}
}
