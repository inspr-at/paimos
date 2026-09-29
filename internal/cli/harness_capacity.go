// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

type heartbeatCapacity struct{ Account, Source, File, Phase string }

func (o *heartbeatCapacity) flags(fs *flagSet) {
	fs.string(&o.Phase, "capacity-phase", 0, "start, update or end (run-heartbeat infers lifecycle phases)")
	fs.string(&o.Account, "capacity-account", 0, "enrolled account UUID owned by this agent")
	fs.string(&o.Source, "capacity-source", 0, "quota stream parser: codex or claude")
	fs.string(&o.File, "capacity-file", 0, "physical path to a Codex rollout/app-server or Claude stream JSONL file")
}
func (o heartbeatCapacity) validate() error {
	if o.Account == "" && o.Source == "" && o.File == "" && o.Phase == "" {
		return nil
	}
	if o.Phase != "" && o.Phase != "start" && o.Phase != "update" && o.Phase != "end" {
		return usagef("invalid --capacity-phase")
	}
	if !validUUID(o.Account) || (o.Source != "codex" && o.Source != "claude") || o.File == "" {
		return usagef("capacity reporting requires --capacity-account, --capacity-source and --capacity-file")
	}
	return nil
}
func (rt *runtime) reportHeartbeatCapacity(ctx context.Context, o heartbeatCapacity) error {
	_, err := rt.reportCapacityReading(ctx, o)
	return err
}
func (rt *runtime) reportCapacityReading(ctx context.Context, o heartbeatCapacity) (bool, error) {
	if err := o.validate(); err != nil {
		return false, err
	}
	if o.File == "" {
		return false, nil
	}
	readings, err := capacity.ReadFile(o.File, o.Source, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if len(readings) == 0 {
		return false, nil
	}
	phase := o.Phase
	if phase == "" {
		phase = "update"
	}
	for i := range readings {
		readings[i].Phase = phase
	}
	err = rt.harnessDoCtx(ctx, http.MethodPost, "/api/agent-accounts/"+o.Account+"/readings", "", struct {
		Readings []capacity.Reading `json:"readings"`
	}{readings}, nil)
	return err == nil, err
}
