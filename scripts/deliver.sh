#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
# No --wait: axi run returns at the first gate or after its default hold, and ATM's screen paints the gate from then on.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT" --closes "$ATM_ISSUE"
