#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
# The run stays in the foreground without --yes, so ATM's screen paints its steps; whoever launched the run answers each gate.
# The test step alone runs past axi's default 8 minute hold here, so the hold is the length of a whole gate.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT" --closes "$ATM_ISSUE" --wait 2h
