#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Prove the startup webhook regression detects a missing projection retry.

Run through remote-test.sh with internal/delivery and:
  -exec=../../scripts/test-webhook-start-race-negative.py
  -run=^TestDeliveryWebhookStartupProjectionRace$
Only a temporary Go overlay is changed; no negative-control commit is needed.
"""

import json
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    root = Path(__file__).resolve().parents[1]
    source = root / "internal/delivery/webhook.go"
    check = "if !errors.Is(err, pgx.ErrNoRows) || attempt == 2 {"
    content = source.read_text()
    if content.count(check) != 1:
        raise RuntimeError("negative-control edit is not unique")
    with tempfile.TemporaryDirectory(prefix="aeon-1650-negative-") as temporary:
        replacement = Path(temporary) / "webhook.go"
        replacement.write_text(content.replace(check, check.replace("== 2", "== 0")))
        overlay = Path(temporary) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
        result = subprocess.run([
            "go", "test", "-p", "2", "-count=1", "-timeout=2m", "-overlay=" + str(overlay),
            "-run=^TestDeliveryWebhookStartupProjectionRace$/history_ready_during_request_true$",
            "./internal/delivery",
        ], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
        print(result.stdout, end="", flush=True)
        if result.returncode == 0 or "startup projection HTTP 502, want 204" not in result.stdout:
            raise RuntimeError("negative control did not fail for the missing projection retry")
        print("AEON-1650 negative control: startup webhook returned 502 without retries (expected failure)", flush=True)
    return subprocess.run(sys.argv[1:], timeout=180).returncode


if __name__ == "__main__":
    raise SystemExit(main())
