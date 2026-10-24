## 1. Lock that does not wait

- [ ] 1.1 Add a non-blocking exclusive lock to `internal/securefile` (unix: `LOCK_EX|LOCK_NB`; windows: `LOCKFILE_FAIL_IMMEDIATELY`) that returns a distinct "held" result instead of waiting.

## 2. Staging folders hold their lock

- [ ] 2.1 In `materialize` and `preflightInstalledSchema`, take the lock on `<folder>/.held` right after `os.MkdirTemp`; in the existing cleanup, remove the folder first and release the lock after.

## 3. Sweep

- [ ] 3.1 Add one exported function in `bundledplugin` that lists the `.<plugin>-schema-preflight-*` and `.<plugin>-bundle-*` folders of a plugin root with size and age (#551 prints both), and removes those older than one hour whose `.held` lock it takes without waiting (a folder with no `.held` file counts as not held).
- [ ] 3.2 Call it at the start of `prepare`, for that spec's plugin name, before `materialize`.

## 4. Acceptance

- [ ] 4.1 In `features/distribution/install.feature` and its steps, write the five scenarios of `specs/plugin-staging/spec.md`, run through `roca _install-bundled-plugins --json` on the real binary; the test sets folder ages with `os.Chtimes` and holds the lock from the test process.
- [ ] 4.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
