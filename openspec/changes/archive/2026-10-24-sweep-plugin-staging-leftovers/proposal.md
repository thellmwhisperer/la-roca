## Why

An interrupted plugin update leaves its staging folders in `~/.roca/plugins/`
and nothing removes them later. `internal/distribution/bundledplugin/bundledplugin.go`
creates two temporary folders and removes them only through `defer os.RemoveAll`
or `cleanup()`:

- `preflightInstalledSchema` (line 316): `.<plugin>-schema-preflight-*`, holding a
  `VACUUM INTO` copy of the whole installed database before `ApplySchema` runs on it.
- `materialize` (line 510): `.<plugin>-bundle-*`, a staged package.

A killed process (SIGINT, SIGKILL, an operator aborting a lock wait) runs neither
cleanup. `Ensure`/`EnsureAll` run on every `roca _install-bundled-plugins`, which
`install.sh`, `roca update` and the acceptance suite call, and on `roca vector install`.
Nothing serializes two of them, so a sweep must leave alone a folder that another
live `roca` process is still using.

Who asked: issue #550, carrying the piece that #551 (`roca clean`) builds on. A
preflight free-space check stays out of this change: there is no proof of a home
running out of disk, and its
failure branch cannot be reached by an end-to-end scenario without a product test hook.

Proof: one home after the 1.87.x upgrade (same day as #432 and #461) held one
`.roca-corpus-schema-preflight-*` folder with a full-size `roca-corpus.db` and a
large `roca-corpus.db-journal` (the process died inside `ApplySchema`), and over a
hundred `.roca-corpus-bundle-*` folders created over a few hours, one per
interrupted retry (issue #550). Reproduce: start `roca _install-bundled-plugins` on a
home whose corpus needs a schema advance, kill it during the preflight, then
`ls -a ~/.roca/plugins/`.

## What Changes

- Every staging folder the bundled installer creates holds an exclusive advisory
  lock on a `.held` file inside it for as long as its creator uses it; the creator
  removes the folder before it releases the lock.
- At the start of preparing each bundled plugin, the installer removes every
  `.<plugin>-schema-preflight-*` and `.<plugin>-bundle-*` folder in the plugin root
  that is more than one hour old and whose `.held` lock it can take without waiting.
  A folder whose lock is held by another process stays. The age rule closes the
  moment between creating a folder and taking its lock, when another process could
  otherwise see it unlocked.
- `internal/securefile` gains a non-blocking variant of `Lock` that reports
  "held by someone else" instead of waiting.
- The sweep is one exported function of `bundledplugin` that returns what it found,
  so `roca clean` (#551) lists and removes with the same rule.

## Capabilities

### New Capabilities

- `plugin-staging`: lifecycle of the bundled installer's temporary folders and the
  rule that tells a leftover from a folder in use.

### Modified Capabilities

None.

## Impact

- Code: `internal/distribution/bundledplugin/bundledplugin.go` (`prepare`,
  `materialize`, `preflightInstalledSchema`, the new sweep function),
  `internal/securefile/lock_unix.go` and `lock_windows.go` (non-blocking lock).
- Acceptance scenarios to write, in `features/distribution/install.feature`, run by
  `make accept` through `roca _install-bundled-plugins --json`: old leftover preflight
  and bundle folders are gone after the install; a folder whose `.held` lock the test
  process holds survives it; a folder created a moment ago survives it.
- Not touched: the full-size `VACUUM INTO` copy itself and any free-space check;
  `roca doctor` and any new command (#551); plugin databases, vector sidecars, their
  WAL/SHM/journal files, backups, logs and configuration; the schema advance rules.
