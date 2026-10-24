## 1. State lock

- [ ] 1.1 In `LockStateUsage` (`plugins/vector/internal/vector/runtime.go`), after taking the shared relocation lock on a managed state folder that does not exist and that no relocation moved, create it with mode 0700 and return the lock; keep the "moved; rerun" error only for a folder a relocation moved.
- [ ] 1.2 In `Launch`, keep the state lock before `MkdirAll` but rely on 1.1, so `roca vector install` and the background setup a query starts both reach the worker.

## 2. Acceptance

- [ ] 2.1 Write the three scenarios of `specs/vector-first-use/spec.md` as acceptance scenarios run by `make accept` against the real `roca`, on a home made by `roca init` with vector search off and then `vector = true`; point the model download at a local fixture so no test fetches the real model.
- [ ] 2.2 `make e2e-cover` passes: every changed line is executed by those scenarios.
