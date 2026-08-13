#!/usr/bin/env bash
# Exercise the real current CLI against homes frozen by historical releases.
set -euo pipefail

cd "$(dirname "$0")/.."

binary="${1:-bin/roca}"
requested="${2:-}"
versions_file="internal/distribution/release/testdata/upgrade/versions.txt"
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

assert_contains() {
  file="$1"
  value="$2"
  label="$3"
  if ! grep -Fq "$value" "$file"; then
    echo "$label: expected $value in $file" >&2
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

  run_roca "$home" ingest > "$work/ingest.json"
  assert_contains "$work/ingest.json" '"errors": 0' "$version ingest"

  run_roca "$home" exec \
    "SELECT COUNT(*) AS old_sessions FROM sessions WHERE session_id = '11111111-2222-3333-4444-555555555555'" \
    > "$work/old-session.json"
  assert_contains "$work/old-session.json" '"old_sessions": 1' "$version historical session"

  run_roca "$home" exec \
    "SELECT COUNT(*) AS old_exchanges FROM exchanges WHERE human_text = 'remember the frozen amber compass' AND agent_text = 'the frozen amber compass is recorded'" \
    > "$work/old-exchange.json"
  assert_contains "$work/old-exchange.json" '"old_exchanges": 1' "$version historical exchange"

  run_roca "$home" exec \
    "SELECT COUNT(*) AS current_exchanges FROM exchanges WHERE session_id = '99999999-8888-7777-6666-555555555555' AND model = 'fixture-current-model' AND tokens_in = 11 AND tokens_out = 7" \
    > "$work/current-exchange.json"
  assert_contains "$work/current-exchange.json" '"current_exchanges": 1' "$version current ingest"

  run_roca "$home" exec \
    "SELECT COUNT(*) AS current_memory_shape FROM memories WHERE source_model IS NULL OR source_model IS NOT NULL" \
    > "$work/current-schema.json"
  assert_contains "$work/current-schema.json" '"current_memory_shape": 0' "$version current schema"

  run_roca "$home" doctor > "$work/doctor.json"
  assert_contains "$work/doctor.json" '"config_exists": true' "$version config health"
  assert_contains "$work/doctor.json" '"prompt_exists": true' "$version prompt health"
  assert_contains "$work/doctor.json" '"model_disabled": true' "$version offline health"

  run_roca "$home" health > "$work/health.json"
  assert_contains "$work/health.json" '"status": "pass"' "$version data health"
  echo "upgrade gauntlet: $version passed"
}

while IFS= read -r version; do
  if [ -z "$requested" ] || [ "$requested" = "$version" ]; then
    run_fixture "$version"
  fi
done < "$versions_file"
