#!/usr/bin/env python3
"""Local CLI contract fixture; no real AX, Substrate or network execution."""
import base64, io, json, os, pathlib, shlex, sqlite3, subprocess, sys, tarfile, time

root = pathlib.Path(__file__).resolve().parent
config = json.loads((root / 'config.json').read_text())
args = [v for v in sys.argv[1:] if not v.startswith('--namespace=') and not v.startswith('--atespace=') and not v.startswith('--context=')]
state_path = root / 'remote.json'
state = json.loads(state_path.read_text()) if state_path.exists() else {}
def save(): state_path.write_text(json.dumps(state))
def record(operation, **values):
    with (root / 'calls.jsonl').open('a') as stream: stream.write(json.dumps({'operation': operation, **values}) + '\n')
op = args[0]
if op == 'apply':
    documents = [json.loads(v) for v in pathlib.Path(args[2]).read_text().split('\n---\n')]
    name = documents[0]['metadata']['name']
    db = sqlite3.connect('file:' + config['database'] + '?mode=ro', uri=True)
    rows = [json.loads(r[0]) for r in db.execute("SELECT body FROM resources WHERE kind='sandbox'")]
    db.close()
    owned = [v for v in rows if v['data']['name'] == name and v['status'] == 'prepared']
    if len(owned) != 1:
        record('unowned-apply', name=name)
        sys.exit(88)
    state.update(name=name, task=True, workspace=True)
    save()
    record('apply', name=name, sandboxId=owned[0]['id'], preparedBeforeApply=True)
    if config['mode'] == 'partial': sys.exit(17)
elif op == 'resume':
    record('resume', name=args[2])
elif op == 'get':
    kind = args[1]
    if kind == 'task': print('status:\n  phase: Running')
    elif kind == 'tasks':
        print('NAME ATESPACE PHASE ACTOR WORKER-IP AGE')
        if state.get('task'): print(state['name'], 'test Running actor host 1s')
    elif kind == 'workspaces':
        print('NAME ATESPACE GIT-REPOS MCP-SERVERS')
        if state.get('workspace'): print(state['name'], 'test 0 0')
    else: sys.exit(2)
    record('get', kind=kind)
elif op == 'delete':
    kind, name = args[1:3]
    if state and state['name'] != name: raise RuntimeError('unrelated deletion')
    record('delete', kind=kind, name=name)
    if config['mode'] == 'cleanup' and kind == 'task': sys.exit(19)
    state[kind] = False
    save()
elif op == 'ssh':
    command = args[3:]
    if command == ['/bin/true']: sys.exit(0)
    script = command[-1]
    guest = root / 'guest'
    if script.startswith('mkdir -p /workspace/job'):
        guest.mkdir()
        (root / 'input.b64').write_text('')
    elif script.startswith("printf '%s'"):
        with (root / 'input.b64').open('a') as stream: stream.write(shlex.split(script)[2])
    elif script.startswith('base64 -d'):
        with tarfile.open(fileobj=io.BytesIO(base64.b64decode((root / 'input.b64').read_text()))) as archive:
            archive.extractall(guest, filter='data')
    elif script.endswith('base64 < /workspace/result.tar'):
        body = io.BytesIO()
        with tarfile.open(fileobj=body, mode='w') as archive:
            for path in guest.iterdir(): archive.add(path, arcname=path.name)
        print(base64.b64encode(body.getvalue()).decode())
    elif script.startswith('cd /workspace/job && env -i '):
        argv = shlex.split(script.split('IS_ARTIFACT_PATH=/workspace/job/.intelligence-artifact ', 1)[1])
        record('command', argv=argv)
        if config['mode'] == 'crash':
            (root / 'crash-ready.json').write_text(json.dumps({'pid': os.getpid(), 'argv': argv}))
            while True: time.sleep(1)
        result = subprocess.run(argv, cwd=guest, env={'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(guest), 'IS_ARTIFACT_PATH': str(guest / '.intelligence-artifact')})
        sys.exit(result.returncode)
    else: raise RuntimeError('unexpected guest command')
else: raise RuntimeError('unexpected AX operation')
