#!/usr/bin/env bash
# Exercise the real current CLI against homes frozen by historical releases.
set -euo pipefail

cd "$(dirname "$0")/.."

versions_file="internal/distribution/release/testdata/upgrade/versions.txt"

versions=()
while IFS= read -r listed || [ -n "$listed" ]; do
  [ -n "$listed" ] || continue
  versions+=("$listed")
done < "$versions_file"
if [ "${#versions[@]}" -eq 0 ]; then
  echo "$versions_file lists no frozen home to upgrade" >&2
  exit 2
fi

# The list is the single source of the CI matrix too: a committed archive
# becomes one more job without either workflow naming a version.
if [ "${1:-}" = "--versions" ]; then
  separator=""
  printf '['
  for version in "${versions[@]}"; do
    printf '%s"%s"' "$separator" "$version"
    separator=","
  done
  printf ']\n'
  exit 0
fi

binary="${1:-bin/roca}"
requested="${2:-}"
if [[ "$binary" != /* ]]; then
  binary="$(pwd)/$binary"
fi
if [ ! -x "$binary" ]; then
  echo "current binary is not executable: $binary" >&2
  exit 2
fi
if [ -n "$requested" ] && ! grep -Fxq "$requested" "$versions_file"; then
  echo "$requested is not listed in $versions_file" >&2
  exit 2
fi

workdirs=()
cleanup() {
  status=$?
  if [ "$status" -eq 0 ]; then
    rm -rf "${workdirs[@]}"
  elif [ "${#workdirs[@]}" -gt 0 ]; then
    echo "upgrade gauntlet retained diagnostics in ${workdirs[*]}" >&2
  fi
}
trap cleanup EXIT

assert_json() {
  file="$1"
  pattern="$2"
  label="$3"
  if ! grep -qE "$pattern" "$file"; then
    echo "$label: expected /$pattern/ in $file" >&2
    sed -n '1,240p' "$file" >&2
    return 1
  fi
}

run_roca() {
  home="$1"
  shift
  env -i HOME="$home" PATH=/usr/bin:/bin ROCA_MODELS_ORDER=none \
    "$binary" --db-path "$home/.roca/roca.db" --json "$@"
}

run_fixture() {
  version="$1"
  fixture="internal/distribution/release/testdata/upgrade/homes/$version.tar.gz"
  mkdir -p .tmp
  work="$(mktemp -d ".tmp/upgrade-gauntlet.${version}.XXXXXX")"
  workdirs+=("$work")
  home="$work/home"
  mkdir -p "$home"
  tar -xzf "$fixture" -C "$home"

  session_dir="$home/.claude/projects/-synthetic-current-upgrade"
  mkdir -p "$session_dir"
  cat > "$session_dir/99999999-8888-7777-6666-555555555555.jsonl" <<'JSONL'
{"type":"user","sessionId":"99999999-8888-7777-6666-555555555555","timestamp":"2026-08-02T10:00:00Z","cwd":"/synthetic/current-upgrade-probe","message":{"content":"record the current jade sextant"}}
{"type":"assistant","sessionId":"99999999-8888-7777-6666-555555555555","timestamp":"2026-08-02T10:00:01Z","message":{"model":"fixture-current-model","usage":{"input_tokens":11,"output_tokens":7},"content":[{"type":"text","text":"the current jade sextant is recorded"}]}}
JSONL

  # Which database the frozen release wrote its own history to is read from the
  # extracted home, before the upgrade installs anything: a home frozen before
  # the bundled corpus kept that history in core, one frozen after it kept the
  # same history in the corpus, and an upgrade owes both the answer and the
  # place. Counting the two separately fails a silent relocation that a single
  # total would accept.
  if [ -e "$home/.roca/plugins/roca-corpus/roca-corpus.db" ]; then
    frozen_in_core=0
    frozen_in_corpus=1
  else
    frozen_in_core=1
    frozen_in_corpus=0
  fi

  run_roca "$home" ingest > "$work/ingest.json"
  assert_json "$work/ingest.json" '"errors": 0' "$version ingest"

  run_roca "$home" exec \
    "SELECT (SELECT COUNT(*) FROM sessions WHERE session_id = '11111111-2222-3333-4444-555555555555') AS core_sessions, (SELECT COUNT(*) FROM plugin_roca_corpus.sessions WHERE session_id = '11111111-2222-3333-4444-555555555555') AS corpus_sessions" \
    > "$work/old-session.json"
  assert_json "$work/old-session.json" "\"core_sessions\": $frozen_in_core" "$version historical session in core"
  assert_json "$work/old-session.json" "\"corpus_sessions\": $frozen_in_corpus" "$version historical session in corpus"

  run_roca "$home" exec \
    "SELECT (SELECT COUNT(*) FROM exchanges WHERE human_text = 'remember the frozen amber compass' AND agent_text = 'the frozen amber compass is recorded') AS core_exchanges, (SELECT COUNT(*) FROM plugin_roca_corpus.exchanges WHERE human_text = 'remember the frozen amber compass' AND agent_text = 'the frozen amber compass is recorded') AS corpus_exchanges" \
    > "$work/old-exchange.json"
  assert_json "$work/old-exchange.json" "\"core_exchanges\": $frozen_in_core" "$version historical exchange in core"
  assert_json "$work/old-exchange.json" "\"corpus_exchanges\": $frozen_in_corpus" "$version historical exchange in corpus"

  run_roca "$home" exec \
    "SELECT SUM(found) AS attributed_sessions FROM (
       SELECT COUNT(*) AS found FROM sessions
       WHERE session_id = '11111111-2222-3333-4444-555555555555'
         AND source_surface = 'Claude Code'
       UNION ALL
       SELECT COUNT(*) AS found FROM plugin_roca_corpus.sessions
       WHERE session_id = '11111111-2222-3333-4444-555555555555'
         AND source_surface = 'Claude Code'
     )" \
    > "$work/old-surface.json"
  assert_json "$work/old-surface.json" '"attributed_sessions": 1' "$version historical surface"

  # New ingest lands in the bundled corpus the upgrade installs, so the frozen
  # core keeps only its history and the current run is asserted where it is
  # written.
  run_roca "$home" exec \
    "SELECT COUNT(*) AS current_exchanges FROM plugin_roca_corpus.exchanges e
     JOIN plugin_roca_corpus.sessions s USING (session_id)
     WHERE e.session_id = '99999999-8888-7777-6666-555555555555'
       AND e.model = 'fixture-current-model' AND e.tokens_in = 11 AND e.tokens_out = 7
       AND s.source_surface = 'Claude Code'" \
    > "$work/current-exchange.json"
  assert_json "$work/current-exchange.json" '"current_exchanges": 1' "$version current ingest"

  # The assertion is that the migrated column is queryable, so the expected
  # value has to hold whatever the frozen home stored: an aggregate over an
  # unadopted database cannot name `source_model` at all.
  run_roca "$home" exec \
    "SELECT COUNT(source_model) >= 0 AS migrated_memory_column FROM memories" \
    > "$work/current-schema.json"
  assert_json "$work/current-schema.json" '"migrated_memory_column": 1' "$version current schema"

  run_roca "$home" doctor > "$work/doctor.json"
  assert_json "$work/doctor.json" '"config_exists": true' "$version config health"
  assert_json "$work/doctor.json" '"prompt_exists": true' "$version prompt health"
  assert_json "$work/doctor.json" '"model_disabled": true' "$version offline health"

  # Anchored on the report's own verdict: every nested check carries a `status`
  # of its own, and an unanchored match passes on a failing report.
  run_roca "$home" health > "$work/health.json"
  assert_json "$work/health.json" '^  "status": "pass"' "$version data health"
  echo "upgrade gauntlet: $version passed"
}

ran=0
for version in "${versions[@]}"; do
  if [ -z "$requested" ] || [ "$requested" = "$version" ]; then
    run_fixture "$version"
    ran=$((ran + 1))
  fi
done

expected="${#versions[@]}"
if [ -n "$requested" ]; then
  expected=1
fi
if [ "$ran" -ne "$expected" ]; then
  echo "upgrade gauntlet ran $ran of the $expected homes in $versions_file" >&2
  exit 1
fi
