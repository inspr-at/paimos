// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"testing"
)

func TestVerificationApprovalURLUsesExistingAccountWithoutMutation(t *testing.T) {
	e, api, _, opts, executor := engineFixture(t)
	approveFixture(t, e, api, opts)
	before, err := e.Store.Read(snapshotName, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	creates, calls := api.createCount, len(executor.calls)
	for range 2 {
		link, err := e.VerificationApprovalURL(testAccount)
		if err != nil || link != "https://aeon.example.test/agents?verify_account="+testAccount {
			t.Fatal("incorrect owner approval link", err)
		}
	}
	for _, id := range []string{otherAccount, "bad?account"} {
		if _, err := e.VerificationApprovalURL(id); err == nil {
			t.Fatal("unknown account produced approval link")
		}
	}
	after, err := e.Store.Read(snapshotName, 1<<20)
	if err != nil || !bytes.Equal(before, after) || api.createCount != creates || len(executor.calls) != calls {
		t.Fatal("verify changed pairing or local processes")
	}
	s, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	s.Removed = map[string]bool{testAccount: true}
	if err := e.save(s, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VerificationApprovalURL(testAccount); err == nil {
		t.Fatal("removed account produced approval link")
	}
}
