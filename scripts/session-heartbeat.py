#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Heartbeat one Aeon harness session and keep its name in step with the harness.

  bind  record which native source session belongs to one Aeon session
  run   heartbeat that Aeon session through the Aeon CLI; add --label when
        the bound source session has a current name

A binding is explicit: the source session UUID comes from the launcher (the
documented thread.started event of `codex exec --json`) or from the operator
(for example the session id Codex shows in /status). It is never inferred from
a window title, working directory, most recent session or shared agent. Every
beat re-reads the binding and sends a name only when the binding names this
Aeon session, so a rebound or replaced generation cannot rename another one.

Only Codex has a supported name source: its session name index, read through
session-metadata.py's descriptor-pinned reader. Other harnesses are refused,
not approximated. A missing, blank, refused or partial read omits --label; it
never clears the Aeon label. The current name is sent on every beat without a
local cache: Aeon records a history entry only for a change, and a failed beat
cannot suppress the next attempt.

The producer never opens the worker lease, an API key or harness auth state.
It passes the lease path to the CLI, which inherits the caller's environment;
it discards CLI output and logs one value-free JSON line per beat. It advances
the activity sequence from a fresh status read, so it replaces a session's
heartbeat loop rather than running beside another one.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import stat
import subprocess
import sys
import time
import uuid
from pathlib import Path

# Runs from a repository checkout; it must not leave __pycache__ beside itself.
sys.dont_write_bytecode = True
_SPEC = importlib.util.spec_from_file_location(
    "session_metadata", Path(__file__).resolve().parent / "session-metadata.py")
METADATA = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(METADATA)
Refusal = METADATA.Refusal

SCHEMA = "aeon.native-session-binding.v1"
BINDING_KEYS = {"schema", "harness", "aeon_session_id", "source_session_id"}
HARNESSES = ("claude", "codex", "cursor", "grok", "pi")
NAME_SOURCES = {"codex"}
MAX_BINDING = 4096
MAX_EVENT_LINE = 4096
MAX_STATUS = 1 << 20
CLI_TIMEOUT = 60
ACTIVE_PHASES = {"starting", "working", "yielded"}
PASS_THROUGH = ("account-label", "branch", "brief", "effort", "harness-version", "model", "note", "worktree")


def canonical_uuid(value: object) -> bool:
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value
    except ValueError:
        return False


def unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    document: dict[str, object] = {}
    for key, value in pairs:
        if key in document:
            raise ValueError("duplicate key")
        document[key] = value
    return document


def verified(document: object) -> dict[str, str]:
    if not isinstance(document, dict) or set(document) != BINDING_KEYS or document["schema"] != SCHEMA:
        raise Refusal("binding has an unsupported shape")
    if document["harness"] not in NAME_SOURCES:
        raise Refusal("binding harness has no supported name source")
    if not canonical_uuid(document["aeon_session_id"]) or not canonical_uuid(document["source_session_id"]):
        raise Refusal("binding session ids must be canonical UUIDs")
    return document


def read_binding(path: Path) -> dict[str, str]:
    """Return a verified binding; a link, shared or oversized file is refused."""
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC
    try:
        fd = os.open(path, flags)
    except OSError as exc:
        raise Refusal("binding is unavailable") from exc
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_BINDING:
            raise Refusal("binding must be a small regular file")
        if info.st_nlink != 1 or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise Refusal("binding must be a private single-link file owned by this user")
        raw = bytearray()
        while len(raw) <= MAX_BINDING:
            chunk = os.read(fd, MAX_BINDING + 1 - len(raw))
            if not chunk:
                break
            raw.extend(chunk)
    except OSError as exc:
        raise Refusal("binding is unreadable") from exc
    finally:
        os.close(fd)
    if len(raw) > MAX_BINDING:
        raise Refusal("binding exceeds the byte limit")
    try:
        document = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    except (UnicodeError, ValueError) as exc:
        raise Refusal("binding is not one complete JSON object") from exc
    return verified(document)


def write_binding(path: Path, harness: str, aeon_session: str, source_session: str, replace: bool) -> None:
    if harness not in NAME_SOURCES:
        raise Refusal(f"{harness} has no supported safe session name source; name sync stays unsupported")
    document = verified({"schema": SCHEMA, "harness": harness,
                         "aeon_session_id": aeon_session, "source_session_id": source_session})
    directory = path.parent
    try:
        info = os.lstat(directory)
    except OSError as exc:
        raise Refusal("binding directory is unavailable") from exc
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o022:
        raise Refusal("binding directory must be a directory owned by this user and writable only by it")
    body = (json.dumps(document, separators=(",", ":")) + "\n").encode()
    # A replacement is staged and renamed so a reader never sees a partial file.
    target = directory / (f".{path.name}.{os.getpid()}.tmp" if replace else path.name)
    try:
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    except FileExistsError as exc:
        raise Refusal("binding already exists; rebinding needs --replace") from exc
    except OSError as exc:
        raise Refusal("binding could not be created") from exc
    try:
        view = memoryview(body)
        while view:
            view = view[os.write(fd, view):]
        os.fsync(fd)
    finally:
        os.close(fd)
    if replace:
        try:
            os.replace(target, path)
        except OSError as exc:
            os.unlink(target)
            raise Refusal("binding could not be replaced") from exc


def thread_started(line: bytes) -> str:
    """Return the thread id of one complete `codex exec --json` start event."""
    if len(line) > MAX_EVENT_LINE or not line.endswith(b"\n"):
        return ""
    try:
        event = json.loads(line.decode("utf-8"), object_pairs_hook=unique_object)
    except (UnicodeError, ValueError):
        return ""
    if not isinstance(event, dict) or event.get("type") != "thread.started":
        return ""
    thread = event.get("thread_id")
    return thread if canonical_uuid(thread) else ""


def bind_from_exec_events(options: argparse.Namespace, stdin, stdout) -> int:
    """Bind from the first event line, then copy the stream through unparsed.

    Only the first line is ever decoded. The worker's output continues to its
    log whatever happens to the binding, so a refusal cannot stall Codex.
    """
    first = stdin.readline(MAX_EVENT_LINE + 1)
    source = thread_started(first)
    code = 3
    try:
        if not source:
            raise Refusal("the first codex exec event is not thread.started; no binding written")
        write_binding(options.binding, "codex", options.aeon_session, source, options.replace)
        code = 0
    except Refusal as exc:
        print(str(exc), file=sys.stderr, flush=True)
    try:
        stdout.write(first)
        stdout.flush()
        while chunk := stdin.read1(65536):
            stdout.write(chunk)
            stdout.flush()
    except BrokenPipeError:
        return code or 1
    return code


def pass_through(extra: list[str]) -> list[str]:
    """Normalise optional fixed heartbeat metadata to --name=value arguments."""
    result, index = [], 0
    while index < len(extra):
        flag = extra[index]
        name, has_value, value = flag[2:].partition("=") if flag.startswith("--") else ("", "", "")
        if name not in PASS_THROUGH:
            raise ValueError("heartbeat pass-through accepts only --" + ", --".join(PASS_THROUGH))
        if not has_value:
            index += 1
            if index == len(extra):
                raise ValueError(f"--{name} needs a value")
            value = extra[index]
        result.append(f"--{name}={value}")
        index += 1
    return result


def current_label(options: argparse.Namespace) -> tuple[str, str]:
    try:
        binding = read_binding(options.binding)
    except Refusal:
        return "", "unbound"
    if binding["aeon_session_id"] != options.session:
        return "", "bound-elsewhere"
    try:
        found = METADATA.codex_index(options.codex_index, binding["source_session_id"])
    except Refusal:
        return "", "unavailable"
    name = found.get("thread_title", "")
    return (name, "included") if name else ("", "unnamed")


def call(runner, command: list[str], capture: bool = False):
    try:
        return runner(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                      stderr=subprocess.DEVNULL, timeout=CLI_TIMEOUT, check=False)
    except (OSError, subprocess.SubprocessError):
        return None


def read_status(options: argparse.Namespace, runner) -> dict | None:
    result = call(runner, [options.aeon, "--json", "harness", "status",
                           f"--project={options.project}", f"--session={options.session}"], capture=True)
    if result is None or result.returncode != 0 or len(result.stdout) > MAX_STATUS:
        return None
    try:
        status = json.loads(result.stdout)
    except (UnicodeError, ValueError):
        return None
    if not isinstance(status, dict) or status.get("id") != options.session:
        return None
    sequence = status.get("activity_sequence")
    if not isinstance(sequence, int) or isinstance(sequence, bool) or sequence < 0:
        return None
    return status


def active(status: dict) -> bool:
    return status.get("stopped_at") is None and status.get("archived_at") is None and status.get("phase") in ACTIVE_PHASES


def heartbeat_command(options: argparse.Namespace, status: dict, label: str) -> list[str]:
    command = [options.aeon, "harness", "heartbeat", f"--project={options.project}", f"--session={options.session}",
               f"--agent={options.agent}", f"--worker-lease-file={options.worker_lease_file}",
               f"--phase={options.phase}", f"--activity={options.activity}",
               f"--activity-sequence={status['activity_sequence'] + 1}"]
    if label:
        command.append(f"--label={label}")
    return command + options.extra


def beat(options: argparse.Namespace, runner) -> dict[str, str]:
    label, label_state = current_label(options)
    status = read_status(options, runner)
    if status is None:
        return {"heartbeat": "skipped", "label": label_state, "reason": "status unavailable"}
    if not active(status):
        return {"heartbeat": "stopped", "label": label_state}
    for _ in range(2):
        result = call(runner, heartbeat_command(options, status, label))
        if result is not None and result.returncode == 0:
            return {"heartbeat": "accepted", "label": label_state}
        latest = read_status(options, runner)
        if latest is None:
            break
        if not active(latest):
            return {"heartbeat": "stopped", "label": label_state}
        # Retry at once only when another reporter advanced the sequence.
        if latest["activity_sequence"] == status["activity_sequence"]:
            break
        status = latest
    return {"heartbeat": "rejected", "label": label_state}


def run(options: argparse.Namespace, runner=subprocess.run, sleep=time.sleep, out=None) -> int:
    out = out or sys.stdout
    count = 0
    while True:
        count += 1
        record = {"beat": count, **beat(options, runner)}
        print(json.dumps(record, separators=(",", ":")), file=out, flush=True)
        if record["heartbeat"] == "stopped":
            return 0
        if options.beats and count >= options.beats:
            return 0 if record["heartbeat"] == "accepted" else 1
        sleep(options.interval)


def parse_args(argv: list[str]) -> argparse.Namespace:
    extra: list[str] = []
    if "--" in argv:
        split = argv.index("--")
        argv, extra = argv[:split], argv[split + 1:]
    parser = argparse.ArgumentParser(description="Heartbeat one Aeon session with its bound harness name")
    commands = parser.add_subparsers(dest="command", required=True)

    bind = commands.add_parser("bind", help="record the native source session of one Aeon session")
    bind.add_argument("--binding", type=Path, required=True, help="private binding file to create")
    bind.add_argument("--aeon-session", required=True, help="public Aeon harness session UUID")
    bind.add_argument("--harness", choices=HARNESSES, default="codex")
    source = bind.add_mutually_exclusive_group(required=True)
    source.add_argument("--source-session", help="explicit native session UUID")
    source.add_argument("--from-codex-exec-events", action="store_true",
                        help="take the thread id from the first `codex exec --json` event on stdin, then copy stdin to stdout")
    bind.add_argument("--replace", action="store_true", help="atomically replace an existing binding")

    beat_parser = commands.add_parser("run", help="heartbeat an Aeon session and send its bound name")
    beat_parser.add_argument("--aeon", required=True, help="absolute path of the Aeon CLI")
    beat_parser.add_argument("--project", required=True, help="project key")
    beat_parser.add_argument("--session", required=True, help="public Aeon harness session UUID")
    beat_parser.add_argument("--agent", required=True, help="authenticated agent name")
    beat_parser.add_argument("--worker-lease-file", required=True, help="private lease file, passed to the CLI unread")
    beat_parser.add_argument("--binding", type=Path, required=True, help="binding written by `bind`")
    beat_parser.add_argument("--codex-index", type=Path, required=True, help="the explicit Codex session_index.jsonl")
    beat_parser.add_argument("--phase", choices=sorted(ACTIVE_PHASES), default="working")
    beat_parser.add_argument("--activity", choices=("busy", "idle", "throttled", "unknown"), default="busy")
    beat_parser.add_argument("--interval", type=int, default=60, help="seconds between beats (5-3600)")
    beat_parser.add_argument("--beats", type=int, default=0, help="stop after this many beats (0 runs until the session stops)")
    beat_parser.add_argument("--once", action="store_true", help="send exactly one beat")
    options = parser.parse_args(argv)

    if options.command == "bind":
        if not canonical_uuid(options.aeon_session):
            parser.error("--aeon-session must be a canonical UUID")
        if options.source_session is not None and not canonical_uuid(options.source_session):
            parser.error("--source-session must be a canonical UUID")
        if options.from_codex_exec_events and options.harness != "codex":
            parser.error("--from-codex-exec-events binds only codex sessions")
        if extra:
            parser.error("bind takes no pass-through arguments")
        return options
    if not canonical_uuid(options.session):
        parser.error("--session must be a canonical UUID")
    if not os.path.isabs(options.aeon):
        parser.error("--aeon must be an absolute path")
    if not 5 <= options.interval <= 3600 or options.beats < 0:
        parser.error("--interval must be 5-3600 seconds and --beats non-negative")
    if options.once:
        options.beats = 1
    try:
        options.extra = pass_through(extra)
    except ValueError as exc:
        parser.error(str(exc))
    return options


def main(argv: list[str] | None = None) -> int:
    options = parse_args(list(sys.argv[1:] if argv is None else argv))
    try:
        if options.command == "run":
            return run(options)
        if options.from_codex_exec_events:
            return bind_from_exec_events(options, sys.stdin.buffer, sys.stdout.buffer)
        write_binding(options.binding, options.harness, options.aeon_session, options.source_session, options.replace)
    except Refusal as exc:
        print(str(exc), file=sys.stderr)
        return 3
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
