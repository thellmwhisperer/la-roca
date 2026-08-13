#!/usr/bin/env bash
# Freeze a synthetic home produced by an actual release binary. CI consumes the
# committed result; it never downloads or rebuilds historical releases.
set -euo pipefail

cd "$(dirname "$0")/.."

versions_file="internal/distribution/release/testdata/upgrade/versions.txt"
github_cli="${GH_RELEASE_CLI:-gh}"
requested="${1:-}"

if [ -z "$requested" ]; then
  echo "usage: $0 <released-vX.Y.Z>" >&2
  exit 2
fi
if ! grep -Fxq "$requested" "$versions_file"; then
  echo "$requested is not listed in $versions_file" >&2
  exit 2
fi
if ! command -v "$github_cli" >/dev/null 2>&1; then
  echo "$github_cli is required (set GH_RELEASE_CLI to override)" >&2
  exit 1
fi

case "$(uname -s):$(uname -m)" in
  Darwin:arm64) platform="darwin-arm64" ;;
  Linux:x86_64 | Linux:amd64) platform="linux-x64" ;;
  Linux:arm64 | Linux:aarch64) platform="linux-arm64" ;;
  *)
    echo "no released La Roca binary for $(uname -s)/$(uname -m)" >&2
    exit 1
    ;;
esac

mkdir -p .tmp
stage="$(mktemp -d ".tmp/freeze-upgrade-home.${requested}.XXXXXX")"
home="$stage/home"
destination="internal/distribution/release/testdata/upgrade/homes/$requested.tar.gz"
asset="roca-${requested}-${platform}"
mkdir -p "$home/.roca"

# GH_RELEASE_CLI defaults to gh, so these are `gh release download` calls for
# contributors. Agent workspaces can select their compatible wrapper instead.
"$github_cli" release download "$requested" --pattern "$asset" --dir "$stage"
"$github_cli" release download "$requested" --pattern checksums.txt --dir "$stage"
chmod +x "$stage/$asset"

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha="$(sha256sum "$stage/$asset" | awk '{print $1}')"
else
  actual_sha="$(shasum -a 256 "$stage/$asset" | awk '{print $1}')"
fi
expected_sha="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$stage/checksums.txt")"
if [ -z "$expected_sha" ] || [ "$actual_sha" != "$expected_sha" ]; then
  echo "checksum mismatch for $asset" >&2
  exit 1
fi
case "$("$stage/$asset" --version)" in
  "roca $requested "*) ;;
  *) echo "$asset does not report $requested" >&2; exit 1 ;;
esac

# An explicit empty order keeps fixture production offline and proves that the
# historical config continues to parse after an upgrade.
printf '[defaults]\nanthropic_export_paths = ["/synthetic/upgrade-export"]\n\n[models]\norder = []\n' \
  > "$home/.roca/config.toml"
env -i HOME="$home" PATH=/usr/bin:/bin ROCA_MODELS_ORDER=none \
  "$stage/$asset" --db-path "$home/.roca/roca.db" --json init > "$stage/init.json"

# Seed after init so this explicit ingest, rather than init's bootstrap scan,
# creates the historical row. Every value is unmistakably synthetic.
session_dir="$home/.claude/projects/-synthetic-upgrade-fixture"
mkdir -p "$session_dir"
cat > "$session_dir/11111111-2222-3333-4444-555555555555.jsonl" <<'JSONL'
{"type":"user","sessionId":"11111111-2222-3333-4444-555555555555","timestamp":"2026-08-01T10:00:00Z","cwd":"/synthetic/upgrade-fixture","message":{"content":"remember the frozen amber compass"}}
{"type":"assistant","sessionId":"11111111-2222-3333-4444-555555555555","timestamp":"2026-08-01T10:00:01Z","message":{"model":"fixture-model","content":[{"type":"text","text":"the frozen amber compass is recorded"}]}}
JSONL
env -i HOME="$home" PATH=/usr/bin:/bin ROCA_MODELS_ORDER=none \
  "$stage/$asset" --db-path "$home/.roca/roca.db" --json ingest > "$stage/ingest.json"

# Logs contain generation time and invocation paths; they are operational
# output, not part of the user state the gauntlet needs to upgrade.
rm -rf "$home/.roca/logs"
archive_root="$stage/archive"
mkdir -p "$archive_root"
cp -R "$home/.roca" "$archive_root/.roca"
cat > "$archive_root/origin.json" <<JSON
{
  "release": "$requested",
  "asset": "$asset",
  "sha256": "$actual_sha"
}
JSON
archive="$stage/$requested.tar.gz"
COPYFILE_DISABLE=1 tar -C "$archive_root" -czf "$archive" .
mkdir -p "$(dirname "$destination")"
mv -f "$archive" "$destination"

rm -rf "$stage"
echo "froze $destination from $asset"
