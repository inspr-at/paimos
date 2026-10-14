// SPDX-License-Identifier: AGPL-3.0-only
//go:build (!darwin && !linux) || aeon_test_unsupported

package nativepty

import (
	"context"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type CommandPlan struct {
	Args, Environment []string
	Provider          string
	Require           func(context.Context, nativecontract.PTYLaunchSpec) error
}
type CommandBuilder interface {
	Prepare(context.Context, nativecontract.PTYLaunchSpec) (CommandPlan, error)
}
type ExecLauncher struct{ Builder CommandBuilder }

func (*ExecLauncher) OpenPTY(context.Context, nativecontract.PTYLaunchSpec) (nativecontract.PTYRuntime, error) {
	return nil, refuse(nativecontract.ReasonQualificationMissing)
}
