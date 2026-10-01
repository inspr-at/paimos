# SPDX-License-Identifier: AGPL-3.0-only
import base64
import hashlib
import importlib.util
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("sealer", Path(__file__).with_name("ci-web-seal.py"))
sealer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sealer)


class SealTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.web = Path(self.temp.name)
        self.code = b"console.log('real launcher');\n"
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as tar:
            member = tarfile.TarInfo("package/cli.js")
            member.size = len(self.code)
            tar.addfile(member, io.BytesIO(self.code))
        self.archive = archive.getvalue()
        self.packages = {}
        for name in ("@playwright/test", "playwright", "playwright-core"):
            prefix = "node_modules/" + name
            path = self.web / prefix / "cli.js"
            path.parent.mkdir(parents=True)
            path.write_bytes(self.code)
            self.packages[prefix] = {
                "resolved": "https://registry.npmjs.org/" + name + "/-/fixture.tgz",
                "integrity": "sha512-" + base64.b64encode(hashlib.sha512(self.archive).digest()).decode(),
            }
        self.save_lock()

    def save_lock(self):
        body = json.dumps({"packages": self.packages}).encode()
        (self.web / "package-lock.json").write_bytes(body)
        self.lock_hash = hashlib.sha256(body).hexdigest()

    def seal(self, archive=None):
        with patch.object(sealer.urllib.request, "urlopen", side_effect=lambda *a, **kw: io.BytesIO(archive or self.archive)):
            return sealer.seal(self.web, self.lock_hash)

    def test_seals_archive_bytes(self):
        result = self.seal()
        self.assertEqual(len(result), 3)
        self.assertEqual(result["node_modules/@playwright/test/cli.js"], hashlib.sha256(self.code).hexdigest())

    def test_rejects_replaced_installed_launcher(self):
        (self.web / "node_modules/@playwright/test/cli.js").write_text("console.log('forged JSON');\n")
        with self.assertRaisesRegex(ValueError, "differs from lockfile archive"):
            self.seal()

    def test_rejects_changed_archive(self):
        with self.assertRaisesRegex(ValueError, "archive integrity mismatch"):
            self.seal(b"forged tarball")

    def test_rejects_changed_lock(self):
        (self.web / "package-lock.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "lockfile changed"):
            self.seal()

    def test_rejects_other_registry(self):
        self.packages["node_modules/@playwright/test"]["resolved"] = "https://example.com/test.tgz"
        self.save_lock()
        with self.assertRaisesRegex(ValueError, "npm registry"):
            self.seal()


if __name__ == "__main__":
    unittest.main()
