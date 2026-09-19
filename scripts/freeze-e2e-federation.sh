#!/usr/bin/env bash
# Freeze the synthetic federation homes the e2e suite copies. The suite never
# runs this script. CI reads testdata/e2e-federation/frozen.tar.gz only.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -x bin/roca ]; then
  echo "run make build first" >&2
  exit 1
fi

stage="$(mktemp -d /tmp/roca-e2e-federation.XXXXXX)"
cleanup() { rm -rf "$stage"; }
trap cleanup EXIT

roca="$(pwd)/bin/roca"
root="$(pwd)"
export ROCA_MODELS_ORDER=none

init_home() {
  local home="$1"
  mkdir -p "$home/.roca" "$home/tmp" "$home/bin"
  cp "$roca" "$home/bin/roca"
  chmod +x "$home/bin/roca"
  if [ -x .tmp/roca-vector-native ]; then
    cp .tmp/roca-vector-native "$home/bin/roca-vector"
    chmod +x "$home/bin/roca-vector"
  fi
  env -i HOME="$home" PATH="$home/bin:/usr/bin:/bin" TMPDIR="$home/tmp" ROCA_MODELS_ORDER=none \
    "$home/bin/roca" --db-path "$home/.roca/roca.db" --json init >/dev/null
  python3 - "$home/.roca/config.toml" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
body = path.read_text()
body = body.replace("vector = false", "vector = true")
if "vector = true" not in body:
    if "[features]" in body:
        body = body.replace("[features]", "[features]\nvector = true", 1)
    else:
        body += "\n[features]\nvector = true\n"
if "vector_consent" not in body:
    body = body.replace("[features]", "[features]\nvector_consent = true", 1)
path.write_text(body)
PY
}

copy_sources() {
  local home="$1"
  mkdir -p "$home/.codex" "$home/.claude/projects"
  cp -R testdata/e2e-federation/sources/codex/. "$home/.codex/"
  cp -R testdata/e2e-federation/sources/claude/projects/. "$home/.claude/projects/"
}

apply_sql() {
  local db="$1"
  local sql="$2"
  go run scripts/e2e-federation-sql.go "$db" "$sql"
}

run_roca() {
  local home="$1"
  shift
  env -i HOME="$home" PATH="$home/bin:/usr/bin:/bin" TMPDIR="$home/tmp" ROCA_MODELS_ORDER=none \
    "$home/bin/roca" "$@"
}

LONG="$(python3 -c 'print("0123456789"*200 + " branch: lab done: seeded state: testing next: verify budgets")')"

# pill-free seeded home
pill_free="$stage/pill-free"
init_home "$pill_free"
copy_sources "$pill_free"
run_roca "$pill_free" ingest --json >/dev/null
run_roca "$pill_free" store --layer handoff --content \
  "branch: lab scope: harbor done: seeded the frozen federation state: ready next: run the installed binary suite" \
  --origin agent --agent codex --project harbor >/dev/null
run_roca "$pill_free" store --layer handoff --content \
  "branch: lab scope: dock done: second project receipt state: ready next: cross-project view" \
  --origin agent --agent codex --project dock >/dev/null
run_roca "$pill_free" store --layer handoff --content "$LONG" \
  --origin agent --agent codex --project budgets >/dev/null
run_roca "$pill_free" store --layer discovery --content \
  "the harbor lantern marks the synthetic federation row" \
  --origin agent --agent codex >/dev/null

# main = pill-free plus the uso-de-la-roca pill
main="$stage/main"
cp -R "$pill_free" "$main"
run_roca "$main" store --layer pill --content \
  "How an agent searches La Roca: binary first, vectors first, then qualified exec. Never open the database files." \
  --metadata '{"pill_slug":"uso-de-la-roca"}' --origin agent --agent codex >/dev/null

# pr321: initialized home, alias rows, sources, history ingest state removed
pr321="$stage/pr321"
init_home "$pr321"
copy_sources "$pr321"
corpus_321="$pr321/.roca/plugins/roca-corpus/roca-corpus.db"
apply_sql "$corpus_321" testdata/e2e-federation/seed/pr321-alias.sql
apply_sql "$corpus_321" testdata/e2e-federation/seed/pr321-clear-history-state.sql

# pr324: initialized home, stem-split rows, sources
pr324="$stage/pr324"
init_home "$pr324"
copy_sources "$pr324"
apply_sql "$pr324/.roca/plugins/roca-corpus/roca-corpus.db" testdata/e2e-federation/seed/pr324-stem.sql

# Optional tiny vector sidecars. A missing model is not a freeze failure.
for home in "$main" "$pill_free"; do
  run_roca "$home" vector ingest --delta >/dev/null 2>&1 || true
done

archive_root="$stage/archive"
mkdir -p "$archive_root"
for name in main pill-free pr321 pr324; do
  src="$stage/$name"
  dest="$archive_root/$name"
  mkdir -p "$dest"
  rm -rf "$src/.roca/logs" "$src/tmp" "$src/bin"
  rm -f "$src/.roca/plugins/.roca-vector.relocation.lock"
  mkdir -p "$src/.roca/plugins/roca-vector/state"
  cp -R "$src/." "$dest/"
done

archive="$stage/frozen.tar.gz"
python3 - "$archive_root" "$stage" "$archive" <<'PY_NORMALIZE'
import pathlib, sqlite3, subprocess, sys, tarfile
root = pathlib.Path(sys.argv[1])
stage = pathlib.Path(sys.argv[2])
prefixes = []
for name in ("main", "pill-free", "pr321", "pr324"):
    home = stage / name
    prefixes.extend((str(home.resolve()), str(home)))
def normalize(text):
    for prefix in prefixes:
        text = text.replace(prefix, "/synthetic/e2e-federation")
    return text
for path in root.rglob("*"):
    if not path.is_file():
        continue
    if path.suffix == ".db":
        with sqlite3.connect(path) as db:
            tables = [r[1] for r in db.execute("PRAGMA table_list")
                      if r[2] == "table" and not r[1].startswith("sqlite_")]
            quote = lambda name: '"' + name.replace('"', '""') + '"'
            for table in tables:
                for col in db.execute(f"PRAGMA table_info({quote(table)})").fetchall():
                    column = quote(col[1])
                    for prefix in prefixes:
                        where = f"typeof({column})='text' AND instr({column}, ?) > 0"
                        if db.execute(f"SELECT count(*) FROM {quote(table)} WHERE {where}", (prefix,)).fetchone()[0]:
                            db.execute(
                                f"UPDATE {quote(table)} SET {column}=replace({column}, ?, '/synthetic/e2e-federation') WHERE {where}",
                                (prefix, prefix))
        db.close()
    elif path.suffix in (".json", ".toml", ".md", ".jsonl"):
        path.write_text(normalize(path.read_text()))
dbs = [str(p) for p in root.rglob("*.db")]
if dbs:
    subprocess.run(["go", "run", "scripts/freeze-upgrade-vacuum.go", *dbs], check=True)

def owner(info):
    info.uid = info.gid = 0
    info.uname = info.gname = "root"
    return info
with tarfile.open(sys.argv[3], "w:gz") as archive:
    archive.add(root, arcname=".", filter=owner)
PY_NORMALIZE

mkdir -p testdata/e2e-federation
mv -f "$archive" testdata/e2e-federation/frozen.tar.gz
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum testdata/e2e-federation/frozen.tar.gz | awk '{print $1}' > testdata/e2e-federation/frozen.sha256
else
  shasum -a 256 testdata/e2e-federation/frozen.tar.gz | awk '{print $1}' > testdata/e2e-federation/frozen.sha256
fi

echo "froze testdata/e2e-federation/frozen.tar.gz"
echo "sha256 $(cat testdata/e2e-federation/frozen.sha256)"
