#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
# While this command runs, ATM's screen hands it the terminal; once it ends, the screen paints the gate live.
# So axi run holds only briefly. Its hold ending with the run still going exits 1 but is not a failure:
# this repository's test step alone outlasts any short hold, so that case is delivered too.
no-mistakes init || exit 1
no-mistakes axi run --intent "$ATM_INTENT" --closes "$ATM_ISSUE" --wait 30s && exit 0
no-mistakes axi status | grep -q '^  status: running'
