#!/usr/bin/env python3
"""Safety regressions for the opt-in proof runner; never starts a service/model."""
import argparse
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("proof", Path(__file__).with_name("platform-acceptance.py"))
proof = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(proof)


class ProofGuards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="pai928-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.binary = self.root / "binary"
        self.binary.write_bytes(b"fixture-not-executable")
        self.unit = self.root / "unit.service"
        self.unit.write_bytes(b"fixture-not-a-unit")
        self.plan = {
            "root": str(self.root), "instance": "pai928-1234567890",
            "vm_state_root": "/tmp/pai928-1234567890", "unit": str(self.unit),
            "unit_sha256": proof.sha(self.unit), "files": {str(self.binary): proof.sha(self.binary)},
            "helper_sources": {}, "codex": str(self.binary), "codex_sha256": proof.sha(self.binary),
            "runner_sha256": proof.sha(proof.__file__),
        }
        self.path = self.root / "plan.json"
        self.save()

    def save(self):
        self.path.write_text(json.dumps(self.plan))

    def test_changed_binary_fails_before_any_subprocess(self):
        self.binary.write_bytes(b"replacement")
        with patch.object(proof.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "pin changed"):
                proof.load_plan(self.path)
            run.assert_not_called()

    def test_foreign_state_root_fails_before_any_subprocess(self):
        self.plan["vm_state_root"] = "/tmp/existing-other-runtime"
        self.save()
        with patch.object(proof.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "scope mismatch"):
                proof.load_plan(self.path)
            run.assert_not_called()

    def test_service_and_vendor_require_explicit_gate(self):
        for mode in ("service", "codex"):
            with self.subTest(mode=mode), patch.object(proof.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "explicit reviewed-plan"):
                    proof.execute(argparse.Namespace(plan=self.path, mode=mode, execute_reviewed_plan=False))
                run.assert_not_called()

    def test_prior_receipt_cannot_be_overwritten(self):
        target = self.root / "receipt.json"
        proof.write(target, {"status": "original"})
        with self.assertRaises(FileExistsError):
            proof.write(target, {"status": "replacement"})
        self.assertEqual(json.loads(target.read_text()), {"status": "original"})


if __name__ == "__main__":
    unittest.main()
