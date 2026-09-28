// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import "testing"

func TestWatchRelayNoHistoryIsolationAndBounds(t *testing.T) {
	var b watchRelay
	b.publish("tenant/session-a", "old history")
	a, closeA, ok := b.subscribe("tenant/session-a")
	if !ok {
		t.Fatal("subscribe")
	}
	defer closeA()
	c, closeC, _ := b.subscribe("tenant/session-b")
	defer closeC()
	foreign, closeF, _ := b.subscribe("other/session-a")
	defer closeF()
	select {
	case <-a:
		t.Fatal("replayed history")
	default:
	}
	b.publish("tenant/session-a", "new")
	if <-a != "new" {
		t.Fatal("lost text")
	}
	for _, ch := range []chan string{c, foreign} {
		select {
		case <-ch:
			t.Fatal("cross-session or cross-tenant relay")
		default:
		}
	}
	b.publish("tenant/session-a", "one")
	b.publish("tenant/session-a", "overflow")
	<-a
	if _, open := <-a; open {
		t.Fatal("slow reader not closed")
	}
	if b.count != 2 {
		t.Fatal("viewer quota leaked")
	}
	for _, bad := range []string{"\x1b[31mred", "\u202etext", "\x00", "\xff"} {
		if inertText(bad) {
			t.Fatal("active/control text accepted")
		}
	}
	if !inertText("<script>inert</script>\nplain\ttext") {
		t.Fatal("plain text rejected")
	}
}
