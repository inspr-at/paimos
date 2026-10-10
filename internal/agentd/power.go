// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"log/slog"
	"sync"
)

// Power provides temporary idle-sleep protection without changing host settings.
// A successful assertion must also end when the daemon process exits, including
// a crash. The caller releases it once; manual sleep and lid closure still apply.
type Power interface {
	PreventIdleSleep() (release func(), err error)
}

type systemPower struct{}

func (s *Supervisor) holdRunPower() func() {
	release, err := s.power.PreventIdleSleep()
	if err != nil || release == nil {
		// Ordinary admission remains unchanged when the OS cannot protect a run.
		// Report the failed protection honestly without exposing native errors.
		slog.Warn("idle-sleep protection unavailable for active agent run")
		return func() {}
	}
	var once sync.Once
	stop := context.AfterFunc(s.lifetime, func() { once.Do(release) })
	return func() {
		stop()
		once.Do(release)
	}
}
