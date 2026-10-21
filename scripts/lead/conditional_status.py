# SPDX-License-Identifier: AGPL-3.0-only
"""Cancellation guards for the LEAD autopilot; no work runs at import time.

The coordinator installs this alongside autopilot.py after the API/CLI release.
The injected runner is autopilot.py's bounded subprocess adapter.
"""

import calendar
import json
from pathlib import Path
import re
import subprocess
import time

MAX_RECORD_BYTES = 2 * 1024 * 1024


def dispatch_mark(root, slug):
    """Return the latest dispatch's epoch and brief, or None if absent.

    Invalid/unreadable metadata raises: callers must defer instead of pairing a
    stop record with an older run. No worker or key files are opened.
    """
    try:
        with (Path(root) / "qa/dispatch-marks" / slug).open() as source:
            parts = source.read(4097).split()
    except FileNotFoundError:
        return None
    if len(parts) != 3 or len(parts[2]) > 256:
        raise ValueError("invalid dispatch mark")
    epoch = float(parts[0])
    if not epoch > 0 or not epoch < float("inf"):
        raise ValueError("invalid dispatch epoch")
    return epoch, parts[2]


def stopped_on_purpose(root, slug, brief=None):
    """A stop applies to its exact brief and dispatch, including first builds.

    Legacy pause-workers records have integer epochs. Compare at second
    precision so a stop during the dispatch's own second is still honoured.
    Missing dispatch metadata cannot invalidate an existing matching stop.
    """
    try:
        with (Path(root) / "qa/stopped-runs.txt").open() as source:
            raw = source.read(MAX_RECORD_BYTES + 1)
    except FileNotFoundError:
        return False
    except (OSError, UnicodeError):
        return True
    if len(raw) > MAX_RECORD_BYTES:
        return True
    try:
        mark = dispatch_mark(root, slug)
        if brief is None and mark:
            brief = mark[1]
        records = [line.split("\t") for line in raw.splitlines() if line.split("\t")[0] == slug]
        if brief is not None:
            records = [p for p in records if len(p) < 2 or p[1] == brief]
        if not records:
            return False
        if mark and brief is not None and mark[1] != brief:
            return True  # a stale precheck must not act for a different round
        if mark:
            epoch = mark[0]
        else:
            try:
                with (Path(root) / "start-queue.nohup.log").open() as source:
                    starts = [line.split()[0] for line in source if re.match(rf"^\S+Z start {re.escape(slug)} ", line)]
                epoch = calendar.timegm(time.strptime(starts[-1][:20], "%Y-%m-%dT%H:%M:%SZ"))
            except (OSError, UnicodeError, ValueError, IndexError):
                return True
        return any(len(p) < 3 or not float(p[2]) < int(epoch) for p in records)
    except (OSError, UnicodeError, ValueError, OverflowError):
        return True


def delivery_blocked(root, slug, key, run, brief=None):
    """None permits work; ('stopped'|'retry', reason) blocks it.

    Read again immediately before pushes and gates. A transient tracker failure
    keeps the precheck pending; cancelled/stopped runs leave the precheck queue.
    """
    if stopped_on_purpose(root, slug, brief):
        return "stopped", "this dispatch has a stop record"
    try:
        rc, raw = run([str(Path(root) / "pm"), "issue", "get", key, "--json"], timeout=30)
        if rc or len(raw) > MAX_RECORD_BYTES:
            raise ValueError("unreadable ticket")
        state = json.loads(raw)["status"]
        if not isinstance(state, str) or not state.strip() or len(state) > 128 or any(c in state for c in "\r\n\t"):
            raise ValueError("unreadable status")
    except (OSError, ValueError, KeyError, TypeError, TimeoutError, subprocess.TimeoutExpired):
        return "retry", "ticket status unreadable"
    if state == "cancelled":
        return "stopped", "ticket is cancelled"
    return None
