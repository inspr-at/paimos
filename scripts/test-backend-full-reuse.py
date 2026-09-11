#!/usr/bin/env python3
"""Exercise evidence reuse against real commit ancestry and changed paths."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("reuse", Path(__file__).with_name("backend-full-reuse.py"))
reuse = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reuse)


class ReuseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.cwd = os.getcwd()
        os.chdir(self.tmp.name)
        self.addCleanup(lambda: os.chdir(self.cwd))
        self.addCleanup(self.tmp.cleanup)
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Test")
        self.base = self.commit("backend/main.go", "package main\n")
        self.head = self.commit("frontend/src/Offer.vue", "<h1>Offer</h1>\n")
        self.run = {"databaseId": 42, "headSha": self.base, "headBranch": "main",
                    "event": "push", "status": "completed", "conclusion": "success"}
        self.jobs = [{"name": name, "status": "completed", "conclusion": "success"}
                     for name in ("backend-full-authorize", "backend-full", "backend-full-serial",
                                  "backend-full-race (core)", "backend-full-race (handlers)",
                                  "backend-full-race (runtime)")]
        actual = reuse.command

        def command(*args):
            if args[0] == "gh":
                return json.dumps([self.run] if args[2] == "list" else {"jobs": self.jobs})
            return actual(*args)

        self.patcher = patch.object(reuse, "command", side_effect=command)
        self.patcher.start()
        self.addCleanup(self.patcher.stop)

    def git(self, *args):
        return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL).decode().strip()

    def commit(self, path, content):
        p = Path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content)
        self.git("add", "--", path)
        self.git("commit", "-qm", "fixture")
        return self.git("rev-parse", "HEAD")

    def choose(self, event="push", ref="refs/heads/main"):
        return reuse.select(self.head, event, ref, "example/product")

    def test_frontend_and_metadata_reuse_real_execution(self):
        self.head = self.commit("VERSION", "260911120000.0.0\n")
        self.assertEqual(self.choose(), {"run_full": "false", "source_sha": self.base,
                                        "source_run": "42",
                                        "source_url": "https://github.com/example/product/actions/runs/42"})

    def test_backend_and_unknown_paths_require_full(self):
        for path in ("backend/main.go", "Dockerfile", "frontend/package.json",
                     "scripts/backend-full-reuse.py", ".github/workflows/backend-full.yml",
                     "odd\nfrontend/src/file"):
            with self.subTest(path=path):
                self.head = self.commit(path, "changed\n")
                self.assertEqual(self.choose(), {"run_full": "true"})

    def test_removed_backend_file_is_not_hidden_by_rename(self):
        self.git("mv", "backend/main.go", "frontend/src/main.go")
        self.git("commit", "-qm", "move")
        self.head = self.git("rev-parse", "HEAD")
        self.assertEqual(self.choose(), {"run_full": "true"})

    def test_schedules_manual_pr_and_tags_always_full(self):
        for event, ref in (("schedule", "refs/heads/main"), ("workflow_dispatch", "refs/heads/main"),
                           ("pull_request", "refs/pull/1/merge"), ("push", "refs/tags/v1")):
            self.assertEqual(self.choose(event, ref), {"run_full": "true"})

    def test_every_required_execution_must_be_successful(self):
        for job in self.jobs:
            for conclusion in ("skipped", "failure", "cancelled"):
                job["conclusion"] = conclusion
                self.assertEqual(self.choose(), {"run_full": "true"})
            job["conclusion"] = "success"

    def test_failed_foreign_or_missing_source_evidence_is_not_reused(self):
        for key, value in (("conclusion", "failure"), ("headBranch", "feature"),
                           ("event", "pull_request"), ("headSha", "0" * 40)):
            before = self.run[key]
            self.run[key] = value
            self.assertEqual(self.choose(), {"run_full": "true"})
            self.run[key] = before
        self.git("checkout", "-qb", "other", self.base)
        self.run["headSha"] = self.commit("frontend/src/Other.vue", "other")
        self.git("checkout", "-q", self.head)
        self.assertEqual(self.choose(), {"run_full": "true"})


if __name__ == "__main__":
    unittest.main()
