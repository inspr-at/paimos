// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"testing"

	"github.com/inspr-at/paimos/backend/pirpctest"
)

const piRPCHelperTest = "TestPiRPCChildProcess"

// TestPiRPCChildProcess hosts the test-only fake pi --mode rpc child.
func TestPiRPCChildProcess(t *testing.T) {
	if !pirpctest.Run() {
		return
	}
}
