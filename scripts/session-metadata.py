#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Read allowlisted harness metadata and print heartbeat fields.

The command accepts a synthetic fixture directory only. It copies thread title,
model, and reasoning effort from an allowlist of file names and keys. It does
not read transcripts, auth stores, or live harness directories.

stdout is one JSON object:
  harness, optional thread_title, model, reasoning_effort,
  changed, heartbeat_args

heartbeat_args uses the existing CLI flags --model and --effort. thread_title
is for the session-rename --label flag and is never placed in heartbeat_args.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
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
    """The fixture is outside the allowlist. Nothing from it is printed."""


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
    parser.add_argument("--fixture", required=True, type=Path, help="synthetic metadata directory")
    parser.add_argument("--harness", required=True, choices=sorted(HARNESS_FILES))
    parser.add_argument("--session-id", default="", help="select one indexed session")
    parser.add_argument("--previous", type=Path, help="earlier JSON output from this command")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
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
    print(json.dumps(document, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
