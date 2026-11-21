#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
python3 - "$ROOT" <<'PY'
import json, os, pathlib, shlex, shutil, subprocess, sys, tempfile

root = pathlib.Path(sys.argv[1])
contract = json.loads((root / 'tests/fixtures/release-v0.4.34-workflow.json').read_text())

def bindings(lines):
    result = {}
    for line in lines:
        if not line.startswith('bind -n '):
            continue
        parts = shlex.split(line)
        assert parts[2] not in result, f'duplicate key: {parts[2]}'
        result[parts[2]] = parts[3:]
    return result

current = bindings((root / 'tmux/supertree.conf').read_text().splitlines())
for key, action in bindings(contract['bindings']).items():
    assert current.get(key) == action, f'{contract["release"]} shortcut changed: {key}'

with tempfile.TemporaryDirectory() as temporary:
    base = pathlib.Path(temporary)
    checkout = base / "checkout with #'quote"
    (checkout / 'bin').mkdir(parents=True)
    shutil.copy2(root / 'bin/st', checkout / 'bin/st')
    shutil.copytree(root / 'bin/st-lib', checkout / 'bin/st-lib')
    tools = base / 'home/.local/bin'
    tools.mkdir(parents=True)
    log, core_log = base / 'launched', base / 'core'
    fixture = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
name = pathlib.Path(sys.argv[0]).name
key = 'RELEASE_CORE_LOG' if name == 'st-next' else 'RELEASE_LAUNCH_LOG'
with open(os.environ[key], 'a') as file:
    file.write(json.dumps({'command': name, 'args': sys.argv[1:]}) + '\n')
if name == 'st-next':
    assert sys.argv[1] == 'harness', 'core started outside an explicit harness opt-in'
    os.execvp(sys.argv[2], [sys.argv[2], *sys.argv[4:]])
'''
    for name in ('claude', 'codex', 'opencode', 'st-next'):
        path = checkout / 'bin/st-next' if name == 'st-next' else tools / name
        path.write_text(fixture)
        path.chmod(0o755)
    repo = base / 'repo'
    repo.mkdir()
    def command(args, env=None):
        result = subprocess.run(args, cwd=repo, env=env, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=15)
        assert result.returncode == 0, (args, result.stdout, result.stderr)
        return result
    command(['git', 'init', '-q', '-b', 'main'])
    command(['git', '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
             'commit', '-q', '--allow-empty', '-m', 'initial'])
    env = {key: value for key, value in os.environ.items()
           if not key.startswith('ST_') and key not in ('TMUX', 'TMUX_PANE')}
    env.update(HOME=str(base / 'home'), PATH=str(tools) + os.pathsep + os.environ['PATH'],
               ST_CONFIG=str(base / 'config'), ST_STATE=str(base / 'state'),
               ST_WORKTREE_ROOT=str(base / 'trees'),
               RELEASE_LAUNCH_LOG=str(log), RELEASE_CORE_LOG=str(core_log))
    config = base / 'config'
    for name, expected in contract['harnesses'].items():
        config.write_text(f'ST_HARNESS={name}\nST_SHELL=/usr/bin/true\n')
        env['ST_STATE'] = str(base / name)
        log.unlink(missing_ok=True)
        for _ in range(2):
            command([str(checkout / 'bin/st'), '_agent'], env)
        actual = [json.loads(row) for row in log.read_text().splitlines()]
        assert actual == [{'command': expected['command'], 'args': expected[phase]}
                          for phase in ('start', 'resume')], (name, actual)
        settings = dict(line.split('\t', 1) for line in command([str(checkout / 'bin/st'), 'config'], env).stdout.splitlines())
        assert settings['windows'].split() == contract['windows'], settings
        assert not core_log.exists(), 'installed core changed normal agent launch'

    # Explicit tools add integration to the same real harness, with its released args.
    config.write_text('ST_HARNESS=codex\nST_AGENT_TOOLS=1\nST_SHELL=/usr/bin/true\n')
    env['ST_STATE'] = str(base / 'opt-in')
    command([str(checkout / 'bin/st'), '_agent'], env)
    assert json.loads(core_log.read_text().splitlines()[-1])['args'] == ['harness', 'codex', '--', '--no-daemon']
    assert json.loads(log.read_text().splitlines()[-1]) == {'command': 'codex', 'args': ['--no-daemon']}

    if shutil.which('nvim'):
        for mode, enabled in (('', False), ('ST_AGENT_TOOLS=1\n', True), ('ST_AGENT_MODE=paired\n', True)):
            config.write_text('ST_HARNESS=codex\nST_SHELL=/usr/bin/true\n' + mode)
            passed = base / 'editor-passed'
            passed.unlink(missing_ok=True)
            check = base / 'editor.lua'
            check.write_text("local exists = vim.fn.exists(':SupertreeAsk') == 2\n"
                             f"if exists == {str(enabled).lower()} then\n"
                             f"  vim.fn.writefile({{'ok'}}, {json.dumps(str(passed))})\nend\n"
                             "vim.cmd('qa!')\n")
            previous = core_log.read_text()
            command([str(checkout / 'bin/st'), '_run', 'nvim', '--headless', '-u', 'NONE',
                     '-c', f'lua dofile({json.dumps(str(check))})'], env)
            assert passed.exists(), ('editor bridge enabled without opt-in or missing after opt-in', mode)
            assert core_log.read_text() == previous, 'opening editor started core'
print(f'ok: {contract["release"]} keys, native harness launch/resume, and explicit integration opt-ins')
PY
