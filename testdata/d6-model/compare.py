#!/usr/bin/env python3
"""Opt-in Darwin lab: real published/branch default queries and model reads."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import struct
import subprocess
import time

parser = argparse.ArgumentParser(description=__doc__)
for option in ('published-core', 'published-vector', 'branch-core', 'branch-vector', 'model', 'lab'):
    parser.add_argument('--' + option, required=True, type=Path)
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
lab = args.lab.resolve()
assert root / '.tmp' in lab.parents, 'lab must be below the project .tmp directory'
assert not lab.exists(), 'use a fresh lab directory; existing evidence is preserved'
lab.mkdir(parents=True, mode=0o700)
home = lab / 'home'
data = home / '.roca'
data.mkdir(parents=True)
model_size = 957680480
pin = 'a5db3381f2e514d3490a3a31fe70eb1a65e95016c85c6c2c23223b810806594f'
model = data / 'models/nomic-embed-text-v2-moe' / (pin + '.gguf')
model.parent.mkdir(parents=True)
shutil.copyfile(args.model, model)
assert model.stat().st_size == model_size
base = {k: v for k, v in os.environ.items() if not k.startswith(('ROCA_', 'DYLD_'))}
bindir = lab / 'bin'
bindir.mkdir()
base.update(HOME=str(home), ROCA_DB_PATH=str(data / 'roca.db'), ROCA_READ_ONLY='0',
            ROCA_VECTOR_STATE_DIR=str(lab / 'state'), ROCA_VECTOR_PLUGIN_ROOT=str(data / 'plugins'),
            ROCA_VECTOR_RESIDENT_SOCKET=str(lab / 's.sock'), ROCA_PREFIX=str(bindir),
            PATH=str(bindir) + os.pathsep + base['PATH'])
assert len(base['ROCA_VECTOR_RESIDENT_SOCKET']) < 100, 'lab socket path is too long'


def run(binary, argv, env, name):
    process = subprocess.run([str(binary.resolve()), *argv], env=env,
                             capture_output=True, text=True, timeout=180)
    (lab / (name + '.stdout')).write_text(process.stdout)
    (lab / (name + '.stderr')).write_text(process.stderr)
    assert process.returncode == 0, (name, process.stderr)
    return process


version = json.loads(run(args.published_core, ['version', '--json'], base, 'version').stdout)
assert version['version'] == 'v1.84.8', version
companion_version = run(args.published_vector, ['--version'], base, 'vector-version').stdout
assert 'v1.84.8' in companion_version, companion_version
# The fixture has evidence in both prepared sidecars; default query must search both.
with sqlite3.connect(data / 'roca.db') as db:
    db.executescript((root / 'data/schema.sql').read_text())
    db.execute("INSERT INTO sessions(session_id,source_agent,title) VALUES('d6-fixture','fixture','Synthetic navigation')")
    for number, text in enumerate(['A compass points toward magnetic north.',
                                  'A map shows the route through the forest.',
                                  'A lighthouse guides ships back to the harbor.'], 1):
        db.execute("INSERT INTO exchanges(session_id,exchange_number,human_text,agent_text,agent_timestamp) "
                   "VALUES('d6-fixture',?,?,?,'2026-01-01')", (number, 'How do we navigate?', text))
(data / 'config.toml').write_text('[features]\nplugins = true\nvector = true\n')
for argv in [['migrate', '--json'], ['exec', 'SELECT 1', '--json']]:
    run(args.branch_core, argv, base, argv[0])
with sqlite3.connect(data / 'plugins/roca-ops/roca-ops.db') as db:
    db.execute("INSERT INTO memories(layer,content,origin) VALUES('project',"
               "'Use a compass and map to navigate the forest.','agent')")
installed = bindir / 'roca-vector'
shutil.copyfile(args.branch_vector, installed)
installed.chmod(0o700)
setup_env = dict(base, ROCA_VECTOR_ROCA_BINARY=str(args.branch_core.resolve()))
run(installed, ['install', '--json'], setup_env, 'install')
completion = lab / 'state/completion.json'
deadline = time.monotonic() + 180
while not completion.exists():
    assert time.monotonic() < deadline, 'lab indexing did not complete'
    time.sleep(0.2)
setup = json.loads(completion.read_text())
assert not setup.get('error'), setup
for plugin in ('roca-ops', 'roca-corpus'):
    sidecar = data / ('plugins/' + plugin + '/' + plugin + '.vector.db')
    assert sidecar.exists(), 'missing prepared sidecar'
# Instrumentation observes model read/pread bytes, including cache hits; mmap
# and native model-load traffic are outside this SHA-256 read measurement.
library = lab / 'reads.dylib'
subprocess.run(['clang', '-dynamiclib', '-o', str(library), str(root / 'testdata/d6-model/reads-darwin.c')], check=True)
base.update(ROCA_D6_MODEL=str(model), DYLD_INSERT_LIBRARIES=str(library))


def counter(path):
    files = list(path.parent.glob(path.name + '.*'))
    assert files, 'missing counters'
    total = 0
    for file in files:
        raw = file.read_bytes()
        assert len(raw) == 8, 'unreadable counter'
        total += struct.unpack('Q', raw)[0]
    return total


def normalize(value):
    if isinstance(value, dict):
        return {k: normalize(v) for k, v in value.items()
                if k not in ('elapsed_ms', 'latency_ms', 'version', 'source_sha')}
    if isinstance(value, list):
        return [normalize(v) for v in value]
    return value


results = {}
for label, core, vector in [('published', args.published_core, args.published_vector),
                            ('branch', args.branch_core, args.branch_vector)]:
    env = dict(base, ROCA_VECTOR_ROCA_BINARY=str(core.resolve()))
    shutil.copyfile(vector, installed)
    installed.chmod(0o700)
    # Replace with the same pinned content on a fresh inode before each startup.
    fresh = model.with_suffix('.replacement')
    shutil.copyfile(model, fresh)
    fresh.replace(model)
    startup = lab / (label + '-startup.counter')
    log = open(lab / (label + '-resident.log'), 'w')
    resident = subprocess.Popen([str(installed), '_resident', '--listen', str(lab / 's.sock'), '--idle', '60s'],
                                env=dict(env, ROCA_D6_COUNTER=str(startup)), stdout=log, stderr=log)
    connection = socket.socket(socket.AF_UNIX)
    connection.settimeout(60)
    try:
        deadline = time.monotonic() + 60
        while True:
            try:
                connection.connect(str(lab / 's.sock'))
                break
            except (FileNotFoundError, ConnectionRefusedError):
                assert resident.poll() is None, 'resident exited'
                assert time.monotonic() < deadline, 'resident unavailable'
                time.sleep(0.1)
        stream = connection.makefile('rwb', buffering=0)
        for _ in range(2):
            event = json.loads(stream.readline())
            assert event['kind'] != 'error', event
        assert event['stage'] == 'prewarm' and event['kind'] == 'result', event
        startup_bytes = counter(startup)
        row = {'startup_bytes': startup_bytes}
        for route, binary, argv in [('direct', installed, ['query']), ('core', core, ['vector', 'query']),
                                    ('fallback', installed, ['query'])]:
            counted = lab / (label + '-' + route + '.counter')
            query_env = dict(env, ROCA_D6_COUNTER=str(counted))
            if route == 'fallback':
                refused = lab / 'refused-socket'
                refused.mkdir(exist_ok=True)
                refused.chmod(0o755)
                query_env['ROCA_VECTOR_RESIDENT_SOCKET'] = str(refused / 's.sock')
            process = run(binary, argv + ['How does a compass help navigate?', '10', '--json'],
                          query_env, label + '-' + route)
            result = json.loads(process.stdout)
            assert result['vector_executed'] and result['results'], 'query did not return vector evidence'
            assert {r['database'] for r in result['results']} == {'ops', 'corpus'}, result
            row[route] = {'bytes': counter(counted), 'result': normalize(result), 'stderr': process.stderr}
        assert counter(startup) == startup_bytes, 'resident reread model during hot queries'
        resident_log = (lab / (label + '-resident.log')).read_text()
        assert resident_log.count('answered query in') == 2, 'query used fallback or a different resident'
        assert row['direct']['result'] == row['core']['result'] == row['fallback']['result'], 'route equality failed'
        results[label] = row
    finally:
        connection.close()
        resident.terminate()
        resident.wait(timeout=10)
        log.close()
assert results['published']['direct']['result'] == results['branch']['direct']['result'], 'result equality failed'
for route in ('direct', 'core', 'fallback'):
    assert results['published'][route]['stderr'] == results['branch'][route]['stderr'], 'errors differ'
assert results['published']['startup_bytes'] == 2 * model_size
assert results['branch']['startup_bytes'] == model_size
assert results['published']['direct']['bytes'] == model_size
assert results['branch']['direct']['bytes'] == 0
assert results['published']['core']['bytes'] == results['branch']['core']['bytes'] == 0
assert results['published']['fallback']['bytes'] == 2 * model_size
assert results['branch']['fallback']['bytes'] == 0
results['binaries'] = {name: hashlib.sha256(getattr(args, name).read_bytes()).hexdigest()
                       for name in ('published_core', 'published_vector', 'branch_core', 'branch_vector')}
(lab / 'comparison.json').write_text(json.dumps(results, indent=2))
print('PASS: same databases, rows, scores, notices and errors; startup 2 -> 1 full reads; direct hot 1 -> 0; core hot 0 -> 0; local fallback 2 -> 0')
