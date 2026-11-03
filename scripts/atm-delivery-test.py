#!/usr/bin/env python3
"""Tests for scripts/atm-delivery.sh against a fake no-mistakes on PATH.

The fake answers `init`, a bare `axi status` (the branch's state before launch), `axi run` (printing a launch
receipt for the nonce and generation it was given, unless FAKE_NO_RECEIPT is set) and `axi status --run <id>`
(the run's final state). `sleep` is faked so a pending run is not waited on."""
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).resolve().parent / "atm-delivery.sh"

FAKE_NM = """#!/bin/sh
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
"init ") exit 0 ;;
"axi status")
  if [ "$3" = --run ]; then
    printf 'run:\\n  id: "%s"\\n  status: %s\\noutcome: %s\\n' "$4" "$FAKE_STATUS" "$FAKE_OUTCOME"
  else
    printf 'runs_on_current_branch: 0\\n'
  fi ;;
"axi run")
  [ -n "${FAKE_NO_RECEIPT:-}" ] && exit 0
  printf 'launch_receipt:\\n  run_id: "r1"\\n  launch_nonce: "%s"\\n  validation_generation: "%s"\\n' "$4" "$6" ;;
*) exit 2 ;;
esac
"""


class DeliveryTest(unittest.TestCase):
    def run_delivery(self, status="completed", outcome="passed", no_receipt=False):
        tmp = pathlib.Path(tempfile.mkdtemp())
        bin_dir = tmp / "bin"
        bin_dir.mkdir()
        (bin_dir / "no-mistakes").write_text(FAKE_NM)
        (bin_dir / "sleep").write_text("#!/bin/sh\nexit 0\n")
        for tool in ("no-mistakes", "sleep"):
            (bin_dir / tool).chmod(0o755)
        report_dir = tmp / "runs" / "551-8"
        report_dir.mkdir(parents=True)
        env = dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}", FAKE_LOG=str(tmp / "log"),
                   FAKE_STATUS=status, FAKE_OUTCOME=outcome, ATM_REPORT=str(report_dir / "report.json"),
                   ATM_INTENT="the unit", ATM_ISSUE="551")
        if no_receipt:
            env["FAKE_NO_RECEIPT"] = "1"
        done = subprocess.run(["sh", str(SCRIPT)], env=env, capture_output=True, text=True, timeout=30)
        log = (tmp / "log").read_text() if (tmp / "log").exists() else ""
        return done, log, report_dir

    def test_passing_run_exits_zero_and_targets_integration(self):
        done, log, report_dir = self.run_delivery()
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)
        self.assertIn("--closes 551 --base-branch integration", log)
        self.assertIn("axi status --run r1", log)
        self.assertEqual((report_dir / "delivery.run").read_text().strip(), "r1")

    def test_failed_run_exits_one(self):
        done, _, _ = self.run_delivery(status="failed", outcome="failed")
        self.assertEqual(done.returncode, 1)

    def test_completed_without_passing_outcome_exits_one(self):
        done, _, _ = self.run_delivery(outcome="failed")
        self.assertEqual(done.returncode, 1)

    def test_launch_without_receipt_exits_one(self):
        done, _, _ = self.run_delivery(no_receipt=True)
        self.assertEqual(done.returncode, 1)
        self.assertIn("no launch receipt of this attempt binds a run", done.stdout)


if __name__ == "__main__":
    sys.exit(unittest.main())
