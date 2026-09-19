# Ops schema-ahead Aceptacion

Isolated lab prefix. Never `~/.roca` and never a live hub.
Binary: the branch build from `make build`.

This is not the UNIQUE `idx_sessions_exact_payload` class and not #427
short-ids. The hub failure is ops schema/index `6/2` already adopted by a
newer binary while this release still declares `5/2`.

Home: `$LAB` with `HOME=$LAB`.
Initialize that home once with the branch binary, then advance only the
ops ledger to the live ahead state before the checks:

```sh
LAB="$(mktemp -d)"
export LAB
HOME="$LAB" ./roca init

OPS="$LAB/.roca/plugins/roca-ops/roca-ops.db"
sqlite3 "$OPS" <<'SQL'
ALTER TABLE memories ADD COLUMN legacy_id INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_legacy_id
  ON memories(legacy_id) WHERE legacy_id IS NOT NULL;
UPDATE plugin_schema SET schema_version = 6, index_version = 2;
SQL
```

## bundled plugin place

command: `HOME=$LAB ./roca _install-bundled-plugins --json`
exit: 0
stdout contains: `"installed": true`
stdout/stderr does not contain: newer than supported, plugin skipped

## doctor

command: `HOME=$LAB ./roca doctor`
exit: 0
stdout/stderr does not contain: newer than supported, plugin skipped, schema-adoption

## vector query

command: `HOME=$LAB ./roca vector query "ops schema ahead" 5 --databases corpus,ops`
exit: 0
stdout/stderr does not contain: newer than supported, plugin skipped, schema-adoption
