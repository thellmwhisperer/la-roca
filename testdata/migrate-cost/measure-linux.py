"""Measure logical reads, including child accounting transferred by wait()."""
import json
import os
import subprocess
import sys
import time


def bytes_read():
    with open("/proc/self/io") as counters:
        return int(dict(line.split(":", 1) for line in counters)["rchar"])


before = bytes_read()
started = time.monotonic_ns()
with open(os.devnull, "wb") as output:
    code = subprocess.call(sys.argv[1:], stdout=output, stderr=output)
elapsed = time.monotonic_ns() - started
print(json.dumps({"bytes_read": bytes_read() - before, "elapsed_ns": elapsed}))
sys.exit(code)
