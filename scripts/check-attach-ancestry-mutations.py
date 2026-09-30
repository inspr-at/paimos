#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Remove attach ancestry guards and require real failing tests on macOS.

Each source file is restored byte-for-byte, including on failure. Build errors
and missing tests do not count as killing a mutation. No Git operations.
"""
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
PROCESS = "internal/agentd/attach_process.go"
UNSAFE = "TestAttachDarwinAncestryRefusesUnsafeChains/"
MUTATIONS = [
    ("helper pinned start", PROCESS,
     "!sameAttachProcessIdentity(current, peer)",
     "(false && !sameAttachProcessIdentity(current, peer))",
     "TestAttachDarwinAncestryPinsHelperAndTarget/30$"),
    ("target pinned start", PROCESS,
     "!sameAttachProcessIdentity(selected, target)",
     "(false && !sameAttachProcessIdentity(selected, target))",
     "TestAttachDarwinAncestryPinsHelperAndTarget/40$"),
    ("helper TTY", PROCESS, "!current.TTY", "(false && !current.TTY)",
     UNSAFE + "lost_tty$"),
    ("self-led session", PROCESS, "current.Session == current.PID",
     "(false && current.Session == current.PID)", UNSAFE + "self_session$"),
    ("leader session binding", PROCESS, "leader.Session != current.Session",
     "(false && leader.Session != current.Session)", UNSAFE + "wrong_leader_session$"),
    ("helper ancestry", PROCESS,
     "!independentAttachAncestry(current.PID, target.PID, current.UID, observe, seen)",
     "(false && !independentAttachAncestry(current.PID, target.PID, current.UID, observe, seen))",
     UNSAFE + "helper_descends_from_target$"),
    ("leader ancestry", PROCESS,
     "!independentAttachAncestry(leader.PID, target.PID, current.UID, observe, seen)",
     "(false && !independentAttachAncestry(leader.PID, target.PID, current.UID, observe, seen))",
     UNSAFE + "leader_descends_from_target$"),
    ("target ancestry", PROCESS,
     "!independentAttachAncestry(target.PID, current.PID, current.UID, observe, seen)",
     "(false && !independentAttachAncestry(target.PID, current.PID, current.UID, observe, seen))",
     UNSAFE + "target_descends_from_helper$"),
    ("ancestor UID boundary", PROCESS, "p.UID != uid && p.UID != 0",
     "(false && p.UID != uid && p.UID != 0)",
     UNSAFE + "(foreign_helper_ancestor|foreign_target_ancestor|foreign_leader)$"),
    ("ancestor start required", PROCESS, 'p.Started == ""',
     '(false && p.Started == "")', UNSAFE + "missing_start$"),
    ("PID reuse during traversal", PROCESS,
     "exists && before != identity", "false && exists && before != identity",
     "TestAttachDarwinAncestryRefusesPIDReuse/(30|40|10)$"),
    ("whole graph recheck", PROCESS,
     "attachProcessIdentity(after) != before", "(false && attachProcessIdentity(after) != before)",
     "TestAttachDarwinAncestryRefusesPIDReuse/50$"),
    ("preview independence", "internal/agentd/attach.go",
     "!independentAttachPeer(peer, observed, m.ancestry) || !attachwatch.Within",
     "(false && !independentAttachPeer(peer, observed, m.ancestry)) || !attachwatch.Within",
     "TestAttachModesIdentityAndLease"),
    ("confirmation independence", "internal/agentd/attach.go",
     "!independentAttachPeer(peer, observed, m.ancestry) || s.tail != nil && s.tail.check() != nil || in.Digest",
     "(false && !independentAttachPeer(peer, observed, m.ancestry)) || s.tail != nil && s.tail.check() != nil || in.Digest",
     "TestAttachRechecksAncestryAndSessionOnConfirmAndEveryPoll/confirm/"),
    ("last check before upload", "internal/agentd/attach.go",
     "!independentAttachPeer(peer, observed, m.ancestry) || s.tail != nil && s.tail.check() != nil {",
     "(false && !independentAttachPeer(peer, observed, m.ancestry)) || s.tail != nil && s.tail.check() != nil {",
     "TestAttachDarwinAncestryRecheckedBeforeUpload$"),
]


def run(pattern):
    return subprocess.run(
        ["go", "test", "-p", "2", "-count=1", "-run", pattern, "./internal/agentd"],
        cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, timeout=120,
    )


def main():
    if sys.platform != "darwin":
        raise SystemExit("Run the Darwin ancestry mutations on a macOS test machine.")
    os.environ["GOMAXPROCS"] = "2"
    baseline = run("TestAttachDarwin|TestAttachModesIdentityAndLease|TestAttachRechecksAncestry")
    if baseline.returncode != 0:
        print(baseline.stdout[-4000:])
        raise SystemExit("Unmodified ancestry regressions must pass first.")
    for name, filename, before, after, pattern in MUTATIONS:
        path = ROOT / filename
        original = path.read_bytes()
        source = original.decode()
        if source.count(before) != 1:
            raise SystemExit(f"{name}: mutation target is not unique")
        try:
            path.write_text(source.replace(before, after))
            result = run(pattern)
            if result.returncode == 0 or "--- FAIL:" not in result.stdout:
                print(result.stdout[-4000:])
                raise SystemExit(f"{name}: regression did not kill the mutation")
            print(f"KILLED: {name}", flush=True)
        finally:
            path.write_bytes(original)
    print(f"All {len(MUTATIONS)} mutations killed; sources restored.")


if __name__ == "__main__":
    main()
