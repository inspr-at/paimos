// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"testing"
	"time"
)

func TestStampSessionEtaBoundary(t *testing.T) {
	reported := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	interval := 10 * time.Minute
	withReport := func() *Session {
		at := reported
		return &Session{EtaReportedAt: &at}
	}

	fresh := withReport()
	stampSessionEta(fresh, interval, reported.Add(2*interval))
	if fresh.EtaStale {
		t.Fatal("exactly two intervals is still fresh")
	}

	stale := withReport()
	stampSessionEta(stale, interval, reported.Add(2*interval+time.Microsecond))
	if !stale.EtaStale {
		t.Fatal("past two intervals is stale")
	}

	stopped := withReport()
	stop := reported
	stopped.StoppedAt = &stop
	stampSessionEta(stopped, interval, reported.Add(24*time.Hour))
	if stopped.EtaStale {
		t.Fatal("a stopped session is not stale")
	}

	none := &Session{}
	stampSessionEta(none, interval, reported.Add(time.Hour))
	if none.EtaStale {
		t.Fatal("a session with no report is not stale")
	}
	stampSessionEta(nil, interval, reported)
}
