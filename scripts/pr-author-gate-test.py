#!/usr/bin/env python3
"""Executable synthetic acceptance for the PR author gate."""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts/pr-author-gate.py"
ALLOW = ROOT / ".github/pr-author-allow.txt"
TESEO = "sol@javiermellado.com"
CURSOR = "cursoragent@cursor.com"
LAB = "agent@host.mellado.lab"
NOREPLY = "1+synthetic@users.noreply.github.com"


class PrAuthorGateTest(unittest.TestCase):
    def setUp(self):
        (ROOT / ".tmp").mkdir(exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(dir=ROOT / ".tmp")
        self.addCleanup(self.tmp.cleanup)
        self.cwd = Path(self.tmp.name)

    def git(self, *args, email=TESEO, cwd=None):
        env = os.environ.copy()
        env["GIT_AUTHOR_NAME"] = "Javier Mellado"
        env["GIT_AUTHOR_EMAIL"] = email
        env["GIT_COMMITTER_NAME"] = "Javier Mellado"
        env["GIT_COMMITTER_EMAIL"] = email
        return subprocess.check_output(
            ["git", "-c", "core.hooksPath=/dev/null", *args],
            cwd=cwd or self.cwd, text=True, env=env,
        ).strip()

    def check(self, base, head="HEAD", cwd=None):
        return subprocess.run(
            [sys.executable, str(CHECK), "--repo", str(cwd or self.cwd),
             "--allow", str(ALLOW), "--base", base, "--head", head],
            text=True, capture_output=True,
        )

    def repo_with_base(self):
        cwd = Path(tempfile.mkdtemp(dir=ROOT / ".tmp"))
        self.addCleanup(lambda: shutil.rmtree(cwd, ignore_errors=True))
        self.git("init", "-q", cwd=cwd)
        (cwd / "base.txt").write_text("base\n")
        self.git("add", ".", cwd=cwd)
        self.git("commit", "-qm", "Synthetic base", cwd=cwd)
        return cwd, self.git("rev-parse", "HEAD", cwd=cwd)

    def commit_file(self, cwd, name, body, email=TESEO):
        (cwd / name).write_text(name + "\n")
        self.git("add", name, cwd=cwd)
        self.git("commit", "-qm", body, email=email, cwd=cwd)
        return self.git("rev-parse", "HEAD", cwd=cwd)

    def test_synthetic_pair_and_frozen_base(self):
        started = time.monotonic()
        cwd, dirty_base = self.repo_with_base()
        self.git("commit", "--allow-empty", "-qm",
                 "Synthetic dirty base\n\nCo-authored-by: Cursor <" + CURSOR + ">",
                 cwd=cwd)
        dirty_base = self.git("rev-parse", "HEAD", cwd=cwd)
        clean = self.commit_file(cwd, "clean.txt", "Synthetic teseo change")
        passed = self.check(dirty_base, clean, cwd=cwd)
        self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
        self.assertIn("pr-author: clean", passed.stdout)

        self.commit_file(
            cwd, "trailer.txt",
            "Synthetic trailer\n\nCo-authored-by: Cursor <" + CURSOR + ">",
        )
        trailer = self.check(dirty_base, cwd=cwd)
        self.assertEqual(trailer.returncode, 1, trailer.stdout + trailer.stderr)
        self.assertIn("Co-authored-by trailer", trailer.stdout)

        for email in (CURSOR, LAB, NOREPLY):
            with self.subTest(email=email):
                foreign, base = self.repo_with_base()
                self.commit_file(foreign, "bad.txt", "Synthetic foreign author", email=email)
                result = self.check(base, cwd=foreign)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("not on allowlist", result.stdout)
                self.assertIn(email, result.stdout)

        empty = self.check(clean, clean, cwd=cwd)
        self.assertEqual(empty.returncode, 0, empty.stdout + empty.stderr)
        self.assertIn("pr-author: clean", empty.stdout)
        self.assertLess(time.monotonic() - started, 5)
        print("acceptance: synthetic Co-authored-by fails; clean Teseo-only range passes")


if __name__ == "__main__":
    unittest.main()
