"""Read final process rchar before reaping; polling can miss a short exec."""
import json
import os
import subprocess
import sys
import time

started = time.monotonic_ns()
with open(os.devnull, "wb") as output:
    child = subprocess.Popen(sys.argv[1:], stdout=output, stderr=output)
    os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOWAIT)
    elapsed = time.monotonic_ns() - started
    with open(f"/proc/{child.pid}/io") as counters:
        reads = dict(line.split(":", 1) for line in counters)
    code = child.wait()
print(json.dumps({"bytes_read": int(reads["rchar"]), "elapsed_ns": elapsed}))
sys.exit(code)
