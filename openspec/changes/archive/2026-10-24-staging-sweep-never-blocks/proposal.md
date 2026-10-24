## Why

The staging sweep of `sweep-plugin-staging-leftovers` (#550, PR #564) runs at the
start of `prepare` in `internal/distribution/bundledplugin/bundledplugin.go`, and
`prepare` returns the sweep's error. `openServiceWith` in
`internal/distribution/cli/cli.go` calls `rocaops.Ensure` and `rocacorpus.Ensure`,
and through them `prepare`, on every open that is not read-only. So one leftover
that the sweep cannot remove makes every such command fail: `roca exec`,
`roca query`, `roca store`, and `roca doctor`, the command that should explain it.
A leftover is garbage by definition; it must not stop the product.

The same sweep removes, on the next ordinary command, every leftover it can.
So a separate `roca clean` command would almost never find anything to list.
What is still missing is a report of the leftovers the sweep could not remove.

Who asked: proposal, from issue #551 (`roca clean` and a doctor report);
decision 8151 (24-oct-2026, project la-roca) orders it rewritten as this change;
decision 8156 (24-oct-2026, project la-roca) reduces it to these two points and
takes `roca clean` out, to a separate ticket after the release if Javi wants it.

Proof: on the PR #564 build, 24-oct-2026, in a disposable HOME after `roca init`:
`roca exec "SELECT count(*) FROM plugin_roca_ops.memories"` exits 0. Then create
`~/.roca/plugins/.roca-corpus-bundle-stuck/sub/payload`, set `sub` to mode 0500
and the folder's modification time two hours back. The same `roca exec` and
`roca doctor` both exit 1 with `install bundled corpus plugin: unlinkat
…/.roca-corpus-bundle-stuck/sub/payload: permission denied`.

## What Changes

- The sweep never fails a command. A staging folder it cannot inspect or remove
  stays where it is, the sweep records the error for that folder and goes on to
  the next one, and `prepare` continues.
- `bundledplugin.StagingFolder` carries that error, so a caller can say why a
  folder is still there.
- `roca doctor` reports, for the bundled plugins, every staging folder more than
  one hour old, not held, and still present after its own sweep: path, size in
  bytes and the cause (the removal error, or that read-only mode does not remove).
  It shows the same list in `--json` as `plugin_staging`.
- A staging folder's age is the newest modification time across the folder and
  everything inside it, so content written within the last hour keeps an old
  folder fresh. After a failed removal the sweep restores the modification times
  it walked (best effort) and counts the surviving bytes again, so the next run
  still sees the folder as old and reports its real size.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `plugin-staging` (added by `sweep-plugin-staging-leftovers`): the sweep is
  best effort, and doctor reports what it left.

## Impact

- Code: `internal/distribution/bundledplugin/bundledplugin.go` (`SweepStaging`
  records per-folder errors instead of returning them; `prepare` ignores the
  sweep result), `internal/distribution/cli/models.go` (doctor report and its
  rendering), and the `install.feature` example that expected a non-zero exit
  for a `.held` that is a directory.
- Acceptance scenarios, in `features/distribution/install.feature` (tag
  `@plugin-staging`), run by `make accept` against the real `roca`: the
  scenarios of `specs/plugin-staging/spec.md` that this change adds.
- Not touched: `roca clean` and any new command (decision 8156); the one-hour
  threshold and the `.held` rule of the sweep; the read-only snapshot report of doctor; the
  ownership report of doctor; `roca doctor --report`; plugin databases, vector
  sidecars, their WAL/SHM/journal files, backups, logs and configuration.
