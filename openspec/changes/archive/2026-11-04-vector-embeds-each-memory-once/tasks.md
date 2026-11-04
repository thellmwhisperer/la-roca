## 1. Ownership

- [x] 1.1 Make the ops sidecar embed, in cutover, the memories whose migration source is `core` (through `memory_compatibility` or a view over it), and leave corpus-sourced memories to the corpus sidecar.
- [x] 1.2 Report a sidecar with nothing to embed as finished after `roca vector install`, and drop the `roca vector install` remedy from `roca doctor` for it.

## 2. End to end

- [x] 2.1 Write the two scenarios of `specs/vector-memory-ownership/spec.md` in `features/distribution/e2e-federation.feature`, run by `make e2e` against the installed candidate with the pinned embedding model, installing with `install.sh` from the suite's local release channel; record that both fail on v1.94.1.
- [x] 2.2 `make e2e-cover` passes: every changed line is executed by an end-to-end run.
  - Recorded on the final head bec0cf3 with `make e2e-cover COVER_BASE=d004d48` (the integration commit the change was cut from): `ok github.com/thellmwhisperer/la-roca/test/acceptance 205.474s` and `cover-diff: 41 changed lines inside covered blocks, all executed by an end-to-end run`, exit 0.
