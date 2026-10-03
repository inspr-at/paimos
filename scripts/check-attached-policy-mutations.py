#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""AEON-392 negative controls; run inside nix develop with the test DB set.

Each mutation must fail its security assertion, not merely fail compilation.
The exact original file bytes are restored before proceeding, even on failure.
No Git operation, daemon, production connection or durable payload is involved.
"""
from pathlib import Path
import os
import subprocess
import sys

# The approved remote Go lane can execute this as its test-binary wrapper.
# It first runs that binary unchanged, then runs the negative controls in the
# same disposable checkout/database. Normal invocation remains unchanged.
if len(sys.argv)>1:
    if sys.argv[1] != "--exec-test" or len(sys.argv)<3:
        raise SystemExit("usage: check-attached-policy-mutations.py [--exec-test TEST-BINARY [ARGS...]]")
    baseline=subprocess.run(sys.argv[2:])
    if baseline.returncode:
        raise SystemExit(baseline.returncode)

ROOT = Path(__file__).resolve().parents[1]
MUTATIONS = [
    ("owner predicate", "internal/attachedmsg/policy.go", " || p.ID != a.Owner", "", "./internal/agentpairing", "TestAttachedSenderAndTenantPolicy"),
    ("send generation", "internal/attachedmsg/policy.go", 'if in.Generation == "" || in.Generation != g.Binding.Generation {', "if false {", "./internal/agentpairing", "TestAttachedConsentAndTupleNegatives"),
    ("recipient tuple", "internal/attachedmsg/policy.go", "in.Recipient != a.Principal || ", "", "./internal/agentpairing", "TestAttachedSenderAndTenantPolicy"),
    ("consent digest", "internal/attachedmsg/grant.go", 'if g.State != "pending" || generation != g.Binding.Generation || digest != g.Digest {', 'if g.State != "pending" || generation != g.Binding.Generation {', "./internal/agentpairing", "TestAttachedConsentDowngradeAndDefaultCapability"),
    ("messaging signature", "internal/attachedmsg/grant.go", ' || !VerifyLocalConsent(a.LocalAuthPublicKey, g.Digest, nonce, LocalConsentReason(g), signature)', '', "./internal/agentpairing", "TestAttachedStrictLocalConsentAndNoLeaseRenewal"),
    ("payload tuple", "internal/attachedmsg/store.go", " || p.binding != b", "", "./internal/attachedmsg", "TestVolatileReservationCommitRollbackAndTuple"),
    ("payload replay", "internal/attachedmsg/store.go", "delete(s.entries, key)", "// mutation: retained payload", "./internal/attachedmsg", "TestVolatileReservationCommitRollbackAndTuple"),
    ("agent body clearing", "internal/inbox/attached.go", 'body = ""', '// mutation: retain notification body', "./internal/agentpairing", "TestAttachedAPIIdentityAndLegacyConsumers"),
    ("disabled explicit fallback", "internal/inbox/attached.go", 'if !m.attached.Enabled() && in.Generation != "" {\n\t\treturn true, msg, compat, attachedmsg.Fail(409, "feature_disabled")\n\t}', 'if !m.attached.Enabled() { return }', "./internal/agentpairing", "TestAttachedDisabledExplicitNotesNeverPersist"),
    ("live session lease", "internal/attachedmsg/grant.go", "WHERE session_id=$1::uuid AND state='active' AND lease_until>clock_timestamp()", "WHERE session_id=$1::uuid", "./internal/agentpairing", "TestAttachedInactiveRoutingPreservesInbox"),
    ("notification memory isolation", "internal/attachedmsg/store.go", 'if (p.grant == "") != (grant == "") {', 'if false {', "./internal/attachedmsg", "TestNotificationMemoryBudgetReservesOwnerCapacity"),
    ("notification rate isolation", "internal/attachedmsg/policy.go", 'if a.Mode == Notification {', 'if false {', "./internal/agentpairing", "TestAttachedNotificationFloodPreservesOwnerBudget"),
    ("take terminal row", "internal/attachedmsg/store.go", "AND attached_outcome IN ('queued','offered') AND message_deadline>clock_timestamp()", "AND message_deadline>clock_timestamp()", "./internal/agentpairing", "TestAttachedTakeRequiresLiveRowInTransaction"),
    ("take row deadline", "internal/attachedmsg/store.go", "AND message_deadline>clock_timestamp()", "", "./internal/agentpairing", "TestAttachedTakeRequiresLiveRowInTransaction"),
    ("event sender cast", "internal/db/migrations/1212_attached_event_privacy.sql", "(\"after\"->>'sender_principal_id')=ANY(aeon_current_principals()::text[])", "(\"after\"->>'sender_principal_id')::uuid=ANY(aeon_current_principals())", "./internal/agentpairing", "TestAttachedEventPrivacyMalformedSender"),
]

for label, file, before, after, package, test in MUTATIONS:
    path = ROOT / file
    original = path.read_bytes()
    source = original.decode()
    if source.count(before) != 1:
        raise SystemExit(f"{label}: expected exactly one mutation site")
    try:
        path.write_text(source.replace(before, after, 1))
        result = subprocess.run(
            ["go", "test", "-p", "2", "-count=1", package, "-run", "^" + test + "$"],
            cwd=ROOT, env={**os.environ, "GOMAXPROCS": "2"}, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180,
        )
        if result.returncode == 0 or f"--- FAIL: {test}" not in result.stdout:
            # Do not print request bodies or credentials from a failing fixture.
            raise SystemExit(f"{label}: SURVIVED or failed outside its assertion")
        print(f"KILLED: {label} ({test})", flush=True)
    finally:
        path.write_bytes(original)
print(f"All {len(MUTATIONS)} negative controls killed; original source restored.")
