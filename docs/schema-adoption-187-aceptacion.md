# Schema adoption Aceptacion

Isolated lab prefix. Never `~/.roca` and never a live hub.
Binary: the branch build from `make build`.

Home: `$LAB` with `HOME=$LAB`.
Initialize that home once with the branch binary, then materialize the affected
pre-1.87 state before any read-only check:

```sh
LAB="$(mktemp -d)"
export LAB
HOME="$LAB" ./roca init

CORE="$LAB/.roca/roca.db"
CORPUS="$LAB/.roca/plugins/roca-corpus/roca-corpus.db"
sqlite3 "$CORE" <<'SQL'
ALTER TABLE sessions DROP COLUMN machine;
ALTER TABLE exchanges DROP COLUMN machine;
ALTER TABLE thinking_blocks DROP COLUMN machine;
ALTER TABLE tool_uses DROP COLUMN machine;
SQL
sqlite3 "$CORPUS" <<'SQL'
DROP INDEX IF EXISTS idx_sessions_exact_payload;
CREATE INDEX idx_sessions_exact_payload
  ON sessions(source_agent, title, metadata);
INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
VALUES ('duplicate-a', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}'),
       ('duplicate-b', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}');
SQL
```

Keep `ROCA_READ_ONLY=1` on both read-only commands below. Running a writable
`doctor` first would adopt the missing columns and hide the regression.

The four harvest tables may be missing `machine`, and `sessions` may still
carry leftover exact-payload clones. Those are the live 1.87 conditions.

## doctor

command: `HOME=$LAB ROCA_READ_ONLY=1 ./roca doctor`
exit: 0
stdout does not contain: database schema requires adoption, read-only mode

## vector query

command: `HOME=$LAB ROCA_READ_ONLY=1 ./roca vector query "schema adoption" 5 --databases corpus,ops`
exit: 0
stdout/stderr does not contain: database schema requires adoption, read-only mode

## bundled plugin place

command: `HOME=$LAB ./roca _install-bundled-plugins --json`
exit: 0
stdout contains: `"installed": true`
stdout/stderr does not contain: idx_sessions_exact_payload

The placement command must succeed with the duplicate corpus sessions in
place. It retains the same-named prior index; exact-payload uniqueness remains
pending until the exact-dedup maintenance flow removes the clones.

## unique guard plus empty-surface clone

The live hub residual is not the prior non-unique index. The unique hash
guard is already installed, schema version is still below the current
declaration, and two session rows share every payload column except
`source_surface` (one empty, one already labeled). Filling the empty one
would collide. Repeat the isolated lab prefix, then replace the corpus
SQL with:

```sh
sqlite3 "$CORPUS" <<'SQL'
UPDATE plugin_schema SET schema_version = schema_version - 1;
INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
VALUES ('labeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', 'Claude Code'),
       ('unlabeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', ''),
       ('fillable', 'claude', 'other', '2026-08-16T10:00:00Z', '{}', '');
SQL
```

Re-run the three commands above. Place must exit 0. `unlabeled` stays
empty. `fillable` becomes `Claude Code`. stdout/stderr still does not
contain `idx_sessions_exact_payload`.
