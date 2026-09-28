#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Synthetic tests for the bound session-name heartbeat producer."""

from __future__ import annotations

import importlib.util
import io
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve().parent / "session-heartbeat.py"
SPEC = importlib.util.spec_from_file_location("session_heartbeat", SCRIPT)
HEARTBEAT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HEARTBEAT)

SECRET = "synthetic-secret-value"
AEON_A = "aaaaaaaa-0000-4000-8000-00000000000a"
AEON_B = "bbbbbbbb-0000-4000-8000-00000000000b"
SOURCE_A = "11111111-1111-4111-8111-111111111111"
SOURCE_B = "22222222-2222-4222-8222-222222222222"
# The thread id shape shown in the `codex exec --json` documentation.
SOURCE_HEX = "0199a213-81c0-7800-8aa1-bbab2a035a53"


class FakeCLI:
    """Stands in for the Aeon CLI; records argv and applies accepted beats."""

    def __init__(self, test: unittest.TestCase, session: str = AEON_A, sequence: int = 4):
        self.test = test
        self.status = {"id": session, "phase": "working", "activity": "busy", "activity_sequence": sequence,
                       "stopped_at": None, "archived_at": None, "display_label": "Registered"}
        self.calls: list[list[str]] = []
        self.codes: list[int] = []
        self.on_heartbeat = None
        self.status_code = 0

    def __call__(self, command, **kwargs):
        self.test.assertEqual(kwargs["stderr"], subprocess.DEVNULL)
        self.test.assertEqual(kwargs["stdin"], subprocess.DEVNULL)
        self.test.assertFalse(kwargs["check"])
        self.calls.append(command)
        if command[1:4] == ["--json", "harness", "status"]:
            body = json.dumps(self.status).encode() if self.status_code == 0 else b""
            return subprocess.CompletedProcess(command, self.status_code, stdout=body)
        self.test.assertEqual(command[1:3], ["harness", "heartbeat"])
        self.test.assertEqual(kwargs["stdout"], subprocess.DEVNULL)
        for argument in command[3:]:
            self.test.assertTrue(argument.startswith("--") and "=" in argument, argument)
        flags = dict(argument[2:].split("=", 1) for argument in command[3:])
        if self.on_heartbeat:
            self.on_heartbeat(flags)
        code = self.codes.pop(0) if self.codes else 0
        if code == 0:
            self.status["activity_sequence"] = int(flags["activity-sequence"])
            if "label" in flags:
                self.status["display_label"] = flags["label"]
        return subprocess.CompletedProcess(command, code, stdout=None)

    @property
    def beats(self) -> list[dict[str, str]]:
        return [dict(arg[2:].split("=", 1) for arg in call[3:]) for call in self.calls if call[1:3] == ["harness", "heartbeat"]]


class ProducerTest(unittest.TestCase):
    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.state = self.root / "worker"
        self.state.mkdir(mode=0o700)
        self.binding = self.state / "native-session.json"
        self.index = self.root / "codex-home" / "session_index.jsonl"
        self.index.parent.mkdir()
        self.lease = self.state / "lease.key"

    def bind(self, aeon: str = AEON_A, source: str = SOURCE_A, *extra: str) -> int:
        return HEARTBEAT.main(["bind", "--binding", str(self.binding), "--aeon-session", aeon,
                               "--source-session", source, *extra])

    def names(self, *entries: tuple[str, str]) -> None:
        self.index.write_text("".join(json.dumps({"id": i, "thread_name": n, "updated_at": "2026-09-28T07:00:00Z"}) + "\n"
                                      for i, n in entries), encoding="utf-8")

    def options(self, *extra: str, session: str = AEON_A):
        return HEARTBEAT.parse_args(["run", "--aeon", "/opt/aeon/bin/aeon", "--project", "AEON", "--session", session,
                                     "--agent", "aeon-coordinator", "--worker-lease-file", str(self.lease),
                                     "--binding", str(self.binding), "--codex-index", str(self.index), "--once", *extra])

    def beat(self, cli: FakeCLI, *extra: str, session: str = AEON_A) -> dict:
        out = io.StringIO()
        HEARTBEAT.run(self.options(*extra, session=session), runner=cli, sleep=self.fail, out=out)
        lines = out.getvalue().splitlines()
        self.assertEqual(len(lines), 1)
        return json.loads(lines[0])


class BindingTest(ProducerTest):
    def test_bind_writes_a_private_exact_binding(self) -> None:
        self.assertEqual(self.bind(), 0)
        info = self.binding.stat()
        self.assertEqual(stat.S_IMODE(info.st_mode), 0o600)
        self.assertEqual(json.loads(self.binding.read_text()), {
            "schema": "aeon.native-session-binding.v1", "harness": "codex",
            "aeon_session_id": AEON_A, "source_session_id": SOURCE_A,
        })
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_A)

    def test_rebinding_requires_replace_and_replaces_atomically(self) -> None:
        self.assertEqual(self.bind(), 0)
        with mock.patch("sys.stderr", io.StringIO()) as err:
            self.assertEqual(self.bind(AEON_A, SOURCE_B), 3)
        self.assertIn("--replace", err.getvalue())
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_A)
        self.assertEqual(self.bind(AEON_A, SOURCE_B, "--replace"), 0)
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_B)
        self.assertEqual(sorted(p.name for p in self.state.iterdir()), ["native-session.json"])

    def test_harnesses_without_a_name_source_are_refused_explicitly(self) -> None:
        for harness in ("claude", "cursor", "grok", "pi"):
            with self.subTest(harness=harness):
                result = subprocess.run([sys.executable, str(SCRIPT), "bind", "--binding", str(self.binding),
                                         "--aeon-session", AEON_A, "--harness", harness, "--source-session", SOURCE_A],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 3)
                self.assertIn("no supported safe session name source", result.stderr)
                self.assertFalse(self.binding.exists())

    def test_bind_rejects_non_canonical_ids_and_unsafe_directories(self) -> None:
        for aeon, source in ((AEON_A.upper(), SOURCE_A), (AEON_A, "not-a-uuid"), ("{" + AEON_A + "}", SOURCE_A)):
            with self.subTest(aeon=aeon, source=source), self.assertRaises(SystemExit) as usage, \
                    mock.patch("sys.stderr", io.StringIO()):
                self.bind(aeon, source)
            self.assertEqual(usage.exception.code, 2)
        shared = self.root / "shared"
        shared.mkdir()
        shared.chmod(0o775)
        linked = self.root / "linked"
        linked.symlink_to(self.state, target_is_directory=True)
        for directory in (shared, linked, self.root / "missing"):
            with self.subTest(directory=directory.name):
                with self.assertRaises(HEARTBEAT.Refusal):
                    HEARTBEAT.write_binding(directory / "native-session.json", "codex", AEON_A, SOURCE_A, False)
                self.assertFalse((self.state / "native-session.json").exists())

    def test_read_binding_refuses_unsafe_or_malformed_files(self) -> None:
        valid = {"schema": "aeon.native-session-binding.v1", "harness": "codex",
                 "aeon_session_id": AEON_A, "source_session_id": SOURCE_A}
        cases = {
            "extra key": json.dumps({**valid, "prompt": SECRET}),
            "missing key": json.dumps({k: v for k, v in valid.items() if k != "harness"}),
            "duplicate key": json.dumps(valid)[:-1] + f',"source_session_id":"{SOURCE_B}"}}',
            "schema": json.dumps({**valid, "schema": "aeon.native-session-binding.v0"}),
            "claude": json.dumps({**valid, "harness": "claude"}),
            "upper uuid": json.dumps({**valid, "source_session_id": SOURCE_HEX.upper()}),
            "truncated": json.dumps(valid)[:-3],
            "oversize": json.dumps({**valid, "pad": "x" * 5000}),
        }
        for name, text in cases.items():
            with self.subTest(case=name):
                self.binding.write_text(text)
                self.binding.chmod(0o600)
                with self.assertRaises(HEARTBEAT.Refusal) as refused:
                    HEARTBEAT.read_binding(self.binding)
                self.assertNotIn(SECRET, str(refused.exception))
        self.binding.write_bytes(b"\xff\xfe")
        with self.assertRaises(HEARTBEAT.Refusal):
            HEARTBEAT.read_binding(self.binding)

        self.binding.write_text(json.dumps(valid))
        self.binding.chmod(0o640)
        with self.assertRaises(HEARTBEAT.Refusal):
            HEARTBEAT.read_binding(self.binding)
        self.binding.chmod(0o600)
        os.link(self.binding, self.root / "second-name")
        with self.assertRaises(HEARTBEAT.Refusal):
            HEARTBEAT.read_binding(self.binding)
        (self.root / "second-name").unlink()
        link = self.state / "linked-binding.json"
        link.symlink_to(self.binding)
        with self.assertRaises(HEARTBEAT.Refusal):
            HEARTBEAT.read_binding(link)
        self.assertEqual(HEARTBEAT.read_binding(self.binding), valid)


class ExecEventTest(ProducerTest):
    def bind_stream(self, stream: bytes, *extra: str) -> tuple[int, bytes, str]:
        options = HEARTBEAT.parse_args(["bind", "--binding", str(self.binding), "--aeon-session", AEON_A,
                                        "--from-codex-exec-events", *extra])
        out = io.BytesIO()
        with mock.patch("sys.stderr", io.StringIO()) as err:
            code = HEARTBEAT.bind_from_exec_events(options, io.BufferedReader(io.BytesIO(stream)), out)
        return code, out.getvalue(), err.getvalue()

    def test_first_thread_started_event_binds_and_the_stream_passes_through_unchanged(self) -> None:
        stream = (json.dumps({"type": "thread.started", "thread_id": SOURCE_HEX}) + "\n"
                  + json.dumps({"type": "item.completed", "item": {"type": "agent_message", "text": SECRET}}) + "\n"
                  + json.dumps({"type": "thread.started", "thread_id": SOURCE_B}) + "\n").encode()
        code, out, err = self.bind_stream(stream)
        self.assertEqual((code, out), (0, stream))
        self.assertNotIn(SECRET, err)
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_HEX)

    def test_anything_but_a_complete_first_start_event_binds_nothing_and_still_passes_through(self) -> None:
        streams = {
            "later start": json.dumps({"type": "turn.started"}) + "\n" + json.dumps({"type": "thread.started", "thread_id": SOURCE_A}) + "\n",
            "bad id": json.dumps({"type": "thread.started", "thread_id": "thread-1"}) + "\n",
            "upper id": json.dumps({"type": "thread.started", "thread_id": SOURCE_HEX.upper()}) + "\n",
            "duplicate id": '{"type":"thread.started","thread_id":"%s","thread_id":"%s"}\n' % (SOURCE_A, SOURCE_B),
            "no newline": json.dumps({"type": "thread.started", "thread_id": SOURCE_A}),
            "oversize": json.dumps({"type": "thread.started", "thread_id": SOURCE_A, "pad": "x" * 5000}) + "\n",
            "empty": "",
        }
        for name, text in streams.items():
            with self.subTest(case=name):
                code, out, err = self.bind_stream(text.encode())
                self.assertEqual((code, out), (3, text.encode()))
                self.assertFalse(self.binding.exists())

    def test_existing_binding_is_kept_unless_replaced(self) -> None:
        self.assertEqual(self.bind(AEON_A, SOURCE_B), 0)
        stream = (json.dumps({"type": "thread.started", "thread_id": SOURCE_A}) + "\n").encode()
        self.assertEqual(self.bind_stream(stream)[:2], (3, stream))
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_B)
        self.assertEqual(self.bind_stream(stream, "--replace")[:2], (0, stream))
        self.assertEqual(HEARTBEAT.read_binding(self.binding)["source_session_id"], SOURCE_A)


class RunTest(ProducerTest):
    def test_bound_name_rides_the_heartbeat_with_an_advanced_sequence(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Old name"), (SOURCE_B, "Sibling name"), (SOURCE_A, "-Renamed $(x) worker"))
        cli = FakeCLI(self)
        record = self.beat(cli, "--", "--brief", "AEON-222", "--branch=bk6.harness.rename")
        self.assertEqual(record, {"beat": 1, "heartbeat": "accepted", "label": "included"})
        self.assertEqual(cli.beats, [{
            "project": "AEON", "session": AEON_A, "agent": "aeon-coordinator", "worker-lease-file": str(self.lease),
            "phase": "working", "activity": "busy", "activity-sequence": "5", "label": "-Renamed $(x) worker",
            "brief": "AEON-222", "branch": "bk6.harness.rename",
        }])
        self.assertEqual(cli.calls[0][:4], ["/opt/aeon/bin/aeon", "--json", "harness", "status"])

    def test_missing_wrong_or_blank_sources_send_no_label_but_keep_the_session_alive(self) -> None:
        cases = {
            "unbound": lambda: None,
            "bound-elsewhere": lambda: (self.bind(AEON_B, SOURCE_A), self.names((SOURCE_A, "Not yours"))),
            "unavailable": lambda: self.bind(),
            "unnamed": lambda: (self.bind(), self.names((SOURCE_B, "Sibling only"))),
            "blank": lambda: (self.bind(), self.names((SOURCE_A, "Earlier"), (SOURCE_A, "  "))),
            "refused": lambda: (self.bind(), self.names((SOURCE_A, "x")), self.index.write_text(
                json.dumps({"id": SOURCE_A, "thread_name": "x", "prompt": SECRET}))),
            "linked": lambda: (self.bind(), (self.root / "elsewhere.jsonl").write_text(
                json.dumps({"id": SOURCE_A, "thread_name": SECRET})), self.index.symlink_to(self.root / "elsewhere.jsonl")),
        }
        expected = {"blank": "unnamed", "refused": "unavailable", "linked": "unavailable"}
        for name, arrange in cases.items():
            with self.subTest(case=name):
                for path in (self.binding, self.index):
                    if path.exists() or path.is_symlink():
                        path.unlink()
                arrange()
                cli = FakeCLI(self)
                out = io.StringIO()
                HEARTBEAT.run(self.options(), runner=cli, sleep=self.fail, out=out)
                record = json.loads(out.getvalue())
                self.assertEqual(record["heartbeat"], "accepted")
                self.assertEqual(record["label"], expected.get(name, name))
                self.assertEqual(len(cli.beats), 1)
                self.assertNotIn("label", cli.beats[0])
                self.assertEqual(cli.status["display_label"], "Registered")
                self.assertNotIn(SECRET, out.getvalue())

    def test_every_beat_resends_the_current_name_without_a_local_cache(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Stable name"))
        cli = FakeCLI(self)
        options = self.options()
        options.beats, slept = 3, []
        out = io.StringIO()
        self.assertEqual(HEARTBEAT.run(options, runner=cli, sleep=slept.append, out=out), 0)
        self.assertEqual(slept, [60, 60])
        self.assertEqual([beat["label"] for beat in cli.beats], ["Stable name"] * 3)
        self.assertEqual([beat["activity-sequence"] for beat in cli.beats], ["5", "6", "7"])
        self.assertEqual(len(out.getvalue().splitlines()), 3)
        self.assertNotIn("Stable name", out.getvalue())

    def test_failed_beat_is_retried_after_a_sequence_race_and_on_the_next_beat(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Retry name"))
        cli = FakeCLI(self)
        cli.codes = [1]

        def other_reporter(flags):
            cli.on_heartbeat = None
            cli.status["activity_sequence"] = 9

        cli.on_heartbeat = other_reporter
        self.assertEqual(self.beat(cli)["heartbeat"], "accepted")
        self.assertEqual([(b["activity-sequence"], b["label"]) for b in cli.beats], [("5", "Retry name"), ("10", "Retry name")])

        cli = FakeCLI(self)
        cli.codes = [1]
        self.assertEqual(self.beat(cli), {"beat": 1, "heartbeat": "rejected", "label": "included"})
        self.assertEqual(len(cli.beats), 1)
        self.assertEqual(cli.status["display_label"], "Registered")
        self.assertEqual(self.beat(cli)["heartbeat"], "accepted")
        self.assertEqual(cli.status["display_label"], "Retry name")

    def test_stopped_archived_foreign_or_unreadable_status_sends_no_heartbeat(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Late name"))
        for name, change, heartbeat in (
            ("stopped", {"stopped_at": "2026-09-28T07:00:00Z", "phase": "stopped"}, "stopped"),
            ("archived", {"archived_at": "2026-09-28T07:00:00Z"}, "stopped"),
            ("stopping", {"phase": "stopping"}, "stopped"),
            ("other session", {"id": AEON_B}, "skipped"),
            ("bad sequence", {"activity_sequence": "4"}, "skipped"),
        ):
            with self.subTest(case=name):
                cli = FakeCLI(self)
                cli.status.update(change)
                self.assertEqual(self.beat(cli)["heartbeat"], heartbeat)
                self.assertEqual(cli.beats, [])
        cli = FakeCLI(self)
        cli.status_code = 1
        self.assertEqual(self.beat(cli), {"beat": 1, "heartbeat": "skipped", "label": "included", "reason": "status unavailable"})
        self.assertEqual(cli.beats, [])

    def test_stop_during_a_failed_beat_ends_the_producer(self) -> None:
        self.bind()
        cli = FakeCLI(self)
        cli.codes = [1]
        cli.on_heartbeat = lambda flags: cli.status.update(stopped_at="2026-09-28T07:00:00Z", phase="stopped")
        options = self.options()
        options.beats = 0
        out = io.StringIO()
        self.assertEqual(HEARTBEAT.run(options, runner=cli, sleep=self.fail, out=out), 0)
        self.assertEqual(json.loads(out.getvalue())["heartbeat"], "stopped")

    def test_producer_never_opens_the_worker_lease(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Name"))
        self.lease.write_text(SECRET)
        opened = mock.Mock(wraps=os.open)
        with mock.patch.object(HEARTBEAT.os, "open", opened), \
                mock.patch.object(HEARTBEAT.os, "supports_dir_fd", os.supports_dir_fd | {opened}):
            record = self.beat(FakeCLI(self))
        self.assertEqual(record["label"], "included")
        self.assertIn("native-session.json", {Path(str(call.args[0])).name for call in opened.call_args_list})
        for call in opened.call_args_list:
            self.assertNotIn("lease", str(call.args[0]))

    def test_pass_through_is_limited_to_fixed_metadata_flags(self) -> None:
        for extra in (["--label", "x"], ["--session=" + AEON_B], ["--activity-sequence", "99"], ["--agent", "other"],
                      ["--worker-lease-file=/tmp/other"], ["stray"], ["--brief"], ["--json"]):
            with self.subTest(extra=extra), self.assertRaises(SystemExit) as usage, mock.patch("sys.stderr", io.StringIO()):
                self.options("--", *extra)
            self.assertEqual(usage.exception.code, 2)
        options = self.options("--", "--model", "gpt-6-astra", "--effort=xhigh", "--note", "--label is data")
        self.assertEqual(options.extra, ["--model=gpt-6-astra", "--effort=xhigh", "--note=--label is data"])

    def test_run_rejects_unsafe_arguments(self) -> None:
        base = ["run", "--aeon", "/opt/aeon", "--project", "AEON", "--session", AEON_A, "--agent", "a",
                "--worker-lease-file", "l", "--binding", "b", "--codex-index", "i"]
        for change in ({"--session": AEON_A.upper()}, {"--aeon": "aeon"}, {"--interval": "1"}):
            argv = list(base)
            for flag, value in change.items():
                if flag in argv:
                    argv[argv.index(flag) + 1] = value
                else:
                    argv += [flag, value]
            with self.subTest(change=change), self.assertRaises(SystemExit) as usage, mock.patch("sys.stderr", io.StringIO()):
                HEARTBEAT.parse_args(argv)
            self.assertEqual(usage.exception.code, 2)

    def test_command_line_producer_drives_an_executable_cli(self) -> None:
        self.bind()
        self.names((SOURCE_A, "Process name"))
        log = self.root / "cli.log"
        cli = self.root / "aeon"
        cli.write_text("\n".join([
            "#!" + sys.executable,
            "import json, sys",
            f"log = open({str(log)!r}, 'a')",
            "log.write(json.dumps(sys.argv[1:]) + '\\n')",
            "if sys.argv[1:4] == ['--json', 'harness', 'status']:",
            f"    print(json.dumps({{'id': {AEON_A!r}, 'phase': 'working', 'activity': 'busy', 'activity_sequence': 2,"
            " 'stopped_at': None, 'archived_at': None}))",
            "else:",
            f"    print({SECRET!r}); print({SECRET!r}, file=sys.stderr)",
        ]) + "\n")
        cli.chmod(0o700)
        result = subprocess.run([sys.executable, str(SCRIPT), "run", "--aeon", str(cli), "--project", "AEON",
                                 "--session", AEON_A, "--agent", "aeon-coordinator", "--worker-lease-file", str(self.lease),
                                 "--binding", str(self.binding), "--codex-index", str(self.index), "--once"],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"beat": 1, "heartbeat": "accepted", "label": "included"})
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        calls = [json.loads(line) for line in log.read_text().splitlines()]
        self.assertEqual(calls[1][:2], ["harness", "heartbeat"])
        self.assertIn("--label=Process name", calls[1])
        self.assertIn("--activity-sequence=3", calls[1])


if __name__ == "__main__":
    unittest.main()
