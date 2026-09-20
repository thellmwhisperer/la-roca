#!/usr/bin/env bash
# Freeze the synthetic federation homes the e2e suite copies. The suite never
# runs this script. CI reads testdata/e2e-federation/frozen only.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -x bin/roca ]; then
  echo "run make build first" >&2
  exit 1
fi

mkdir -p .tmp
stage="$(mktemp -d "$(pwd)/.tmp/roca-e2e-federation.XXXXXX")"
cleanup() { rm -rf "$stage"; }
trap cleanup EXIT

model="${ROCA_E2E_VECTOR_MODEL:-}"
if [ ! -f "$model" ]; then
  echo "set ROCA_E2E_VECTOR_MODEL to the pinned embedding model" >&2
  exit 1
fi
model_sha="a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f"

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
  env -i HOME="$home" PATH="$home/bin:/usr/bin:/bin" TMPDIR="$home/tmp" ROCA_MODELS_ORDER=none \
    "$home/bin/roca" --db-path "$home/.roca/roca.db" --json _install-bundled-plugins >/dev/null
  mkdir -p "$home/.roca/models/nomic-embed-text-v2-moe"
  ln -s "$model" "$home/.roca/models/nomic-embed-text-v2-moe/$model_sha.gguf"
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

# The ready sidecars are part of the frozen contract. The large pinned model is
# supplied only while freezing and is deliberately excluded from the archive.
for home in "$main"; do
  rm -f "$home/.roca/plugins/.roca-vector.relocation.lock"
  state="$home/.roca/plugins/roca-vector/state"
  mkdir -p "$state"
  run_roca "$home" vector install --json >/dev/null
  for _ in $(seq 1 600); do
    [ -f "$state/completion.json" ] && break
    sleep 0.1
  done
  python3 - "$state/completion.json" <<'PY_COMPLETION'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
if not path.is_file():
    raise SystemExit("vector freeze worker did not complete")
completion = json.loads(path.read_text())
if completion.get("exit_status") != 0:
    raise SystemExit(f"vector freeze worker failed: {completion}")
PY_COMPLETION
done

archive_root="$stage/archive"
mkdir -p "$archive_root"
for name in main pill-free pr321 pr324; do
  src="$stage/$name"
  dest="$archive_root/$name"
  mkdir -p "$dest"
  rm -rf "$src/.roca/logs" "$src/.roca/models" "$src/tmp" "$src/bin"
  rm -rf "$src/.roca/plugins/roca-vector"
  rm -f "$src/.local/bin/roca-vector"
  rm -f "$src/.roca/plugins/.roca-vector.relocation.lock"
  find "$src" -name '*.md' -delete
  cp -R "$src/." "$dest/"
  apply_sql "$dest/.roca/plugins/roca-corpus/roca-corpus.db" testdata/e2e-federation/seed/sanitize-machine.sql
done

python3 - "$archive_root" "$stage" <<'PY_NORMALIZE'
import hashlib, pathlib, sqlite3, subprocess, sys
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

dest = pathlib.Path("testdata/e2e-federation/frozen")
if dest.exists():
    subprocess.run(["rm", "-rf", str(dest)], check=True)
dest.parent.mkdir(parents=True, exist_ok=True)
subprocess.run(["cp", "-R", str(root), str(dest)], check=True)
rows = []
for path in sorted(dest.rglob("*.db")):
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    rows.append(f"{digest}  {path.relative_to(dest).as_posix()}")
pathlib.Path("testdata/e2e-federation/frozen.sha256").write_text("\n".join(rows) + "\n")
PY_NORMALIZE

echo "froze testdata/e2e-federation/frozen"
echo "sha256 $(wc -l < testdata/e2e-federation/frozen.sha256 | tr -d ' ') databases"
