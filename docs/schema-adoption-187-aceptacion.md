# Schema adoption Aceptacion

Isolated lab prefix. Never `~/.roca` and never a live hub.
Binary: the branch build from `make build`.

Home: `$LAB` with `HOME=$LAB`.
Init that home once with the branch binary before the commands below.

The four harvest tables may be missing `machine`, and `sessions` may still
carry leftover exact-payload clones. Those are the live 1.87 conditions.

## doctor

command: `HOME=$LAB ./roca doctor`
exit: 0
stdout does not contain: database schema requires adoption, read-only mode

## vector query

command: `HOME=$LAB ./roca vector query "schema adoption" 5 --databases corpus,ops`
exit: 0
stdout/stderr does not contain: database schema requires adoption, read-only mode

## bundled plugin place

command: `HOME=$LAB ./roca _install-bundled-plugins --json`
exit: 0
stdout contains: `"installed": true`
stdout/stderr does not contain: idx_sessions_exact_payload
