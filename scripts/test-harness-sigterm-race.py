#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Go -exec wrapper: reproduce AEON-617, then stress the fixed test binary.

Use through the approved remote-test.sh runner with:
  -json -race -count=30 -timeout=30m
  -exec 'python3 ../../scripts/test-harness-sigterm-race.py' ./internal/cli
The overlay changes only temporary copies; no tracked source is mutated.
"""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile


def main():
    if len(sys.argv) < 2:
        raise SystemExit("expected a Go test binary and its arguments")
    # Go starts -exec wrappers in the package directory, not the repo root.
    root = Path(__file__).resolve().parents[1]
    # Only omit the detector's post-exit delay, including in helper processes.
    test_env = os.environ.copy()
    test_env["GORACE"] = "atexit_sleep_ms=0"
    workers = []

    def interrupted(signum, frame):
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGALRM, interrupted)
    signal.alarm(35 * 60)
    try:
        for _ in range(6):
            workers.append(subprocess.Popen(
                ["nice", "-n", "15", "yes"], stdout=subprocess.DEVNULL
            ))
        print("AEON617_CPU_STRESS_WORKERS=6", flush=True)
        with tempfile.TemporaryDirectory(prefix="aeon-617-negative-") as tmp:
            replacements = {}
            for name, edits in {
                "harness_heartbeat_test.go": [
                    ("\tvar mu sync.Mutex\n", ""),
                    ("\t\tmu.Lock()\n\t\tdefer mu.Unlock()\n", ""),
                ],
                "harness_run_test.go": [("\tsrv.Close()\n\tregs := hbWhere", "\tregs := hbWhere")],
            }.items():
                source = root / "internal/cli" / name
                content = source.read_text()
                for before, after in edits:
                    if content.count(before) != 1:
                        raise RuntimeError("negative-control edit is not unique: " + name)
                    content = content.replace(before, after, 1)
                destination = Path(tmp) / name
                destination.write_text(content)
                replacements[str(source)] = str(destination)
            overlay = Path(tmp) / "overlay.json"
            overlay.write_text(json.dumps({"Replace": replacements}))
            command = [
                "go", "test", "-json", "-race", "-p", "2",
                "-overlay=" + str(overlay), "-run=^TestHarnessRunSIGTERM$",
                "-count=1", "-timeout=2m", "./internal/cli",
            ]
            # Separate executions prevent the race detector's report deduplication
            # from masking whether the negative control reproduces each time.
            for iteration in range(1, 4):
                result = subprocess.run(
                    command, env=test_env, stdout=subprocess.PIPE,
                    stderr=subprocess.STDOUT, text=True, timeout=180, cwd=root,
                )
                print(result.stdout, end="", flush=True)
                events = []
                for line in result.stdout.splitlines():
                    try:
                        events.append(json.loads(line))
                    except ValueError:
                        pass
                failed = any(event.get("Action") == "fail" and
                             event.get("Test") == "TestHarnessRunSIGTERM"
                             for event in events)
                if (result.returncode == 0 or not failed or
                        "WARNING: DATA RACE" not in result.stdout or
                        "harness_heartbeat_test.go" not in result.stdout):
                    raise RuntimeError("negative control did not reproduce the fixture race")
                print(f"AEON617_NEGATIVE_CONTROL_{iteration}=EXPECTED_FIXTURE_RACE", flush=True)
        print("AEON617_FIXED_PACKAGE_START", flush=True)
        return subprocess.call(sys.argv[1:], env=test_env)
    finally:
        signal.alarm(0)
        for worker in workers:
            if worker.poll() is None:
                worker.terminate()
        for worker in workers:
            worker.wait(timeout=10)
        print(f"AEON617_CPU_STRESS_CLEANED={len(workers)}", flush=True)


if __name__ == "__main__":
    sys.exit(main())
