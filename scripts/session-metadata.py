#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Read allowlisted harness metadata and print heartbeat fields.

The live mode reads one explicitly supplied Codex session_index.jsonl and one
explicit source session UUID. That index supplies names only; model, effort and
account stay unknown. Fixture mode exercises adapter logic with synthetic data.
Neither mode reads transcripts or auth stores.
Live reads require physical paths on Linux or Darwin; only Darwin's exact
root /var and /tmp system aliases are supported. Other platforms fail closed.
The live index must be a single-link regular file owned by the caller on its
directory's device. A blank or unprintable name is not a rename: it yields no
label, so an unavailable or partial read can never clear the Aeon label.

stdout is one JSON object:
  harness, optional thread_title, model, reasoning_effort,
  changed, heartbeat_args

Fixture heartbeat_args uses --model and --effort. Live index mode emits only
--label; it cannot establish the current model, reasoning effort or account.
"""

from __future__ import annotations

import argparse
import errno
import json
import os
import stat
import sys
import uuid
from pathlib import Path

HARNESS_FILES = {
    "codex": ("session_meta.json", "session_index.jsonl", "config.toml"),
    "claude": ("claude_session.json", "config.toml"),
}
TITLE_KEYS = ("thread_title", "thread_name", "sessionName", "customTitle", "title")
EFFORT_KEYS = ("reasoning_effort", "model_reasoning_effort", "effort")
FIELDS = ("thread_title", "model", "reasoning_effort")
LIMITS = {"thread_title": 128, "model": 120, "reasoning_effort": 40}
FORBIDDEN_COMPONENTS = {
    ".ssh", ".inspr", ".aws", ".gnupg", ".codex", ".claude", "credentials",
}
MAX_BYTES = 65536
MAX_LINES = 500
MAX_LINE = 4096
MAX_DEPTH = 4


class Refusal(Exception):
    """The source is outside the allowlist. Nothing from it is printed."""


def clean(value: str, limit: int) -> str:
    kept = []
    for char in value:
        if char.isprintable() and char != "\x7f":
            kept.append(char)
        if len(kept) == limit:
            break
    return "".join(kept).strip()


def assert_safe(path: Path) -> Path:
    resolved = path.expanduser().resolve()
    if any(part in FORBIDDEN_COMPONENTS or part.startswith(".env") for part in resolved.parts):
        raise Refusal("refusing a live harness directory or auth store")
    home = Path.home().resolve()
    if resolved == home:
        raise Refusal("refusing the home directory")
    return resolved


def read_text(path: Path) -> str:
    if path.is_symlink():
        raise Refusal(f"refusing symlink {path.name}")
    if path.stat().st_size > MAX_BYTES:
        raise Refusal(f"refusing oversized metadata file {path.name}")
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeError as exc:
        raise Refusal(f"metadata file is not utf-8: {path.name}") from exc


def take(mapping: dict, keys: tuple[str, ...], limit: int) -> str:
    for key in keys:
        value = mapping.get(key)
        if isinstance(value, str):
            cleaned = clean(value, limit)
            if cleaned:
                return cleaned
    return ""


def fields_from(document: dict) -> dict[str, str]:
    payload = document.get("payload")
    sources = [payload, document] if isinstance(payload, dict) else [document]
    found = {}
    for source in sources:
        if "thread_title" not in found:
            title = take(source, TITLE_KEYS, LIMITS["thread_title"])
            if title:
                found["thread_title"] = title
        if "model" not in found:
            model = take(source, ("model",), LIMITS["model"])
            if model:
                found["model"] = model
        if "reasoning_effort" not in found:
            effort = take(source, EFFORT_KEYS, LIMITS["reasoning_effort"])
            if effort:
                found["reasoning_effort"] = effort
    return found


def document_id(document: dict) -> str:
    if isinstance(document.get("id"), str):
        return document["id"]
    payload = document.get("payload")
    if isinstance(payload, dict) and isinstance(payload.get("id"), str):
        return payload["id"]
    return ""


def accept(document: dict, session_id: str, saw: dict[str, bool], indexed: bool) -> dict[str, str]:
    identity = document_id(document)
    if identity:
        saw["ids"] = True
        if session_id and identity != session_id:
            return {}
        saw["matched"] = True
    elif session_id and indexed:
        return {}
    return fields_from(document)


def toml_fields(text: str) -> dict[str, str]:
    found = {}
    for raw in text.splitlines():
        line = raw.strip()
        if line.startswith("["):
            break
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = (part.strip() for part in line.split("=", 1))
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        if key == "model" and "model" not in found:
            model = clean(value, LIMITS["model"])
            if model:
                found["model"] = model
        elif key in {"model_reasoning_effort", "reasoning_effort", "effort"} and "reasoning_effort" not in found:
            effort = clean(value, LIMITS["reasoning_effort"])
            if effort:
                found["reasoning_effort"] = effort
    return found


def discover(root: Path, names: tuple[str, ...]) -> list[Path]:
    found = []
    for dirpath, dirnames, filenames in os.walk(root):
        directory = Path(dirpath)
        depth = len(directory.relative_to(root).parts)
        dirnames[:] = [
            name for name in dirnames
            if name not in FORBIDDEN_COMPONENTS and not name.startswith(".env") and not (directory / name).is_symlink()
        ]
        if depth >= MAX_DEPTH:
            dirnames[:] = []
        for name in filenames:
            if name not in names:
                continue
            path = directory / name
            if not path.is_symlink():
                found.append(path)
    return sorted(found)


def read_previous(path: Path | None) -> dict[str, str]:
    if path is None:
        return {}
    safe = assert_safe(path)
    if not safe.is_file():
        raise Refusal("previous metadata file is missing")
    try:
        document = json.loads(read_text(safe))
    except json.JSONDecodeError as exc:
        raise Refusal("previous metadata file is not JSON") from exc
    if not isinstance(document, dict):
        raise Refusal("previous metadata file is not an object")
    return {key: value for key, value in fields_from(document).items()}


def open_codex_index(path: Path) -> int:
    """Pin the explicit live index; the caller owns and must fstat the fd.

    This live-only policy permits .codex. Fixture paths retain assert_safe's
    stricter policy. Never resolve caller symlinks, even seemingly benign ones.
    """
    if (sys.platform not in {"linux", "darwin"}
            or os.open not in os.supports_dir_fd
            or (sys.platform == "darwin" and os.readlink not in os.supports_dir_fd)
            or not all(getattr(os, flag, 0) for flag in
                       ("O_NOFOLLOW", "O_DIRECTORY", "O_NONBLOCK", "O_CLOEXEC"))):
        raise Refusal("safe live index reads require Linux or Darwin descriptor support")
    absolute = path.expanduser().absolute()
    if absolute.name != "session_index.jsonl":
        raise Refusal("live source must be the explicit session_index.jsonl")
    if any(part.casefold() in FORBIDDEN_COMPONENTS - {".codex"}
           or part.casefold().startswith(".env") for part in absolute.parts):
        raise Refusal("refusing a private store as the live index source")
    if absolute.anchor != "/" or ".." in absolute.parts or "\0" in str(absolute):
        raise Refusal("live index requires a physical path without parent traversal")
    parts = absolute.parts[1:]
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC
    parent = os.open("/", flags | os.O_DIRECTORY)
    try:
        if sys.platform == "darwin" and parts[0] in {"var", "tmp"}:
            # Inspect only the root alias itself, relative to the pinned root.
            # Substitute an exact known spelling; never open through the link.
            # A later alias swap cannot redirect the walk, and /private must
            # itself pass O_DIRECTORY|O_NOFOLLOW just like every other ancestor.
            try:
                target = os.readlink(parts[0], dir_fd=parent)
            except OSError as exc:
                if exc.errno != errno.EINVAL:  # A real directory needs no alias.
                    raise
            else:
                if target not in {"private/" + parts[0], "/private/" + parts[0]}:
                    raise Refusal("refusing a custom root alias in the live index path")
                parts = ("private", *parts)
        for part in parts[:-1]:
            child = os.open(part, flags | os.O_DIRECTORY, dir_fd=parent)
            os.close(parent)
            parent = child
        leaf = os.open(parts[-1], flags, dir_fd=parent)
        try:
            # A file mounted over the leaf is not the directory's own index.
            if os.fstat(leaf).st_dev != os.fstat(parent).st_dev:
                raise Refusal("live index must be on its directory's device")
        except BaseException:
            os.close(leaf)
            raise
        return leaf
    finally:
        os.close(parent)


def codex_index(path: Path, session_id: str) -> dict[str, str]:
    """Read a bounded name index, never a rollout or global model default."""
    try:
        if str(uuid.UUID(session_id)) != session_id:
            raise ValueError
    except ValueError as exc:
        raise Refusal("live metadata requires an explicit canonical source session UUID") from exc
    try:
        fd = open_codex_index(path)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or not 0 <= info.st_size <= MAX_BYTES:
                raise Refusal("live index must be a bounded regular file")
            # A second link can give a private file an allowed name and path.
            if info.st_nlink != 1 or info.st_uid != os.getuid():
                raise Refusal("live index must be a single-link file owned by this user")
            # Bound bytes, including growth after fstat and short reads. A text
            # wrapper's character limit can consume more than MAX_BYTES bytes.
            body = bytearray()
            while len(body) <= MAX_BYTES:
                chunk = os.read(fd, MAX_BYTES + 1 - len(body))
                if not chunk:
                    break
                body.extend(chunk)
        finally:
            os.close(fd)
        if len(body) > MAX_BYTES:
            raise Refusal("live name index exceeds the byte limit")
        text = body.decode("utf-8")
    except (OSError, UnicodeError, NotImplementedError) as exc:
        raise Refusal("live name index is unavailable or unreadable") from exc
    lines = text.splitlines()
    if len(lines) > MAX_LINES:
        raise Refusal("live name index exceeds the record limit")
    found: dict[str, str] = {}
    for line in lines:
        if not line.strip():
            continue
        if len(line) > MAX_LINE:
            raise Refusal("live name index contains an oversized record")
        try:
            document = json.loads(line)
        except json.JSONDecodeError as exc:
            raise Refusal("live name index has an incomplete or invalid record; retry later") from exc
        if not isinstance(document, dict) or set(document) - {"id", "thread_name", "updated_at"}:
            raise Refusal("live name index has an unsupported record shape")
        if document.get("id") == session_id and isinstance(document.get("thread_name"), str):
            # The name index is append-only; the last matching entry is current.
            # A blank current entry means the name is unknown, not "clear it".
            name = clean(document["thread_name"], LIMITS["thread_title"])
            found = {"thread_title": name} if name else {}
    return found


def detect(root: Path, harness: str, session_id: str) -> dict[str, str]:
    names = HARNESS_FILES[harness]
    saw = {"ids": False, "matched": False}
    merged: dict[str, str] = {}
    config: dict[str, str] = {}
    for path in discover(root, names):
        text = read_text(path)
        if path.name == "config.toml":
            config = toml_fields(text)
            continue
        if path.name.endswith(".jsonl"):
            for index, line in enumerate(text.splitlines()):
                if index >= MAX_LINES:
                    break
                if len(line) > MAX_LINE or not line.strip():
                    continue
                try:
                    document = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if isinstance(document, dict):
                    merged.update(accept(document, session_id, saw, True))
            continue
        try:
            document = json.loads(text)
        except json.JSONDecodeError as exc:
            raise Refusal(f"metadata file is not JSON: {path.name}") from exc
        if isinstance(document, dict):
            merged.update(accept(document, session_id, saw, False))
    if session_id and saw["ids"] and not saw["matched"]:
        return {}
    # A shared config is the model source for one fixture session. It is not
    # applied to a selected thread, where another session's global value would
    # be the wrong model.
    if not session_id:
        for key, value in config.items():
            merged.setdefault(key, value)
    return merged


def heartbeat_args(current: dict[str, str], previous: dict[str, str]) -> tuple[list[str], list[str]]:
    changed = [key for key in FIELDS if key in current and current[key] != previous.get(key)]
    args = []
    if "model" in changed:
        args.extend(["--model", current["model"]])
    if "reasoning_effort" in changed:
        args.extend(["--effort", current["reasoning_effort"]])
    return changed, args


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Detect allowlisted harness session metadata")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--fixture", type=Path, help="synthetic metadata directory")
    source.add_argument("--codex-index", type=Path, help="one explicit live session_index.jsonl; names only")
    parser.add_argument("--harness", required=True, choices=sorted(HARNESS_FILES))
    parser.add_argument("--session-id", default="", help="select one indexed session")
    parser.add_argument("--previous", type=Path, help="earlier JSON output from this command")
    parser.add_argument("--null-args", action="store_true", help="emit only safe NUL-delimited heartbeat arguments")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        if args.codex_index:
            if args.harness != "codex" or args.previous:
                raise Refusal("live index requires Codex; previous fixture state is not live evidence")
            current = codex_index(args.codex_index, args.session_id)
            changed = list(current)
            # Send the current name every time. Aeon deduplicates changes and a
            # failed heartbeat cannot suppress the next retry via a local cache.
            args_out = ["--label", current["thread_title"]] if "thread_title" in current else []
        else:
            root = assert_safe(args.fixture)
            if not root.is_dir():
                raise Refusal("fixture must be a directory")
            current = detect(root, args.harness, args.session_id)
            previous = read_previous(args.previous)
            changed, args_out = heartbeat_args(current, previous)
    except Refusal as exc:
        print(str(exc), file=sys.stderr)
        return 3
    document = {"harness": args.harness}
    for key in FIELDS:
        if key in current:
            document[key] = current[key]
    document["changed"] = changed
    document["heartbeat_args"] = args_out
    if args.codex_index:
        document.update(source="codex-name-index", source_session_id=args.session_id,
                        capture_status="partial" if current else "unavailable",
                        missing_fields=[key for key in FIELDS if key not in current])
    if args.null_args:
        for argument in args_out:
            sys.stdout.buffer.write(argument.encode("utf-8") + b"\0")
        return 0
    print(json.dumps(document, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
