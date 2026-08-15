CREATE TABLE IF NOT EXISTS journeys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  train        TEXT NOT NULL,
  ride         TEXT NOT NULL,
  plugin       TEXT NOT NULL,
  started_at   TEXT NOT NULL,
  ended_at     TEXT NOT NULL,
  duration_ms  INTEGER NOT NULL,
  exit_code    INTEGER,
  error        TEXT NOT NULL DEFAULT '',
  gate_status  TEXT NOT NULL,
  stdout       TEXT NOT NULL DEFAULT '',
  stderr       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS journeys_dependency_latest
  ON journeys (ride, plugin, id DESC);

CREATE TABLE IF NOT EXISTS legacy_runs (
  canonical_digest TEXT PRIMARY KEY,
  payload           TEXT NOT NULL CHECK (json_valid(payload))
);

CREATE TABLE IF NOT EXISTS legacy_run_logs (
  canonical_digest TEXT PRIMARY KEY,
  payload           TEXT NOT NULL CHECK (json_valid(payload))
);
