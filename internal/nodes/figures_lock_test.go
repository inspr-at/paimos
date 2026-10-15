// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"testing"
	"time"
	"unsafe"
)

// Risk: work lifecycle copies the module so one attempt can record into a
// deferred batch. That copy must keep the figures lock. A forked mutex guards
// the same map, so a figures read and the lifecycle copy can corrupt the cache.
func TestLifecycleModuleCopySharesFiguresLock(t *testing.T) {
	m := New(nil, SQLWriter{}).(*Module)
	now := time.Unix(1_700_000_000, 0).UTC()
	published := &projectFigures{ViewKey: "shared-cohort"}
	m.figures.put("scope", 4, now, published)
	batch := &deferredWorkEvents{}
	copied := *m
	copied.events = batch
	if figuresLockAddr(m) == 0 || figuresLockAddr(m) != figuresLockAddr(&copied) {
		t.Fatal("lifecycle module copy forked the figures lock")
	}
	if got := copied.figures.get("scope", 4, now); got != published {
		t.Fatal("lifecycle module copy forked the figures cache")
	}
	newer := &projectFigures{ViewKey: "from-copy"}
	copied.figures.put("scope", 5, now, newer)
	if got := m.figures.get("scope", 5, now); got != newer {
		t.Fatal("publication through the lifecycle copy missed the module cache")
	}
	recorded, ok := copied.events.(*deferredWorkEvents)
	if !ok || recorded != batch {
		t.Fatal("lifecycle module copy must record into the deferred batch")
	}
	if _, still := m.events.(SQLWriter); !still {
		t.Fatal("lifecycle module copy replaced the shared event writer")
	}
}

func figuresLockAddr(m *Module) uintptr {
	return uintptr(unsafe.Pointer(&m.figures.mu))
}
