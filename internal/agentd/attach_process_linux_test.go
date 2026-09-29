// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package agentd

import (
	"strings"
	"testing"
)

func TestAttachLinuxStatSessionIdentity(t *testing.T) {
	fields := []string{"S", "20", "30", "20", "34816", "30", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "1", "0", "12345"}
	raw := func() []byte { return []byte("30 (helper (fixture)) " + strings.Join(fields, " ")) }
	start, parent, session, tty, err := linuxAttachStat(raw())
	if err != nil || start != "12345" || parent != 20 || session != 20 || !tty {
		t.Fatal("kernel stat lost parent, session or start identity")
	}
	for _, bad := range []string{"0", "-1", "unknown"} {
		fields[3] = bad
		if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
			t.Fatal("invalid session accepted")
		}
	}
	fields[3], fields[0] = "20", "Z"
	if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
		t.Fatal("dead session leader accepted")
	}
}
