# Tombstones

Empty, and that is the good state.

A tombstone is a permanent, reviewable record of duplication this repository
decided to keep. It **consumes** a finding: an accepted clone stops counting
against the ceiling, so the ceiling keeps measuring live debt only. It never
produces one.

It is the last resort, not the cheap way past a red gate. The order is: remove
the duplication; if it cannot be removed, lower the ceiling by paying something
else down; and only then write the clone down here, with what the smell is, what
actually happened, why it got through and what the rule is now.

Two rules keep the mechanism honest, and `slopslint check --classify --enforce`
holds both:

- a record whose fingerprint matches nothing is **stale** and fails the gate.
  Debt that was paid off does not get to leave its exemption behind;
- accepting a clone does not raise anything. The ceiling comes down by one all
  the same, because the active count went down by one.

One YAML file per record, named after its id. The shape and the fingerprint
field are documented in the slopslint README; `slopslint tombstone add` writes
the skeleton and `slopslint tombstone check` validates it.
