#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Synthetic-fixture tests for the harness metadata adapter."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "session-metadata.py"
SECRET = "synthetic-secret-value"
SESSION_A = "11111111-1111-4111-8111-111111111111"
SESSION_B = "22222222-2222-4222-8222-222222222222"


def run(root: Path, harness: str = "codex", extra: list[str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--fixture", str(root), "--harness", harness, *(extra or [])],
        check=False, capture_output=True, text=True,
    )


class SessionMetadataTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def write(self, name: str, text: str) -> Path:
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def test_codex_fixture_keeps_title_model_and_effort(self) -> None:
        self.write("session_index.jsonl", "\n".join([
            json.dumps({
                "id": SESSION_B, "thread_name": "Other synthetic thread",
                "prompt": SECRET, "cwd": "/tmp/synthetic",
            }),
            json.dumps({
                "id": SESSION_A, "thread_name": "Synthetic model sync",
                "base_instructions": SECRET,
            }),
        ]))
        self.write("session_meta.json", json.dumps({
            "type": "session_meta",
            "payload": {
                "id": SESSION_A, "model": "gpt-6-sol\u0001",
                "model_reasoning_effort": " high ",
                "base_instructions": SECRET, "auth": {"token": SECRET},
            },
        }))
        self.write("config.toml", '\n'.join([
            'model = "gpt-6-terra"',
            'model_reasoning_effort = "medium"',
            f'api_key = "{SECRET}"',
            "",
            "[profiles.other]",
            'model = "should-not-win"',
        ]))
        self.write("rollout.jsonl", json.dumps({"message": SECRET, "model": "from-transcript"}))
        result = run(self.root, extra=["--session-id", SESSION_A])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        self.assertNotIn("from-transcript", result.stdout)
        document = json.loads(result.stdout)
        self.assertEqual(document["thread_title"], "Synthetic model sync")
        self.assertEqual(document["model"], "gpt-6-sol")
        self.assertEqual(document["reasoning_effort"], "high")
        self.assertEqual(document["heartbeat_args"], ["--model", "gpt-6-sol", "--effort", "high"])

    def test_session_filter_ignores_the_other_thread_and_global_model(self) -> None:
        self.write("session_index.jsonl", "\n".join([
            json.dumps({"id": SESSION_A, "thread_name": "Thread A", "model": "gpt-6-luna"}),
            json.dumps({"id": SESSION_B, "thread_name": "Thread B"}),
        ]))
        self.write("config.toml", 'model = "gpt-6-terra"\n')
        result = run(self.root, extra=["--session-id", SESSION_B])
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads(result.stdout)
        self.assertEqual(document["thread_title"], "Thread B")
        self.assertNotIn("model", document)
        self.assertEqual(document["heartbeat_args"], [])

    def test_single_session_config_fills_missing_model_without_profile_or_secret(self) -> None:
        self.write("session_meta.json", json.dumps({"thread_title": "Synthetic"}))
        self.write("config.toml", "\n".join([
            'model = "gpt-6-terra"',
            'model_reasoning_effort = "medium"',
            f'api_key = "{SECRET}"',
            "",
            "[profiles.other]",
            'model = "should-not-win"',
        ]))
        result = run(self.root)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        self.assertNotIn("should-not-win", result.stdout)
        document = json.loads(result.stdout)
        self.assertEqual(document["model"], "gpt-6-terra")
        self.assertEqual(document["reasoning_effort"], "medium")

    def test_unchanged_previous_omits_heartbeat_flags(self) -> None:
        self.write("session_meta.json", json.dumps({"model": "gpt-6-sol", "reasoning_effort": "xhigh", "thread_title": "Synthetic"}))
        previous = self.write("previous.json", json.dumps({
            "thread_title": "Synthetic", "model": "gpt-6-sol", "reasoning_effort": "medium",
            "api_key": SECRET,
        }))
        result = run(self.root, extra=["--previous", str(previous)])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        document = json.loads(result.stdout)
        self.assertEqual(document["changed"], ["reasoning_effort"])
        self.assertEqual(document["heartbeat_args"], ["--effort", "xhigh"])

    def test_claude_fixture_ignores_transcript_and_codex_files(self) -> None:
        self.write("claude_session.json", json.dumps({
            "sessionName": "Synthetic claude thread", "model": "claude-fable-5", "effort": "xhigh",
            "messages": [{"content": SECRET}], "api_key": SECRET,
        }))
        self.write("session_meta.json", json.dumps({"model": "gpt-6-sol", "thread_title": "Codex only"}))
        result = run(self.root, harness="claude")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        self.assertNotIn("gpt-6-sol", result.stdout)
        document = json.loads(result.stdout)
        self.assertEqual(document["thread_title"], "Synthetic claude thread")
        self.assertEqual(document["model"], "claude-fable-5")
        self.assertEqual(document["reasoning_effort"], "xhigh")
        self.assertNotIn("--label", document["heartbeat_args"])

    def test_model_is_clipped_to_the_heartbeat_limit(self) -> None:
        self.write("session_meta.json", json.dumps({"model": "m" * 200}))
        document = json.loads(run(self.root).stdout)
        self.assertEqual(len(document["model"]), 120)

    def test_refuses_symlink_oversize_auth_store_and_invalid_text(self) -> None:
        outside = self.root.parent / "outside-secret.json"
        self.addCleanup(outside.unlink, missing_ok=True)
        outside.write_text(json.dumps({"model": SECRET}), encoding="utf-8")
        (self.root / "session_meta.json").symlink_to(outside)
        skipped = run(self.root)
        self.assertEqual(skipped.returncode, 0, skipped.stderr)
        self.assertNotIn(SECRET, skipped.stdout + skipped.stderr)
        self.assertNotIn("model", json.loads(skipped.stdout))
        (self.root / "session_meta.json").unlink()

        self.write("session_meta.json", "x" * 70000)
        oversized = run(self.root)
        self.assertEqual(oversized.returncode, 3)
        self.assertNotIn("x" * 80, oversized.stdout + oversized.stderr)

        store = self.root / ".ssh"
        store.mkdir()
        self.assertEqual(run(store).returncode, 3)

        broken = self.write("session_meta.json", b"\xff".decode("latin1"))
        broken.write_bytes(b"\xff\xfe" + SECRET.encode())
        rejected = run(self.root)
        self.assertEqual(rejected.returncode, 3)
        self.assertNotIn(SECRET, rejected.stdout + rejected.stderr)

    def test_usage_error_does_not_read_the_fixture(self) -> None:
        self.write("session_meta.json", json.dumps({"model": SECRET}))
        result = subprocess.run([sys.executable, str(SCRIPT), "--fixture", str(self.root)], check=False, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(SECRET, result.stdout + result.stderr)

    def live(self, entries: list[dict], session_id: str = SESSION_A, extra: list[str] | None = None):
        index = self.write("session_index.jsonl", "\n".join(json.dumps(entry) for entry in entries)).resolve()
        return subprocess.run([sys.executable, str(SCRIPT), "--codex-index", str(index), "--harness", "codex", "--session-id", session_id, *(extra or [])], capture_output=True)

    def test_live_index_binds_one_session_and_uses_last_name_without_inventing_configuration(self) -> None:
        result = self.live([
            {"id": SESSION_A, "thread_name": "Old name", "updated_at": "2026-09-27T10:00:00Z"},
            {"id": SESSION_B, "thread_name": "Other name", "updated_at": "2026-09-27T10:01:00Z"},
            {"id": SESSION_A, "thread_name": "Renamed worker", "updated_at": "2026-09-27T10:02:00Z"},
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = json.loads(result.stdout)
        self.assertEqual(body["source_session_id"], SESSION_A)
        self.assertEqual(body["heartbeat_args"], ["--label", "Renamed worker"])
        self.assertEqual(body["capture_status"], "partial")
        self.assertEqual(body["missing_fields"], ["model", "reasoning_effort"])
        self.assertNotIn("account_label", body)

    def test_live_index_missing_session_has_no_heartbeat_arguments(self) -> None:
        result = self.live([{"id": SESSION_B, "thread_name": "Other"}])
        body = json.loads(result.stdout)
        self.assertEqual(body["capture_status"], "unavailable")
        self.assertEqual(body["heartbeat_args"], [])

    def test_live_index_requires_explicit_source_uuid(self) -> None:
        result = self.live([{"id": SESSION_A, "thread_name": "Worker"}], "")
        self.assertEqual(result.returncode, 3)
        self.assertEqual(result.stdout, b"")

    def test_live_index_rejects_transcript_shape_without_echoing_values(self) -> None:
        result = self.live([{"id": SESSION_A, "thread_name": "Worker", "messages": [SECRET]}])
        self.assertEqual(result.returncode, 3)
        self.assertNotIn(SECRET.encode(), result.stdout + result.stderr)

    def test_live_null_args_preserve_shell_metacharacters_as_data_and_allow_clear(self) -> None:
        name = "Literal $(false) `false` 'quotes'"
        result = self.live([{"id": SESSION_A, "thread_name": name}], extra=["--null-args"])
        self.assertEqual(result.stdout.split(b"\0"), [b"--label", name.encode(), b""])
        cleared = self.live([{"id": SESSION_A, "thread_name": ""}], extra=["--null-args"])
        self.assertEqual(cleared.stdout, b"--label\0\0")

    def test_live_index_refuses_symlink_and_oversize(self) -> None:
        index = self.write("session_index.jsonl", "x" * 70000).resolve()
        command = [sys.executable, str(SCRIPT), "--codex-index", str(index), "--harness", "codex", "--session-id", SESSION_A]
        self.assertEqual(subprocess.run(command, capture_output=True).returncode, 3)
        target = self.write("metadata.json", json.dumps({"id": SESSION_A, "thread_name": SECRET}))
        index.unlink()
        index.symlink_to(target)
        result = subprocess.run(command, capture_output=True)
        self.assertEqual(result.returncode, 3)
        self.assertNotIn(SECRET.encode(), result.stdout + result.stderr)

    def test_live_index_rejects_private_store_without_reading_it(self) -> None:
        index = self.root.resolve() / ".ssh" / "session_index.jsonl"
        result = subprocess.run([sys.executable, str(SCRIPT), "--codex-index", str(index), "--harness", "codex", "--session-id", SESSION_A], capture_output=True)
        self.assertEqual(result.returncode, 3)
        self.assertIn(b"private store", result.stderr)


if __name__ == "__main__":
    unittest.main()
