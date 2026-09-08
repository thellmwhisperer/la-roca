#!/usr/bin/env python3
"""D7 lab: measure an unmodified vector executable on synthetic SQLite sources.

Usage: measure-batches.py LAB LABEL VECTOR_BINARY MODEL_GGUF
Each LABEL must be new. LAB owns every database, HOME, claim and subprocess.
The --core seat substitutes only read-only source SQL, never embeddings.
"""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import struct
import subprocess
import sys
import time


def core_fixture():
    root = Path(os.environ["ROCA_VECTOR_PLUGIN_ROOT"])
    databases = json.loads((root / "vector-registry.json").read_text())["databases"]
    if "_database-scope" in sys.argv:
        return {"databases": [d["database"] for d in databases], "selected": [
            {"source": "plugin:" + d["plugin"], "database": d["database"]} for d in databases]}
    with sqlite3.connect(":memory:") as db:
        db.row_factory = sqlite3.Row
        for d in databases:
            source = root / d["plugin"] / d["path"]
            db.execute("ATTACH DATABASE ? AS " + d["alias"], (source.as_uri() + "?mode=ro",))
        return {"rows": [dict(row) for row in db.execute(sys.argv[-1])]}


def measure(lab, label, binary, model):
    root = lab.resolve() / label
    root.mkdir()  # Refuse to overwrite an earlier measurement.
    for directory in ("home", "state", "plugins"):
        (root / directory).mkdir()
    digest = "a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f"
    target = root / "models/nomic-embed-text-v2-moe" / (digest + ".gguf")
    target.parent.mkdir(parents=True)
    target.symlink_to(model.resolve())  # The executable verifies the pinned model.
    registry = {"schema": 2, "databases": []}
    readers, audits = [], []
    for i in range(2):
        plugin = f"lab-{i}"
        directory = root / "plugins" / plugin
        directory.mkdir()
        with sqlite3.connect(directory / "source.db") as db:
            db.execute("CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT, occurred_at TEXT)")
            db.executemany("INSERT INTO notes VALUES(?,?,?)", [
                (n, "hello", f"{n * 2 + i:06d}") for n in range(130 + i)])
        registry["databases"].append({"plugin": plugin, "database": f"lab_{i}",
            "path": "source.db", "alias": f"plugin_lab_{i}", "tables": [{
                "name": "notes", "id_column": "id", "text_columns": ["body"],
                "time_columns": ["occurred_at"]}]})
        sidecar = directory / "source.vector.db"
        db = sqlite3.connect(sidecar)
        db.executescript("""PRAGMA journal_mode=WAL;
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT INTO meta VALUES('schema','vector-v2');
CREATE TABLE chunks(id INTEGER PRIMARY KEY,source_kind TEXT NOT NULL,source_id TEXT NOT NULL,
 text_column TEXT NOT NULL DEFAULT '',chunk_index INTEGER NOT NULL,fingerprint TEXT NOT NULL,
 source_fingerprint TEXT NOT NULL DEFAULT '',locator TEXT NOT NULL,
 updated_at TEXT NOT NULL DEFAULT (datetime('now')),
 UNIQUE(source_kind,source_id,text_column,chunk_index));
CREATE TABLE batch_audit(n INTEGER); INSERT INTO batch_audit VALUES(0);
CREATE TRIGGER batch_audit_insert AFTER INSERT ON chunks BEGIN UPDATE batch_audit SET n=n+1; END;
CREATE TRIGGER batch_audit_update AFTER UPDATE ON chunks BEGIN UPDATE batch_audit SET n=n+1; END;
PRAGMA wal_checkpoint(TRUNCATE);""")
        page = db.execute("SELECT rootpage FROM sqlite_master WHERE name='batch_audit'").fetchone()[0]
        db.execute("BEGIN")
        db.execute("SELECT n FROM batch_audit").fetchone()  # Prevent WAL recycling.
        readers.append(db)
        audits.append((sidecar, page))
    (root / "plugins/vector-registry.json").write_text(json.dumps(registry))
    env = {key: value for key, value in os.environ.items() if not key.startswith("ROCA_")}
    env.update(HOME=str(root / "home"), TMPDIR=str(root), GOMAXPROCS="2",
        ROCA_VECTOR_PLUGIN_ROOT=str(root / "plugins"), ROCA_BATCH_LAB_CORE="1",
        ROCA_VECTOR_ROCA_BINARY=str(Path(__file__).resolve()))
    command = [str(binary.resolve()), "--db-path", str(root / "roca.db"),
        "--state-dir", str(root / "state"), "ingest", "--delta", "--model",
        "nomic-embed-text-v2-moe", "--accelerate", "--json"]
    with (root / "stdout").open("w") as out, (root / "stderr").open("w") as err:
        start = time.monotonic()
        process = subprocess.Popen(command, env=env, stdout=out, stderr=err)
        (root / "state/.worker").write_text(f"{process.pid} lab-run fixture\n")
        status = process.wait()
        elapsed = time.monotonic() - start
    counts = []
    for sidecar, page in audits:
        wal = Path(str(sidecar) + "-wal").read_bytes()
        count, touched = 0, False
        if len(wal) >= 32:
            size = struct.unpack(">I", wal[8:12])[0]
            for offset in range(32, len(wal) - 23 - size, 24 + size):
                number, commit = struct.unpack(">II", wal[offset:offset + 8])
                assert wal[offset + 8:offset + 16] == wal[16:24], "WAL recycled"
                touched |= number == page
                if commit:
                    count += touched
                    touched = False
        counts.append(count)
    evidence = {"label": label, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "exit_status": status, "seconds": round(elapsed, 2), "chunks": 261, "databases": 2,
        "embedding_transactions": counts, "transaction_limit": 7,
        "backend": "accelerator requested; see stderr for actual backend",
        "fixture": "130 + 131 one-word sources; pinned WAL reader and audit trigger; synthetic core SQL"}
    (root / "evidence.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence))
    for reader in readers:
        reader.close()
    return status


if __name__ == "__main__":
    if os.environ.get("ROCA_BATCH_LAB_CORE"):
        print(json.dumps(core_fixture()))
    else:
        lab, label, binary, model = sys.argv[1:]
        sys.exit(measure(Path(lab), label, Path(binary), Path(model)))
