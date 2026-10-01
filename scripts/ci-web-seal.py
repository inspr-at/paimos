#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Seal installed Playwright code against integrity-checked lockfile archives."""
import base64
import hashlib
import io
import json
import sys
import tarfile
import urllib.parse
import urllib.request
from pathlib import Path


def seal(web, lock_hash):
    lock_bytes = (web / "package-lock.json").read_bytes()
    if hashlib.sha256(lock_bytes).hexdigest() != lock_hash:
        raise ValueError("Playwright lockfile changed after snapshot")
    packages = json.loads(lock_bytes)["packages"]
    manifest = {}
    for name in ("@playwright/test", "playwright", "playwright-core"):
        prefix = "node_modules/" + name
        package = packages[prefix]
        url = urllib.parse.urlparse(package["resolved"])
        if (url.scheme != "https" or url.netloc != "registry.npmjs.org"
                or url.query or url.fragment):
            raise ValueError("Playwright archive must use the npm registry")
        archive = urllib.request.urlopen(package["resolved"], timeout=60).read()
        algorithm, expected = package["integrity"].split("-", 1)
        if algorithm != "sha512" or base64.b64encode(hashlib.sha512(archive).digest()).decode() != expected:
            raise ValueError("Playwright archive integrity mismatch")
        with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tar:
            for member in tar:
                parts = member.name.split("/")
                if parts[0] != "package" or any(p in ("", ".", "..") for p in parts[1:]):
                    raise ValueError("Invalid Playwright archive path")
                if not member.isfile() or not member.name.endswith((".js", ".cjs", ".mjs", ".json")):
                    continue
                relative = prefix + "/" + "/".join(parts[1:])
                digest = hashlib.sha256(tar.extractfile(member).read()).hexdigest()
                installed = web / relative
                if installed.is_symlink() or installed.resolve() != web.resolve() / relative:
                    raise ValueError("Playwright code must be a regular installed file")
                if hashlib.sha256(installed.read_bytes()).hexdigest() != digest:
                    raise ValueError("Installed Playwright differs from lockfile archive: " + relative)
                manifest[relative] = digest
    if "node_modules/@playwright/test/cli.js" not in manifest:
        raise ValueError("Missing Playwright launcher")
    return manifest


if __name__ == "__main__":
    print("manifest=" + json.dumps(seal(Path(sys.argv[1]), sys.argv[2]), sort_keys=True, separators=(",", ":")))
