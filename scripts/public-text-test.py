#!/usr/bin/env python3
"""Executable synthetic acceptance and bounded cost check for public-text."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts/public-text.py"
# Generated synthetic negatives keep private-looking literals out of public code.
HOME_PATH = "/" + "Users" + "/someone/x"
TEMP_PATH = "/" + "var" + "/folders/" + "synthetic/x"
HOST = "synthetic" + ".local"
UUID = "-".join(("00000000", "0000", "4000", "8000", "000000000000"))


class PublicTextTest(unittest.TestCase):
    def setUp(self):
        (ROOT / ".tmp").mkdir(exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(dir=ROOT / ".tmp")
        self.addCleanup(self.tmp.cleanup)
        self.cwd = Path(self.tmp.name)

    def check(self, *args, body=""):
        return subprocess.run([sys.executable, str(CHECK), *args], input=body,
                              text=True, capture_output=True, cwd=self.cwd)

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.cwd, text=True).strip()

    def event(self, kind, title="Synthetic report", body=""):
        event = self.cwd / "event.json"
        event.write_text(json.dumps({kind: {"title": title, "body": body}}))
        return str(event)

    def test_cost_body_pair(self):
        started = time.monotonic()
        bad = self.check("--text", body="Reproduction\n" + HOME_PATH)
        self.assertEqual(bad.returncode, 1, bad.stderr)
        self.assertIn(':2: user-home: ' + json.dumps(HOME_PATH), bad.stdout)
        good = self.check("--text", body="Reproduction\n~/x")
        self.assertEqual(good.returncode, 0, good.stdout + good.stderr)
        self.assertIn("public-text: clean", good.stdout)
        # Two tiny bodies must not acquire a network, federation or model cost.
        self.assertLess(time.monotonic() - started, 5)
        print("paired body evidence: synthetic home rejected with quoted line; ~/x passes; under 5 seconds")

    def test_patterns_and_exact_allowlist(self):
        negatives = [HOME_PATH, "/" + "home/someone/x", TEMP_PATH,
                     "/private" + TEMP_PATH, "/" + "Volumes/synthetic/x", HOST, UUID]
        negatives += [".".join(map(str, parts)) for parts in
                      [(10, 2, 3, 4), (172, 16, 2, 3), (172, 31, 2, 3), (192, 168, 2, 3)]]
        for value in negatives:
            with self.subTest(value=value):
                self.assertEqual(self.check("--text", body=value).returncode, 1)
        for value in ("~ $TMPDIR <workspace> <host> <lan-ip>", "m.models.local",
                      "models.localURL", "models.local_field", "172.32.2.3",
                      "172.15.2.3", "999.999.999.999"):
            with self.subTest(value=value):
                self.assertEqual(self.check("--text", body=value).returncode, 0)
        allow = self.cwd / "allow.txt"
        allow.write_text(HOST + "\n" + HOME_PATH + "\n")
        self.assertEqual(self.check("--text", "--allow", str(allow), body=HOST).returncode, 0)
        self.assertEqual(self.check("--text", "--allow", str(allow), body=HOME_PATH).returncode, 0)
        self.assertEqual(self.check("--text", "--allow", str(allow), body="other-" + HOST).returncode, 1)
        self.assertEqual(self.check("--text", "--allow", str(allow), body=HOME_PATH + "-other").returncode, 1)

    def test_lan_ip_sentence_punctuation_and_malformed_addresses(self):
        for parts in ((10, 2, 3, 4), (172, 16, 2, 3), (192, 168, 1, 2)):
            address = ".".join(map(str, parts))
            for suffix in (".", ". Next sentence", ",", ")", ";"):
                with self.subTest(address=address, suffix=suffix):
                    body = "Server was " + address + suffix
                    result = self.check("--text", body=body)
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn(':1: lan-ip: ' + json.dumps(body), result.stdout)
            body = "Server was " + address + "."
            result = self.check("--issue-comment", self.event("issue", body=body))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("issue body, line 1: `lan-ip`", result.stdout)
            self.assertNotIn(address, result.stdout)
            for value in (address + ".5", "1." + address, address + "999",
                          "x" + address, address + "x", address + ".example"):
                with self.subTest(value=value):
                    result = self.check("--text", body="Server was " + value + ".")
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result = self.check("--text", body="Server was " + ".".join(("192", "168", "1", "999")) + ".")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_issue_flag_does_not_repeat_value(self):
        for field in ("title", "body"):
            result = self.check("--issue-comment", self.event("issue", **{field: TEMP_PATH}))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("private-temp", result.stdout)
            self.assertIn("issue " + field + ", line 1", result.stdout)
            self.assertNotIn(TEMP_PATH, result.stdout)
        self.assertEqual(self.check("--issue-comment", self.event("issue", body="~/x")).stdout, "")

    def test_pr_metadata_commits_and_diff(self):
        self.git("init", "-q")
        self.git("config", "user.name", "Synthetic")
        self.git("config", "user.email", "synthetic@example.invalid")
        (self.cwd / "existing.txt").write_text(HOME_PATH + "\n")
        self.git("add", ".")
        self.git("commit", "-qm", "Synthetic base")
        base = self.git("rev-parse", "HEAD")
        (self.cwd / "existing.txt").write_text("~/x\n")
        fixtures = self.cwd / "testdata"
        fixtures.mkdir()
        (fixtures / "synthetic.txt").write_text(UUID + "\n")
        self.git("add", ".")
        self.git("commit", "-qm", "Synthetic safe change")
        args = ["--base", base, "--head", "HEAD"]
        clean = self.check("--event", self.event("pull_request"), *args)
        self.assertEqual(clean.returncode, 0, clean.stdout + clean.stderr)
        for field in ("title", "body"):
            bad = self.check("--event", self.event("pull_request", **{field: HOME_PATH}), *args)
            self.assertEqual(bad.returncode, 1)
            self.assertIn("PR " + field, bad.stdout)
            self.assertIn(json.dumps(HOME_PATH), bad.stdout)
        (self.cwd / "new.txt").write_text(UUID + "\n+++" + HOME_PATH + "\n")
        (fixtures / "synthetic.txt").write_text(UUID + "\n" + HOME_PATH + "\n")
        self.git("add", "new.txt", "testdata")
        self.git("commit", "-qm", HOST)
        bad = self.check("--event", self.event("pull_request"), *args)
        self.assertEqual(bad.returncode, 1)
        for expected in ('"new.txt":1: uuid-v4', '"new.txt":2: user-home',
                         '"testdata/synthetic.txt":2: user-home', '"commits":1: local-host'):
            self.assertIn(expected, bad.stdout)
        self.assertNotIn('"testdata/synthetic.txt":1: uuid-v4', bad.stdout)


if __name__ == "__main__":
    unittest.main()
