"""Measure reaped child rchar through the parent's Linux I/O accounting."""
import json
import os
import subprocess
import sys
import time


def bytes_read():
    with open("/proc/self/io") as counters:
        return int(dict(line.split(":", 1) for line in counters)["rchar"])


# wait() adds the child's complete I/O counters to this parent's counters.
# Reading a zombie's /proc/PID/io can fail its ptrace permission check. The
# parent delta includes the small counter read too, making it an upper bound.
before = bytes_read()
started = time.monotonic_ns()
with open(os.devnull, "wb") as output:
    code = subprocess.call(sys.argv[1:], stdout=output)
elapsed = time.monotonic_ns() - started
reads = bytes_read() - before
print(json.dumps({"bytes_read": reads, "elapsed_ns": elapsed}))
sys.exit(code)
