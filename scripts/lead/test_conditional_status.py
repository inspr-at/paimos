#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Stateful tracker tests; no model, review, git push, or live tracker is run.

Run with --harness-source ROOT to additionally apply the integration patch in
memory and exercise the real patched autopilot functions with stubbed effects.
No live harness file is modified or copied to an executable location.
"""

import argparse
import ast
import calendar
import datetime
import glob
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import time
import types
import unittest

import conditional_status as guards

CODE = Path(__file__).resolve().parent
PM = '''#!/usr/bin/env python3
import json,sys
from pathlib import Path
p = Path(__file__).resolve().parent
args = sys.argv[1:]
with (p/'calls').open('a') as f: f.write(json.dumps(args)+'\\n')
state = (p/'state').read_text()
if args[:2] == ['issue','get']:
    if state == 'bad': print('broken'); sys.exit(1)
    print(json.dumps({'status': 'open' if state == 'error-json' else state}))
    sys.exit(1 if state == 'error-json' else 0)
if args[:2] == ['issue','update']:
    if (p/'cancel-on-update').exists(): state = 'cancelled'; (p/'state').write_text(state)
    if '--if-status' not in args: sys.exit('missing atomic precondition')
    if state not in args[args.index('--if-status')+1].split(','): sys.exit(1)
    (p/'state').write_text(args[args.index('--status')+1]); sys.exit(0)
if args[:2] == ['issue','comment']:
    if (p/'cancel-on-comment').exists(): (p/'state').write_text('cancelled')
    sys.exit(0)
sys.exit('unexpected tracker call')
'''
MARK = '''#!/usr/bin/env python3
import sys
from pathlib import Path
p = Path(__file__).resolve().parent
assert sys.argv[1:3] == ['harness','mark-stopped']
if (p/'require-stop-record').exists():
    assert (p/'qa/stopped-runs.txt').is_file(), 'stop record missing before cleanup'
n = int((p/'mark-attempts').read_text())+1 if (p/'mark-attempts').exists() else 1
(p/'mark-attempts').write_text(str(n))
sys.exit(1 if n <= int((p/'mark-failures').read_text()) else 0)
'''


class Guards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="aeon-conditional-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name, body in (("pm", PM), ("mark-cli", MARK)):
            (self.root / name).write_text(body)
            (self.root / name).chmod(0o700)
        (self.root / "state").write_text("open")
        (self.root / "marker").write_text("Synthetic worker marker")
        (self.root / "mark-failures").write_text("0")
        (self.root / "qa/dispatch-marks").mkdir(parents=True)
        (self.root / "qa/dispatch-marks/slug").write_text("100.25 0 slug.md\n")

    def shell(self, code):
        prelude = f'S={shlex.quote(str(self.root))}; . {shlex.quote(str(CODE / "conditional-status.sh"))}; sleep() {{ :; }}; '
        return subprocess.run(["bash", "-uc", prelude + code], text=True, capture_output=True, timeout=15)

    def calls(self, operation):
        path = self.root / "calls"
        rows = [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
        return [r for r in rows if r[:2] == ["issue", operation]]

    def claim(self, state="open", mode="first"):
        (self.root / "state").write_text(state)
        return self.shell(f'claim_ticket AEON-9001 "$S/marker" {mode}')

    def test_claim_is_one_conditional_transition(self):
        for state in ("open", "qa"):
            with self.subTest(state=state):
                (self.root / "calls").write_text("")
                result = self.claim(state)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((self.root / "state").read_text(), "in_progress")
                self.assertEqual(len(self.calls("update")), 1)
                self.assertEqual(self.calls("update")[0][-2:], ["--if-status", "open,qa"])
                self.assertEqual(len(self.calls("comment")), 1)

    def test_cancel_between_read_and_claim_is_never_overwritten(self):
        (self.root / "cancel-on-update").touch()
        result = self.claim()
        self.assertEqual(result.returncode, 3)
        self.assertEqual((self.root / "state").read_text(), "cancelled")
        self.assertEqual(len(self.calls("update")), 1)
        self.assertEqual(self.calls("comment"), [])

    def test_rounds_never_write_status(self):
        for state in ("open", "qa", "in_progress", "done"):
            with self.subTest(state=state):
                result = self.claim(state, "round")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((self.root / "state").read_text(), state)
        self.assertEqual(self.calls("update"), [])

    def test_cancelled_and_unreadable_never_claim(self):
        for state, expected in (("cancelled", 1), ("bad", 3), ("error-json", 3)):
            with self.subTest(state=state):
                self.assertEqual(self.claim(state).returncode, expected)
        self.assertEqual(self.calls("update"), [])
        self.assertEqual(self.calls("comment"), [])

    def worker(self, state, failures=0):
        (self.root / "state").write_text(state)
        (self.root / "mark-failures").write_text(str(failures))
        return self.shell('''A="$S/mark-cli"; TICKET=AEON-9001; SLUG=slug; SESSION=synthetic;
ST="$S/worker-state"; BRIEF=slug.md; LOG="$S/worker.log";
if ! worker_start_allowed; then exit 0; fi
echo builder-started > "$S/builder"
''')

    def test_cancelled_worker_records_stop_before_retrying_cleanup(self):
        (self.root / "require-stop-record").touch()
        result = self.worker("cancelled", failures=2)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.root / "builder").exists())
        record = (self.root / "qa/stopped-runs.txt").read_text().split("\t")
        self.assertEqual(record[:2], ["slug", "slug.md"])
        self.assertRegex(record[2], r"^\d+\.\d+$")
        self.assertEqual((self.root / "mark-attempts").read_text(), "3")
        self.assertNotIn("mark-stopped failed 3x", (self.root / "escalate.log").read_text())

    def test_failed_mark_stopped_escalates_without_builder(self):
        self.assertEqual(self.worker("cancelled", failures=3).returncode, 0)
        self.assertEqual((self.root / "mark-attempts").read_text(), "3")
        self.assertFalse((self.root / "builder").exists())
        self.assertIn("mark-stopped failed 3x", (self.root / "escalate.log").read_text())

    def test_unreadable_worker_status_stops_and_open_worker_runs(self):
        self.assertEqual(self.worker("bad").returncode, 0)
        self.assertFalse((self.root / "builder").exists())
        self.assertIn("unknown", (self.root / "qa/stopped-runs.txt").read_text())
        self.assertEqual(self.worker("open").returncode, 0)
        self.assertTrue((self.root / "builder").exists())

    def test_stop_record_failure_still_prevents_builder(self):
        (self.root / "qa/stopped-runs.txt").mkdir()
        self.assertEqual(self.worker("cancelled").returncode, 0)
        self.assertFalse((self.root / "builder").exists())
        self.assertIn("could not persist stop record", (self.root / "escalate.log").read_text())
        self.assertEqual((self.root / "mark-attempts").read_text(), "1")

    def test_delivery_honours_exact_run_and_defers_unreadable_status(self):
        calls = []
        def run(command, timeout):
            calls.append(command)
            self.assertEqual(timeout, 30)
            return 0, '{"status":"open"}'
        stop = self.root / "qa/stopped-runs.txt"
        stop.write_text("slug\tslug.md\t100\tcancelled\n")
        self.assertEqual(guards.delivery_blocked(self.root, "slug", "AEON-9001", run)[0], "stopped")
        self.assertEqual(calls, [])
        stop.write_text("slug\tslug.md\t99\tcancelled\n")
        self.assertIsNone(guards.delivery_blocked(self.root, "slug", "AEON-9001", run))
        for response in ((0, '{"status":"cancelled"}'), (1, '{"status":"open"}'), (0, '{}'), (0, '{"status":null}')):
            result = guards.delivery_blocked(self.root, "slug", "AEON-9001", lambda *a, **k: response)
            self.assertEqual(result[0], "stopped" if "cancelled" in response[1] else "retry")
        def timeout(*args, **kwargs):
            raise subprocess.TimeoutExpired("synthetic", 30)
        self.assertEqual(guards.delivery_blocked(self.root, "slug", "AEON-9001", timeout)[0], "retry")

    def test_bad_stop_metadata_fails_closed(self):
        stop = self.root / "qa/stopped-runs.txt"
        stop.write_text("slug\tslug.md\tbad\n")
        self.assertTrue(guards.stopped_on_purpose(self.root, "slug", "slug.md"))
        stop.write_text("slug\tslug.md\t101\n")
        (self.root / "qa/dispatch-marks/slug").write_text("unreadable\n")
        self.assertTrue(guards.stopped_on_purpose(self.root, "slug", "slug.md"))

    def test_qa_transition_is_conditional_and_keeps_terminal_siblings(self):
        calls = []
        def run(command, timeout):
            calls.append(command)
            self.assertEqual(timeout, 30)
            return response
        for response, expected in (
            ((0, '{"status":"qa"}'), True),
            ((1, '{"code":"status_mismatch","current_state":"cancelled"}'), False),
            ((1, '{"code":"status_mismatch","current_state":"done"}'), True),
            ((1, '{"code":"entry_checks_failed"}'), False),
            ((1, 'unreadable'), False),
            ((0, '{}'), False),
        ):
            with self.subTest(response=response):
                self.assertEqual(guards.advance_qa(self.root, "AEON-9001", run)[0], expected)
                self.assertEqual(calls[-1][-2:], ["--if-status", "in_progress,qa"])


def patched_sources(root):
    """Apply exact diff hunks in memory; refuse drift instead of guessing."""
    lines = (CODE / "conditional-status.patch").read_text().splitlines(keepends=True)
    sources, index = {}, 0
    while index < len(lines):
        if not lines[index].startswith("--- a/"):
            raise ValueError("expected file header")
        name = lines[index][6:].strip()
        original = (Path(root) / name).read_text().splitlines(keepends=True)
        index += 2
        cursor, output = 0, []
        while index < len(lines) and not lines[index].startswith("--- a/"):
            match = re.match(r"@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@", lines[index])
            if not match:
                raise ValueError("expected hunk")
            start = int(match[1]) - 1
            output += original[cursor:start]
            cursor = start
            index += 1
            while index < len(lines) and not lines[index].startswith(("@@ ", "--- a/")):
                line = lines[index]
                # Git accepts an empty context line for a blank source line.
                # This keeps the tracked patch free of trailing whitespace.
                if line == "\n":
                    line = " \n"
                if line[0] in " -":
                    if cursor >= len(original) or original[cursor] != line[1:]:
                        raise ValueError(f"harness drift: {name}:{cursor+1}")
                    cursor += 1
                if line[0] in " +":
                    output.append(line[1:])
                index += 1
        output += original[cursor:]
        sources[name] = "".join(output)
    return sources


def integration_tests(sources):
    class Integration(Guards):
        def setUp(self):
            super().setUp()
            self.state, self.effects, self.cancel_on = "open", [], None
            self.slug, self.key = "aeon-9001-test", "AEON-9001"
            (self.root / "qa/dispatch-marks" / self.slug).write_text(f"100.25 0 {self.slug}.md\n")
            (self.root / "start-queue.txt").write_text("")
            (self.root / "start-queue.done").write_text(f"23:00 {self.key} {self.slug} {self.slug}.md\n")
            (self.root / "worktrees" / self.slug).mkdir(parents=True)
            self.logs = []
            namespace = dict(conditional_status=guards, S=str(self.root), W=str(self.root / "worktrees"), R="synthetic/repo",
                             os=os, re=re, json=json, time=time, calendar=calendar, datetime=datetime, glob=glob,
                             subprocess=types.SimpleNamespace(Popen=self.forbidden), EXCLUDE=re.compile(r"^never$"),
                             NOSHIP=re.compile(r"^never$"), MAX_ROUND=6)
            tree = ast.parse(sources["autopilot.py"])
            definitions = ast.Module(body=[n for n in tree.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef))], type_ignores=[])
            exec(compile(definitions, "patched-autopilot", "exec"), namespace)
            namespace.update(sh=self.run_effect, log=lambda msg, **kw: self.logs.append(msg), running=lambda slug: False,
                             last_exit=lambda slug: "exit 0", newest_review_for=lambda slug: None, no_op_round=lambda *a: None,
                             ended_unfinished=lambda slug: None, capped_unfinished=lambda slug: False,
                             ci_first=lambda: True, ci_state=lambda head: "green", pr_dirty=lambda pr: False,
                             main_conflicts=lambda wt: False, GATE=lambda slug: "synthetic-gate.sh")
            self.ap = types.SimpleNamespace(**namespace)
            self.ns = namespace

        def forbidden(self, *args, **kwargs):
            raise AssertionError("unexpected real side effect")

        def run_effect(self, command, cwd=None, timeout=600):
            if isinstance(command, list) and command[:3] == [str(self.root / "pm"), "issue", "get"]:
                return (1, "unreadable") if self.state == "unknown" else (0, json.dumps({"status": self.state}))
            text = command if isinstance(command, str) else " ".join(command)
            if self.cancel_on and self.cancel_on in text:
                self.state = "cancelled"
            if "git push" in text or "nohup ./" in text:
                self.effects.append(text)
                return 0, ""
            if "rev-list --count" in text:
                return 0, "1"
            if "rev-parse HEAD" in text:
                return 0, "abc"
            if "pr list" in text and "length" in text:
                return 0, "1" if "--state open" in text else "0"
            return 0, ""

        def test_patch_syntax_and_worker_guard_order(self):
            for name, source in sources.items():
                if name.endswith(".py"):
                    compile(source, name, "exec")
                else:
                    subprocess.run(["bash", "-n"], input=source, text=True, check=True)
            worker = sources["worker-run.sh"]
            self.assertLess(worker.index("worker_start_allowed"), worker.index('case "$H" in'))
            self.assertGreater(worker.index("worker_start_allowed"), worker.index("harness register"))
            self.assertIn("claim_ticket", sources["dispatch.sh"])
            self.assertNotIn('issue update "$KEY" --status in_progress', sources["dispatch.sh"])
            self.assertIn('"refusing: ticket status unreadable"', sources["start-queue.sh"])

        def test_ci_push_checks_cancel_after_github_reads(self):
            self.cancel_on = "gh pr list"
            result = self.ap.precheck_push(self.slug, self.key)
            self.assertEqual(result[0], "stopped")
            self.assertEqual(self.effects, [])

        def test_ci_precheck_cancel_and_stop_remove_entry_unknown_keeps_it(self):
            for state in ("cancelled", "unknown"):
                self.state = state
                st = {"precheck": {self.slug: {"key": self.key, "state": "push"}}}
                self.ap.precheck_tick(st)
                self.assertEqual(self.slug in st["precheck"], state == "unknown")
                self.assertEqual(self.effects, [])
            self.state = "open"
            (self.root / "qa/stopped-runs.txt").write_text(f"{self.slug}\t{self.slug}.md\t101\n")
            st = {"precheck": {self.slug: {"key": self.key, "state": "push"}}}
            self.ap.precheck_tick(st)
            self.assertEqual(st["precheck"], {})
            self.assertEqual(self.effects, [])

        def test_green_ci_rereads_cancel_before_gate(self):
            def ci(head):
                self.state = "cancelled"
                return "green"
            self.ns["ci_state"] = ci
            st = {"precheck": {self.slug: {"key": self.key, "state": "ci", "head": "abc", "pr": "1"}}}
            self.ap.precheck_tick(st)
            self.assertEqual(self.effects, [])

        def test_first_builds_honour_cancel_unknown_and_stop_records(self):
            for state in ("cancelled", "unknown", "open"):
                self.state = state
                if state == "open":
                    (self.root / "qa/stopped-runs.txt").write_text(f"{self.slug}\t{self.slug}.md\t101\n")
                st = {"precheck": {}, "gated_runs": [], "seen_reviews": []}
                self.ap.tick(st)
                self.assertEqual(st["precheck"], {}, state)
                self.assertEqual(self.effects, [], state)

        def test_first_build_positive_path_registers_precheck(self):
            st = {"precheck": {}, "gated_runs": [], "seen_reviews": []}
            self.ap.tick(st)
            # tick continues into precheck_tick; the stub accepts the push only
            # when the ticket stayed open. This is a recorded stub, never git.
            self.assertTrue(self.slug in st["precheck"] or self.effects)
            self.assertTrue(st["gated_runs"])

        def test_delivery_cancel_during_qa_write_never_arms_enqueue(self):
            head = "abcdef0123456789abcdef0123456789abcdef0123"
            (self.root / "qa/gate-review.md").write_text(f"(head {head})")
            calls = []
            def run(command, cwd=None, timeout=600):
                text = command if isinstance(command, str) else " ".join(command)
                if isinstance(command, list) and command[1:3] == ["issue", "update"]:
                    calls.append(command)
                    self.state = "cancelled"  # after the read, before the atomic write
                    return 1, '{"code":"status_mismatch","current_state":"cancelled"}'
                if "rev-parse HEAD" in text:
                    return 0, head
                if "pr list" in text:
                    return 0, "1"
                return self.run_effect(command, cwd, timeout)
            self.ns["sh"] = run
            self.assertFalse(self.ap.ship(self.slug, self.key, "review", {}))
            self.assertEqual(len(calls), 1)
            self.assertEqual(calls[0][-2:], ["--if-status", "in_progress,qa"])
            self.assertEqual(self.state, "cancelled")
            self.assertEqual(len(self.effects), 1)  # push happened before the cancel
            self.assertIn("git push", self.effects[0])

    return Integration


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--harness-source", type=Path)
    args = parser.parse_args()
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(Guards)
    if args.harness_source:
        suite.addTests(unittest.defaultTestLoader.loadTestsFromTestCase(integration_tests(patched_sources(args.harness_source))))
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    raise SystemExit(0 if result.wasSuccessful() else 1)
