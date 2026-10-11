import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("promote", Path(__file__).with_name("promote-published.py"))
promotion = importlib.util.module_from_spec(spec)
spec.loader.exec_module(promotion)


class PromotionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.remote = self.directory / "remote.git"
        self.work = self.directory / "work"
        self.run_git("init", "--bare", str(self.remote), cwd=self.directory)
        self.work.mkdir()
        self.run_git("init", "-b", "main", cwd=self.work)
        self.run_git("config", "user.email", "test@example.invalid", cwd=self.work)
        self.run_git("config", "user.name", "Publication test", cwd=self.work)
        self.run_git("remote", "add", "origin", str(self.remote), cwd=self.work)
        self.commits = []
        for index in range(3):
            self.run_git("commit", "--allow-empty", "-m", str(index), cwd=self.work)
            self.commits.append(self.run_git("rev-parse", "HEAD", cwd=self.work))
        self.run_git("push", "origin", "main", cwd=self.work)
        previous = Path.cwd()
        os.chdir(self.work)
        self.addCleanup(os.chdir, previous)

    def run_git(self, *args, **kwargs):
        return subprocess.run(["git", *args], check=True, text=True,
                              capture_output=True, **kwargs).stdout.strip()

    def head(self):
        return self.run_git("--git-dir", str(self.remote), "rev-parse", "published")

    def test_bootstrap_forward_rerun_and_late_old_run(self):
        promotion.promote(self.commits[0])
        self.assertEqual(self.head(), self.commits[0])
        promotion.promote(self.commits[2])
        promotion.promote(self.commits[2])
        promotion.promote(self.commits[1])
        self.assertEqual(self.head(), self.commits[2])

    def test_racing_newer_promotion_wins(self):
        promotion.promote(self.commits[0])
        original = promotion.git

        def race(*args, **kwargs):
            if args[0] == "push":
                self.run_git("--git-dir", str(self.remote), "update-ref", "refs/heads/published", self.commits[2])
            return original(*args, **kwargs)

        with patch.object(promotion, "git", side_effect=race):
            promotion.promote(self.commits[1])
        self.assertEqual(self.head(), self.commits[2])

    def test_candidate_outside_main_cannot_promote(self):
        promotion.promote(self.commits[0])
        self.run_git("commit", "--allow-empty", "-m", "unpublished")
        candidate = self.run_git("rev-parse", "HEAD")
        with self.assertRaisesRegex(ValueError, "main's history"):
            promotion.promote(candidate)
        self.assertEqual(self.head(), self.commits[0])

    def test_push_failure_preserves_published(self):
        promotion.promote(self.commits[0])
        hook = self.remote / "hooks/pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        with self.assertRaises(RuntimeError):
            promotion.promote(self.commits[1])
        self.assertEqual(self.head(), self.commits[0])

    def test_divergent_published_is_not_overwritten(self):
        self.run_git("checkout", "--orphan", "divergent")
        self.run_git("commit", "--allow-empty", "-m", "different history")
        divergent = self.run_git("rev-parse", "HEAD")
        self.run_git("push", "origin", "HEAD:refs/heads/published")
        with self.assertRaisesRegex(ValueError, "diverged"):
            promotion.promote(self.commits[2])
        self.assertEqual(self.head(), divergent)
