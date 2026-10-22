#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Go -exec wrapper proving the concurrent-cancel regression detects no CAS.

Run only through the approved remote-test.sh runner, for internal/nodes:
  -exec=../../scripts/test-conditional-status-negative.py
  -run '^TestConditionalStatusPreservesConcurrentCancel$' ./internal/nodes
Only an owned temporary overlay is changed. No negative-control commit exists.
"""

import json
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    root = Path(__file__).resolve().parents[1]
    source = root / "internal/nodes/nodes.go"
    check = "if !slices.Contains(states, current.State) {"
    content = source.read_text()
    if content.count(check) != 1:
        raise RuntimeError("negative-control edit is not unique")
    with tempfile.TemporaryDirectory(prefix="aeon-1345-negative-") as temporary:
        replacement = Path(temporary) / "nodes.go"
        replacement.write_text(content.replace(check, "if false && !slices.Contains(states, current.State) {"))
        overlay = Path(temporary) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
        result = subprocess.run([
            "go", "test", "-p", "2", "-count=1", "-timeout=2m", "-overlay=" + str(overlay),
            "-run=^TestConditionalStatusPreservesConcurrentCancel$", "./internal/nodes",
        ], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
        print(result.stdout, end="", flush=True)
        if result.returncode == 0 or "cancel overwritten or wrong refusal: 200" not in result.stdout:
            raise RuntimeError("negative control did not fail for an overwritten cancellation")
        print("AEON-1345 negative control: cancellation overwritten without the comparison (expected failure)", flush=True)
    return subprocess.run(sys.argv[1:], timeout=180).returncode


if __name__ == "__main__":
    raise SystemExit(main())
