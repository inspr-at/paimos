#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""AEON-393 security negative controls, run only on the disposable test runner.

Each must fail its named assertion, never merely compilation. Exact original
bytes are restored after every control, even on test failure or interruption.
"""
from pathlib import Path
import os
import subprocess

ROOT = Path(__file__).resolve().parents[1]
REPLAY = '''
 // MUTATION: managed-style replay of an open delivery before another claim.
 if in.Operation == "message_offer" {
  var prior attachedmsg.Offer
  err := m.in(r.Context(),p.TenantID,func(tx pgx.Tx) error {
   return tx.QueryRow(r.Context(),`SELECT d.id::text,m.id::text,m.body FROM harness_deliveries d JOIN inbox_messages m ON m.tenant_id=d.tenant_id AND m.id=d.message_id WHERE d.session_id=$1::uuid AND d.completed_at IS NULL AND d.released_at IS NULL ORDER BY d.cursor LIMIT 1`,b.SessionID).Scan(&prior.DeliveryID,&prior.MessageID,&prior.Body)
  })
  if err==nil {reply(w,attachedmsg.Exchange{State:"offered",Offer:&prior});return}
 }
'''
MUTATIONS = [
    ("managed automatic replay", "internal/agentpairing/attached_messages.go", "\tvar offer *attachedmsg.Offer", REPLAY + "\tvar offer *attachedmsg.Offer", "TestAttachedDeliverySingleAttemptAndHonestReceipt"),
    ("nonce verification", "internal/attachedmsg/delivery.go", "subtle.ConstantTimeCompare([]byte(a.digest), []byte(nonceDigest(b, a.grant, r.DeliveryID, r.MessageID, r.Nonce, epoch))) != 1", "subtle.ConstantTimeCompare([]byte(a.digest), []byte(nonceDigest(b, a.grant, r.DeliveryID, r.MessageID, r.Nonce, epoch))) < 0", "TestAttachedDeliveryNonceAndTupleFences/nonce"),
    ("binding epoch CAS", "internal/attachedmsg/delivery.go", "r.Epoch != epoch || ", "", "TestAttachedDeliveryNonceAndTupleFences/epoch"),
    ("settlement authority", "internal/attachedmsg/delivery.go", "_, authority := s.ValidateGrant(ctx, tx, b)", "var authority error", "TestAttachedDeliveryFiveMinuteWaitAndReplayExpiry"),
    ("receipt deadline", "internal/attachedmsg/delivery.go", "if authority != nil || a.expired || !live || !a.released {", "if authority != nil || !live || !a.released {", "TestAttachedDeliveryExpiryWithoutSweepAndFrozenEpoch"),
]

if not os.environ.get("AEON_TEST_DATABASE_URL"):
    raise SystemExit("Disposable AEON_TEST_DATABASE_URL required")
baseline_tests = "|".join(sorted({m[4].split("/")[0] for m in MUTATIONS}))
baseline = subprocess.run(
    ["go", "test", "-p", "2", "-count=1", "./internal/agentpairing", "-run", "^(" + baseline_tests + ")$"],
    cwd=ROOT, env={**os.environ, "GOMAXPROCS": "2"}, text=True,
    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=240,
)
if baseline.returncode != 0:
    raise SystemExit("Unmutated assertion baseline failed; no mutation evidence recorded")
print("PASS: unmutated assertion baseline", flush=True)
for label, file, before, after, test in MUTATIONS:
    path = ROOT / file
    original = path.read_bytes()
    source = original.decode()
    if source.count(before) != 1:
        raise SystemExit(f"{label}: expected exactly one mutation site")
    try:
        path.write_text(source.replace(before, after, 1))
        result = subprocess.run(
            ["go", "test", "-p", "2", "-count=1", "./internal/agentpairing", "-run", "^" + test + "$"],
            cwd=ROOT, env={**os.environ, "GOMAXPROCS": "2"}, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=240,
        )
        top = test.split("/")[0]
        if result.returncode == 0 or f"--- FAIL: {top}" not in result.stdout:
            raise SystemExit(f"{label}: SURVIVED or failed outside its assertion")
        print(f"KILLED: {label} ({test})", flush=True)
    finally:
        path.write_bytes(original)
print("All five negative controls killed; exact original bytes restored.")
