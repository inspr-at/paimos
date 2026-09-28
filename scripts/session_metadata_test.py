#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Synthetic-fixture tests for the harness metadata adapter."""

from __future__ import annotations

import errno
import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve().parent / "session-metadata.py"
SECRET = "synthetic-secret-value"
SESSION_A = "11111111-1111-4111-8111-111111111111"
SESSION_B = "22222222-2222-4222-8222-222222222222"
SPEC = importlib.util.spec_from_file_location("session_metadata", SCRIPT)
METADATA = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(METADATA)


class IndexIOProbe:
    """Real descriptor IO; hooks only schedule deterministic synthetic swaps."""

    def __init__(self, test: unittest.TestCase, root: Path | None = None):
        self.test = test
        self.root = root
        self.before_open = self.after_open = self.after_readlink = self.before_read = None
        self.adjust_stat = None
        self.leaf = None
        self.opened = set()
        self.checked = set()
        self.reads = []
        self.bytes_read = 0
        self.forbidden = set()
        self.short_reads = False

    def open(self, path, flags, *, dir_fd=None):
        for flag in (os.O_NOFOLLOW, os.O_NONBLOCK, os.O_CLOEXEC):
            self.test.assertTrue(flags & flag)
        if path == "/":
            self.test.assertIsNone(dir_fd)
        else:
            self.test.assertIsNotNone(dir_fd)
            self.test.assertNotIn("/", str(path))
            self.test.assertNotEqual(path, "..")
        if path != "session_index.jsonl":
            self.test.assertTrue(flags & os.O_DIRECTORY)
        if self.before_open:
            self.before_open(path)
        fd = os.open(self.root if path == "/" and self.root else path, flags, dir_fd=dir_fd)
        self.opened.add(fd)
        self.checked.discard(fd)  # File descriptors can be reused within a walk.
        if path == "session_index.jsonl":
            self.leaf = fd
        if self.after_open:
            self.after_open(path)
        return fd

    def fstat(self, fd):
        info = os.fstat(fd)
        self.checked.add(fd)
        if self.adjust_stat and fd == self.leaf:
            info = self.adjust_stat(info)
        return info

    def readlink(self, path, *, dir_fd):
        target = os.readlink(path, dir_fd=dir_fd)
        if self.after_readlink:
            self.after_readlink(path)
        return target

    def read(self, fd, size):
        self.test.assertIn(fd, self.checked, "content read before fstat")
        info = os.fstat(fd)
        self.test.assertTrue(stat.S_ISREG(info.st_mode))
        identity = (info.st_dev, info.st_ino)
        self.test.assertNotIn(identity, self.forbidden, "forbidden target reached content reader")
        self.reads.append(identity)
        self.test.assertLessEqual(self.bytes_read + size, METADATA.MAX_BYTES + 1)
        if self.before_read:
            self.before_read()
        chunk = os.read(fd, min(size, 7) if self.short_reads else size)
        self.bytes_read += len(chunk)
        return chunk

    def forbid(self, path: Path):
        info = path.stat()
        self.forbidden.add((info.st_dev, info.st_ino))

    @contextmanager
    def intercept(self):
        proxy = SimpleNamespace(**{name: getattr(os, name) for name in (
            "O_RDONLY", "O_NOFOLLOW", "O_DIRECTORY", "O_NONBLOCK", "O_CLOEXEC", "close", "getuid",
        )})
        proxy.open, proxy.read, proxy.fstat, proxy.readlink = self.open, self.read, self.fstat, self.readlink
        proxy.supports_dir_fd = {proxy.open, proxy.readlink}
        try:
            with mock.patch.object(METADATA, "os", proxy):
                yield proxy
        finally:
            for fd in self.opened:
                with self.test.assertRaises(OSError) as closed:
                    os.fstat(fd)
                self.test.assertEqual(closed.exception.errno, errno.EBADF)


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
        outside = self.root / "outside-secret.json"
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

    def test_live_null_args_preserve_shell_metacharacters_as_data_and_never_clear(self) -> None:
        name = "Literal $(false) `false` 'quotes'"
        result = self.live([{"id": SESSION_A, "thread_name": name}], extra=["--null-args"])
        self.assertEqual(result.stdout.split(b"\0"), [b"--label", name.encode(), b""])
        for blank in ("", "   ", "\u0001\u0002", "\u200b"):
            with self.subTest(blank=repr(blank)):
                cleared = self.live([{"id": SESSION_A, "thread_name": "Named"},
                                     {"id": SESSION_A, "thread_name": blank}], extra=["--null-args"])
                self.assertEqual(cleared.returncode, 0, cleared.stderr)
                self.assertEqual(cleared.stdout, b"")
        body = json.loads(self.live([{"id": SESSION_A, "thread_name": " "}]).stdout)
        self.assertEqual((body["capture_status"], body["heartbeat_args"]), ("unavailable", []))

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


class IndexSafeOpenTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()

    def index(self, directory: Path, title: str = "approved") -> Path:
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / "session_index.jsonl"
        path.write_text(json.dumps({"id": SESSION_A, "thread_name": title}), encoding="utf-8")
        return path

    def denied(self, path: Path, probe: IndexIOProbe, session_id: str = SESSION_A):
        with probe.intercept(), self.assertRaises(METADATA.Refusal) as refused:
            METADATA.codex_index(path, session_id)
        self.assertEqual(probe.reads, [])
        self.assertNotIn(str(self.root), str(refused.exception))
        self.assertNotIn(SECRET, str(refused.exception))

    def test_ancestor_and_leaf_swaps_before_and_after_open(self):
        for component in ("public", "nested", "session_index.jsonl"):
            for after in (False, True):
                with self.subTest(component=component, after=after):
                    root = self.root / f"{component}-{after}"
                    path = self.index(root / "public" / "nested")
                    private = self.index(root / ".ssh" / "nested", "rejected")
                    # Same byte count: path-based size comparisons cannot help.
                    self.assertEqual(path.stat().st_size, private.stat().st_size)
                    source = path if component == path.name else root / "public"
                    target = private if component == path.name else root / ".ssh"
                    if component == "nested":
                        source, target = source / "nested", target / "nested"
                    swapped = []

                    def swap(name):
                        if name == component and not swapped:
                            source.rename(source.with_name(source.name + "-saved"))
                            source.symlink_to(target, target_is_directory=source != path)
                            swapped.append(True)

                    probe = IndexIOProbe(self)
                    probe.forbid(private)
                    if after:
                        probe.after_open = swap
                        expected = path.stat()
                        with probe.intercept():
                            self.assertEqual(METADATA.codex_index(path, SESSION_A), {"thread_title": "approved"})
                        self.assertTrue(probe.reads)
                        self.assertEqual(set(probe.reads), {(expected.st_dev, expected.st_ino)})
                    else:
                        probe.before_open = swap
                        self.denied(path, probe)
                    self.assertEqual(swapped, [True], "scheduled swap was not reached")

    def test_custom_ancestor_and_leaf_links_never_reach_content(self):
        for name, target_name, relative, leaf in (
            ("private", ".ssh", False, False),
            ("relative", "credentials", True, False),
            ("parent_target", "unused/../.ssh", True, False),
            ("benign", "physical", False, False),
            ("leaf", ".ssh", False, True),
        ):
            with self.subTest(name=name):
                root = self.root / name
                (root / "unused").mkdir(parents=True)
                private = self.index(root / target_name, SECRET)
                link = root / ("session_index.jsonl" if leaf else "public")
                target = (private if leaf else private.parent) if not relative else Path(target_name)
                link.symlink_to(target, target_is_directory=not leaf)
                probe = IndexIOProbe(self)
                probe.forbid(private)
                self.denied(link if leaf else link / "session_index.jsonl", probe)

    def test_private_components_are_refused_before_any_open(self):
        for component in sorted(METADATA.FORBIDDEN_COMPONENTS - {".codex"}) + [".env-local", ".SSH", "CREDENTIALS"]:
            with self.subTest(component=component):
                path = self.index(self.root / component, SECRET)
                probe = IndexIOProbe(self)
                self.denied(path, probe)
                self.assertEqual(probe.opened, set())

    def test_parent_traversal_and_non_index_names_are_refused_before_open(self):
        paths = [self.root / "public" / ".." / "session_index.jsonl",
                 self.root / "session_index.jsonl\0",
                 self.root / "rollout.jsonl", self.root / "auth.json",
                 Path("/" + str(self.root)) / "session_index.jsonl"]
        for path in paths:
            with self.subTest(path=path):
                probe = IndexIOProbe(self)
                self.denied(path, probe)
                self.assertEqual(probe.opened, set())

    def test_explicit_canonical_uuid_required_before_open(self):
        path = self.index(self.root)
        for identity in ("", "not-a-uuid", SESSION_A.replace("-", ""), "{" + SESSION_A + "}",
                         "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", " " + SESSION_A):
            with self.subTest(identity=identity):
                probe = IndexIOProbe(self)
                self.denied(path, probe, identity)
                self.assertEqual(probe.opened, set())

    def test_fixture_protections_stay_stricter_than_live_codex_index(self):
        path = self.index(self.root / ".codex")
        with IndexIOProbe(self).intercept():
            self.assertEqual(METADATA.codex_index(path, SESSION_A), {"thread_title": "approved"})
        for component in sorted(METADATA.FORBIDDEN_COMPONENTS) + [".env-local"]:
            with self.subTest(component=component):
                directory = self.root / component
                directory.mkdir(exist_ok=True)
                (directory / "session_meta.json").write_text(json.dumps({"model": SECRET}))
                with self.assertRaises(METADATA.Refusal):
                    METADATA.assert_safe(directory)
                with self.assertRaises(METADATA.Refusal):
                    METADATA.read_previous(directory / "session_meta.json")
        visible = self.root / "fixture-link"
        visible.symlink_to(self.root / ".codex", target_is_directory=True)
        self.assertEqual(run(visible).returncode, 3)
        result = run(self.root)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        self.assertNotIn("model", json.loads(result.stdout))

    def test_descriptor_type_and_size_are_checked_before_read(self):
        for kind in ("directory", "fifo", "oversize", "swapped_oversize"):
            with self.subTest(kind=kind):
                root = self.root / kind
                root.mkdir()
                path = root / "session_index.jsonl"
                probe = IndexIOProbe(self)
                if kind == "directory":
                    path.mkdir()
                elif kind == "fifo":
                    os.mkfifo(path)
                else:
                    self.index(root)

                    def grow(name):
                        if name == path.name:
                            path.rename(root / "saved-index")
                            path.write_bytes(b"x" * (METADATA.MAX_BYTES + 1))

                    if kind == "oversize":
                        grow(path.name)
                    else:
                        probe.before_open = grow
                self.denied(path, probe)

    def test_hard_linked_alias_of_a_private_file_never_reaches_content(self):
        private = self.index(self.root / "private-store", SECRET)
        public = self.root / "public"
        public.mkdir()
        os.link(private, public / "session_index.jsonl")
        probe = IndexIOProbe(self)
        probe.forbid(private)
        self.denied(public / "session_index.jsonl", probe)
        # A second name also disqualifies an otherwise ordinary index.
        ordinary = self.index(self.root / "ordinary")
        os.link(ordinary, self.root / "ordinary-copy")
        self.denied(ordinary, IndexIOProbe(self))

    def test_foreign_owner_and_other_device_are_refused_before_read(self):
        path = self.index(self.root)

        def foreign_owner(info):
            return os.stat_result((info.st_mode, info.st_ino, info.st_dev, info.st_nlink, info.st_uid + 1,
                                   info.st_gid, info.st_size, int(info.st_atime), int(info.st_mtime), int(info.st_ctime)))

        def other_device(info):
            return os.stat_result((info.st_mode, info.st_ino, info.st_dev + 1, info.st_nlink, info.st_uid,
                                   info.st_gid, info.st_size, int(info.st_atime), int(info.st_mtime), int(info.st_ctime)))

        for name, adjust in (("owner", foreign_owner), ("device", other_device)):
            with self.subTest(name=name):
                probe = IndexIOProbe(self)
                probe.adjust_stat = adjust
                self.denied(path, probe)
                self.assertIsNotNone(probe.leaf)

    def test_growth_after_fstat_is_bounded_in_bytes(self):
        for content in (b"x", "\u20ac".encode("utf-8")):
            with self.subTest(multibyte=len(content) > 1):
                path = self.index(self.root)
                probe = IndexIOProbe(self)

                def grow():
                    path.write_bytes(content * (METADATA.MAX_BYTES + 1))

                probe.before_read = grow
                with probe.intercept(), self.assertRaisesRegex(METADATA.Refusal, "byte limit"):
                    METADATA.codex_index(path, SESSION_A)
                self.assertEqual(probe.bytes_read, METADATA.MAX_BYTES + 1)

    def test_valid_index_at_byte_limit_and_short_reads(self):
        path = self.index(self.root)
        record = json.dumps({"id": SESSION_A, "thread_name": "approved"})
        path.write_bytes(((record + " " * (4095 - len(record)) + "\n") * 16).encode())
        self.assertEqual(path.stat().st_size, METADATA.MAX_BYTES)
        for short in (False, True):
            with self.subTest(short_reads=short):
                probe = IndexIOProbe(self)
                probe.short_reads = short
                with probe.intercept():
                    self.assertEqual(METADATA.codex_index(path, SESSION_A), {"thread_title": "approved"})
                self.assertEqual(probe.bytes_read, METADATA.MAX_BYTES)

    def test_read_and_stat_errors_are_redacted_and_close_descriptors(self):
        path = self.index(self.root)
        for operation in ("read", "fstat"):
            with self.subTest(operation=operation):
                probe = IndexIOProbe(self)
                with probe.intercept() as proxy:
                    setattr(proxy, operation, mock.Mock(side_effect=OSError(str(path) + SECRET)))
                    with self.assertRaises(METADATA.Refusal) as refused:
                        METADATA.codex_index(path, SESSION_A)
                self.assertNotIn(str(path), str(refused.exception))
                self.assertNotIn(SECRET, str(refused.exception))
                self.assertEqual(probe.reads, [])

    def test_unsupported_platform_and_missing_primitives_fail_closed(self):
        path = self.index(self.root)
        for platform in ("win32", "freebsd14", "unknown"):
            with self.subTest(platform=platform), mock.patch.object(METADATA.sys, "platform", platform):
                probe = IndexIOProbe(self)
                self.denied(path, probe)
                self.assertEqual(probe.opened, set())
        for capability in ("open", "readlink", "O_NOFOLLOW", "O_DIRECTORY", "O_NONBLOCK", "O_CLOEXEC"):
            with self.subTest(capability=capability), mock.patch.object(METADATA.sys, "platform", "darwin"):
                probe = IndexIOProbe(self)
                with probe.intercept() as proxy:
                    if capability in ("open", "readlink"):
                        proxy.supports_dir_fd.remove(getattr(proxy, capability))
                    else:
                        delattr(proxy, capability)
                    with self.assertRaises(METADATA.Refusal):
                        METADATA.codex_index(path, SESSION_A)
                self.assertEqual(probe.opened, set())
                self.assertEqual(probe.reads, [])

    def test_unsupported_descriptor_operation_never_falls_back(self):
        path = self.index(self.root)
        probe = IndexIOProbe(self)

        def unsupported(name):
            if name == path.name:
                raise NotImplementedError("synthetic unavailable openat")

        probe.before_open = unsupported
        self.denied(path, probe)

    def test_strict_record_allowlist_and_bounds_are_preserved(self):
        path = self.index(self.root)
        valid = {"id": SESSION_A, "thread_name": "approved"}
        cases = [json.dumps({**valid, key: SECRET}) for key in (
            "model", "reasoning_effort", "messages", "payload", "auth", "cwd",
        )]
        cases += ["[]", "{", json.dumps(valid) + "\n" + json.dumps({"id": SESSION_B, "prompt": SECRET}),
                  json.dumps(valid) + "\n" * (METADATA.MAX_LINES + 1),
                  json.dumps({**valid, "thread_name": "x" * (METADATA.MAX_LINE + 1)})]
        for content in cases:
            with self.subTest(case=cases.index(content)):
                path.write_text(content)
                with IndexIOProbe(self).intercept(), self.assertRaises(METADATA.Refusal) as refused:
                    METADATA.codex_index(path, SESSION_A)
                self.assertNotIn(SECRET, str(refused.exception))
        path.write_bytes(b"\xff\xfe")
        with IndexIOProbe(self).intercept(), self.assertRaisesRegex(METADATA.Refusal, "unreadable"):
            METADATA.codex_index(path, SESSION_A)

    def test_darwin_root_alias_targets_and_swaps_in_synthetic_root(self):
        for alias in ("var", "tmp"):
            for case in ("relative", "absolute", "directory", "private_target", "cleaned_target",
                         "alias_swap", "private_prefix_link", "private_child_link"):
                with self.subTest(alias=alias, case=case):
                    root = self.root / alias / case
                    approved = self.index(root / "private" / alias)
                    private = self.index(root / ".ssh", SECRET)
                    private_child = self.index(root / ".ssh" / alias, SECRET)
                    link = root / alias
                    if case == "directory":
                        approved = self.index(link)
                    else:
                        target = {
                            "absolute": "/private/" + alias,
                            "private_target": ".ssh",
                            "cleaned_target": "private/" + alias + "/../../.ssh",
                        }.get(case, "private/" + alias)
                        link.symlink_to(target, target_is_directory=True)
                    if case == "private_prefix_link":
                        (root / "private").rename(root / "saved-private")
                        (root / "private").symlink_to(".ssh", target_is_directory=True)
                    if case == "private_child_link":
                        approved.parent.rename(root / "saved-child")
                        (root / "private" / alias).symlink_to(root / ".ssh", target_is_directory=True)
                    probe = IndexIOProbe(self, root)
                    probe.forbid(private)
                    probe.forbid(private_child)
                    swaps = []

                    def swap_alias(name):
                        self.assertEqual(name, alias)
                        link.rename(root / "saved-alias")
                        link.symlink_to(".ssh", target_is_directory=True)
                        swaps.append(True)

                    if case == "alias_swap":
                        probe.after_readlink = swap_alias
                    path = Path("/") / alias / "session_index.jsonl"
                    with mock.patch.object(METADATA.sys, "platform", "darwin"):
                        if case in {"relative", "absolute", "directory", "alias_swap"}:
                            expected = approved.stat()
                            with probe.intercept():
                                self.assertEqual(METADATA.codex_index(path, SESSION_A), {"thread_title": "approved"})
                            self.assertTrue(probe.reads)
                            self.assertEqual(set(probe.reads), {(expected.st_dev, expected.st_ino)})
                        else:
                            self.denied(path, probe)
                    if case == "alias_swap":
                        self.assertEqual(swaps, [True])

    def test_root_alias_exception_is_darwin_only_and_never_applies_to_nested_links(self):
        self.index(self.root / "private" / "tmp")
        (self.root / "tmp").symlink_to("private/tmp", target_is_directory=True)
        (self.root / "nested").mkdir()
        (self.root / "nested" / "tmp").symlink_to("../private/tmp", target_is_directory=True)
        for platform, path in (("linux", "/tmp/session_index.jsonl"),
                               ("darwin", "/nested/tmp/session_index.jsonl")):
            with self.subTest(platform=platform), mock.patch.object(METADATA.sys, "platform", platform):
                self.denied(Path(path), IndexIOProbe(self, self.root))

    @unittest.skipUnless(sys.platform == "darwin", "native Darwin root aliases")
    def test_native_darwin_temp_aliases_and_physical_spellings(self):
        for parent in ("/tmp", "/var/tmp"):
            with self.subTest(parent=parent), tempfile.TemporaryDirectory(dir=parent) as temporary:
                path = self.index(Path(temporary))
                for spelling in (path, path.resolve()):
                    with IndexIOProbe(self).intercept():
                        self.assertEqual(METADATA.codex_index(spelling, SESSION_A), {"thread_title": "approved"})


if __name__ == "__main__":
    unittest.main()
