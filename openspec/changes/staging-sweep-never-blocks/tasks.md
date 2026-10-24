## 1. Best-effort sweep

- [ ] 1.1 In `SweepStaging`, record a folder's inspection or removal error in a new `StagingFolder` field and continue with the next folder; return an error only when the plugin root itself cannot be read.
- [ ] 1.2 In `prepare`, ignore the sweep result: a leftover never stops the bundled install or the command that opened the service.
- [ ] 1.3 In `features/distribution/install.feature`, the "lock file" example of the filesystem-failures outline now expects exit 0; move it to the new scenario of `specs/plugin-staging/spec.md`.

## 2. Doctor report

- [ ] 2.1 In `roca doctor`, run `SweepStaging` for each bundled plugin (remove unless read-only) and collect the folders more than one hour old, not held and not removed, with path, size and cause.
- [ ] 2.2 Add them to the doctor JSON as `plugin_staging`, and print them in text: one line per folder with path, bytes and cause, or one line saying none is left.

## 3. Acceptance

- [ ] 3.1 Write the seven scenarios of `specs/plugin-staging/spec.md` as acceptance scenarios run by `make accept` against the real `roca`. The unremovable folder is a mode 0500 `sub` folder holding a file; the test restores the mode before cleanup.
- [ ] 3.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
