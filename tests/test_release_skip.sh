#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
python3 - "$ROOT" <<'PY'
import json, os, pathlib, shutil, subprocess, sys, tempfile
root = pathlib.Path(sys.argv[1])
with tempfile.TemporaryDirectory() as d:
    p = pathlib.Path(d); repo = p/'repo'; repo.mkdir(); tools = p/'tools'; tools.mkdir()
    def run(*args, env=None):
        r = subprocess.run(args, cwd=repo, env=env, text=True, capture_output=True)
        assert r.returncode == 0, (args, r.stdout, r.stderr)
        return r.stdout.strip()
    def git(*args): return run('git', *args)
    git('init', '-q', '-b', 'main')
    git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'Test')
    (repo/'scripts').mkdir(); (repo/'.github/workflows').mkdir(parents=True)
    shutil.copy(root/'scripts/release.sh', repo/'scripts/release.sh')
    shutil.copy(root/'scripts/package-release.sh', repo/'scripts/package-release.sh')
    (repo/'bin/st-lib').mkdir(parents=True); (repo/'tmux').mkdir()
    (repo/'bin/st').write_text("ST_VERSION='dev'\n")
    (repo/'bin/st-lib/core.sh').write_text('true\n')
    (repo/'scripts/legacy-upgrade.sh').write_text("ST_VERSION='dev'\n")
    (repo/'install.sh').write_text('true\n')
    (repo/'tmux/supertree.conf').write_text('config\n')
    (repo/'.github/workflows/release.yml').write_text('workflow\n')
    git('add', '.'); git('commit', '-qm', 'base'); git('tag', 'v1.0.0')
    git('remote', 'add', 'origin', str(repo))
    state = p/'state.json'; state.write_text(json.dumps({'latest':'v1.0.0', 'labels':{}, 'published':[]}))
    gh = tools/'gh'
    gh.write_text('''#!/usr/bin/env python3
import json, os, subprocess, sys
p=os.environ['STATE']; s=json.load(open(p)); a=sys.argv[1:]
if a[0]=='api':
 if a[1].endswith('/releases/latest'): print(s['latest'])
 else:
  sha=a[1].split('/')[-2]; print('\\n'.join(s['labels'].get(sha,[])))
elif a[:2]==['release','view']: sys.exit(1)
elif a[:2]==['release','create']:
 subprocess.check_call(['git','tag',a[2],a[a.index('--target')+1]])
elif a[:2]==['release','edit']:
 s['latest']=a[2]; s['published'].append([a[2],subprocess.check_output(['git','rev-parse',a[2]],text=True).strip()]); json.dump(s,open(p,'w'))
'''); gh.chmod(0o755)
    env = dict(os.environ, PATH=str(tools)+os.pathsep+os.environ['PATH'], STATE=str(state), GITHUB_REPOSITORY='test/repo')
    def commit(labels, fail=False):
        marker=repo/'fail-check'
        if fail: marker.write_text('fail')
        elif marker.exists(): marker.unlink()
        git('add', '-A'); git('commit','-q','--allow-empty','-m','merge'); sha=git('rev-parse','HEAD')
        data=json.loads(state.read_text()); data['labels'][sha]=labels; state.write_text(json.dumps(data)); return sha
    def release():
        run('bash','-c', 'source scripts/release.sh; check_source() { [[ ! -f $1/fail-check ]]; }; package_release() { : > "$2/asset"; }; main', env=env)
    def published(): return json.loads(state.read_text())['published']
    skipped=commit(['release:skip','release:major']); release()
    assert published()==[], ('skipped tip was released', published())
    release(); assert published()==[], ('retry released skipped tip', published())
    following=commit([]); release()
    assert published()==[['v2.0.0',following]], ('deferred major bump missing', published())
    commit(['release:skip','release:minor'])
    commit(['release:skip'], fail=True)
    commit(['release:major'], fail=True)
    after=commit([])
    trailing=commit(['release:skip','release:minor'])
    release()
    assert published()==[['v2.0.0',following], ['v3.0.0',after]], ('backlog handling', published())
    release()
    assert len(published()) == 2, ('trailing skip released on retry', published())
    final=commit([]); release()
    assert published()[-1]==['v3.1.0',final], ('trailing minor lost', published())
    extra=commit([]); release()
    assert published()[-1]==['v3.1.1',extra], ('skip or bump leaked', published())
    print('ok: skip tip, retries, repeated skips, backlog, failed checks, and deferred bumps')
PY
