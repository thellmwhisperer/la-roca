import json, os, pathlib, re, shlex, shutil, subprocess
root = pathlib.Path.cwd()
evidence = pathlib.Path(__file__).parent
home = root / '.tmp/e2e-home'
if home.exists():
    shutil.rmtree(home)
home.mkdir(parents=True, exist_ok=True)
(home / '.roca').mkdir(exist_ok=True)
(home / 'bin').mkdir(exist_ok=True)
(home / 'tmp').mkdir(exist_ok=True)
binary = root / '.tmp/roca-test'
env = {'HOME': str(home), 'PATH': str(home / 'bin'), 'TMPDIR': str(home / 'tmp')}
config = home / '.roca/config.toml'
config.write_text('[models]\norder = []\n[features]\nplugins = true\nvector = false\n')
records = []
def clean(text):
    text = re.sub(r'\bqf_[0-9a-f]+\b', '<correlation-id>', text)
    text = text.replace(str(home), '<fixture-home>').replace(str(root), '<workspace>')
    return re.sub(r'\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b', '<correlation-id>', text, flags=re.I)
def run(*args, code=0):
    p = subprocess.run([str(binary), *args], input='', text=True, capture_output=True, env=env, timeout=40)
    records.append('$ roca ' + clean(shlex.join(args)) + '\n' + clean(p.stdout + p.stderr) + f'\nexit: {p.returncode}\n')
    assert p.returncode == code, records[-1]
    return p.stdout + p.stderr
def doc(*args, code=0):
    return json.loads(run(*args, code=code))
try:
    out = run('playground', 'cedar deployment decision', code=1)
    assert 'roca plugin install thellmwhisperer/roca-playground' in out
    run('init', '--db-path', str(home / '.roca/roca.db'), '--json')
    run('store', '--layer', 'project', '--content', 'cedar deployment uses staged rollout', '--origin', 'human', '--json')
    run('index', '--json')
    before = doc('doctor', '--json')
    assert not before.get('providers') and not before.get('titular_provider')
    result = doc('query', 'cedar', '--json')
    assert 'cedar deployment uses staged rollout' in json.dumps(result)
    run('plugin', 'install', str(root / '.tmp/playground/dist/package'), '--yes', '--json')
    provider = home / 'bin/fixture-provider'
    provider.write_text('''#!/bin/sh
/bin/cat >/dev/null
if [ -f "$HOME/provider-called" ]; then
  printf '%s' 'The cedar deployment uses staged rollout.'
else
  : > "$HOME/provider-called"
  printf '%s' 'SELECT id, content AS text FROM main.memories ORDER BY id LIMIT 1'
fi
''')
    provider.chmod(0o700)
    config.write_text('[models]\norder = ["local-binary"]\n[models.local-binary]\ncommand = [' + json.dumps(str(provider)) + ']\nmodel = "synthetic-provider"\ntimeout_seconds = 2\n[features]\nplugins = true\nvector = false\n')
    after = doc('doctor', '--json')
    assert after['titular_provider'] == 'local-binary' and after['providers'][0]['ready']
    marker = home / 'provider-called'
    marker.unlink(missing_ok=True)
    result = doc('query', 'cedar', '--json')
    assert 'cedar deployment uses staged rollout' in json.dumps(result) and not marker.exists()
    result = doc('exec', 'SELECT content FROM main.memories', '--json')
    assert 'cedar deployment uses staged rollout' in json.dumps(result) and not marker.exists()
    result = doc('playground', 'cedar deployment decision', '--json')
    assert result['path'] == 'model' and result['row_count'] == 1
    assert result['rows'][0]['text'] == 'cedar deployment uses staged rollout'
    marker.unlink(missing_ok=True)
    result = doc('playground', 'cedar deployment decision', '--sql-only', '--json')
    assert result['sql'].startswith('SELECT') and not result.get('rows')
    marker.unlink(missing_ok=True)
    out = run('playground', 'cedar deployment decision', '--full')
    assert 'The cedar deployment uses staged rollout.' in out
    marker.unlink(missing_ok=True)
    result = doc('explore', 'cedar', '--json')
    assert result['mode'] == 'explore' and result['row_count'] == 1
    assert result['interpretation'] == 'The cedar deployment uses staged rollout.'
    marker.unlink(missing_ok=True)
    result = doc('explore', 'cedar', '--deep', '--json')
    assert result['mode'] == 'explore_deep' and result['interpretation']
    result = doc('exec', 'SELECT content FROM main.memories', '--json')
    assert 'cedar deployment uses staged rollout' in json.dumps(result)
finally:
    header = 'Synthetic CLI verification. Core: 25b88be170b523b133bda7687bfac49d7f6e63e4.\nCompanion: f80f466b24b0b9d2d6ecff8f839ccbd0f3d60b53.\nProvider replies are deterministic fixtures; no live model or real federation is used.\nPaths and correlation IDs are normalized; outputs otherwise retained.\n\n'
    (evidence / 'playground-cli-transcript.txt').write_text(header + '\n'.join(records))
print('CLI evidence completed: optional install hint, real plugin install, conditional probe, deterministic query/exec, SQL, prose, and exploration.')
