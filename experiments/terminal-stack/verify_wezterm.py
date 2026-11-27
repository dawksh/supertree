import json, os, pathlib, shutil, subprocess, tempfile, time, unicodedata

# Opt-in desktop smoke test: needs an installed WezTerm and a graphical session.
# Run through scripts/test-local.sh; all GUI/PTY state belongs to this instance.
if not os.environ.get('SUPERTREE_TEST_SANDBOX'):
    raise SystemExit('Run through bash scripts/test-local.sh -- python3 experiments/terminal-stack/verify_wezterm.py')
root = pathlib.Path(__file__).resolve().parents[2]
spike = str(root / '.dev/terminal-stack/spike')
wez = shutil.which('wezterm')
if not wez:
    raise SystemExit('WezTerm is not installed; this optional desktop probe does not install it.')
gui_bin = str(pathlib.Path(wez).resolve().with_name('wezterm-gui'))
if not pathlib.Path(gui_bin).is_file() or not pathlib.Path(spike).is_file():
    raise SystemExit('Run experiments/terminal-stack/run probe first and use a WezTerm install with wezterm-gui beside wezterm.')
namespace = pathlib.Path(tempfile.mkdtemp(prefix='st-stack-wezterm-', dir='/tmp'))
(namespace/'host').mkdir(mode=0o700)
env = os.environ.copy()
env.pop('WEZTERM_UNIX_SOCKET', None)
env.pop('WEZTERM_PANE', None)
env['XDG_RUNTIME_DIR'] = str(namespace)
env['XDG_CACHE_HOME'] = str(namespace/'cache')
env['XDG_CONFIG_HOME'] = str(namespace/'config')
config = namespace/'wezterm.lua'
config.write_text('return { initial_cols = 120, initial_rows = 30, check_for_updates = false, automatically_reload_config = false, quit_when_all_windows_are_closed = true, window_close_confirmation = "NeverPrompt", enable_tab_bar = false, unix_domains = {}, ssh_domains = {} }\n')
log = (namespace/'gui.log').open('wb')
processes = []
started = False

def run(args, **kwargs):
    return subprocess.check_output(args, env=env, timeout=15, text=True, **kwargs)

def inspect():
    return json.loads(run([spike, 'inspect', '-state', str(namespace/'host')]))['Frames']

def launch():
    for name in ('pane', 'socket'):
        (namespace/name).unlink(missing_ok=True)
    script = 'printf "%s\\n" "$WEZTERM_PANE" > "$1/pane"\nprintf "%s\\n" "$WEZTERM_UNIX_SOCKET" > "$1/socket"\nexec "$2" attach -state "$1/host"'
    p = subprocess.Popen([gui_bin, '--config-file', str(config), 'start', '--always-new-process', '--no-auto-connect', '--cwd', str(namespace), '--', '/bin/sh', '-c', script, 'proof', str(namespace), spike], env=env, stdout=log, stderr=log)
    processes.append(p)
    deadline = time.monotonic()+15
    while True:
        socket = (namespace/'socket').read_text().strip() if (namespace/'socket').exists() else ''
        pane = (namespace/'pane').read_text().strip() if (namespace/'pane').exists() else ''
        if socket and pane.isdigit():
            break
        if p.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('WezTerm did not start its isolated pane')
        time.sleep(.1)
    cli_env = env.copy()
    cli_env['WEZTERM_UNIX_SOCKET'] = socket
    return p, cli_env, pane

def cli(cli_env, pane, command, *args):
    return subprocess.check_output([wez, 'cli', '--no-auto-start', command, '--pane-id', pane, *args], env=cli_env, timeout=10, text=True)

def wait_text(cli_env, pane, expected):
    deadline = time.monotonic()+10
    while time.monotonic() < deadline:
        text = cli(cli_env, pane, 'get-text')
        if unicodedata.normalize('NFC', expected) in unicodedata.normalize('NFC', text):
            return text
        time.sleep(.1)
    raise RuntimeError('Real WezTerm screen missing expected test text: '+repr(expected))

try:
    run([spike, 'start', '-state', str(namespace/'host'), '-command', 'exec /bin/zsh -f -i', '-command', 'exec nvim --clean --cmd "set noswapfile"', '-command', "printf 'THIRD-REAL-TERMINAL'; exec sleep 120"])
    started = True
    pids = [f['PID'] for f in inspect()]
    p, cli_env, pane = launch()
    wait_text(cli_env, pane, 'terminal proof')
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1b3\x1b0')
    wait_text(cli_env, pane, 'views [1 2]')
    assert not inspect()[2]['Exited'] and inspect()[2]['PID'] == pids[2]
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1b2\x1bH')
    wait_text(cli_env, pane, 'views [2 1]')
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1b3')
    wait_text(cli_env, pane, 'THIRD-REAL-TERMINAL')
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1b2')
    time.sleep(.2)
    cli(cli_env, pane, 'send-text', '--no-paste', 'i')
    message = 'LIVE-WEZTERM-界-e\u0301-👩\u200d💻'
    cli(cli_env, pane, 'send-text', message)
    wait_text(cli_env, pane, message)
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1bq')
    p.wait(timeout=10)
    assert [f['PID'] for f in inspect()] == pids
    p, cli_env, pane = launch()
    wait_text(cli_env, pane, message)
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1b1')
    cli(cli_env, pane, 'send-text', '--no-paste', "printf 'STOCK-ZSH-REAL-OK\\n'\r")
    wait_text(cli_env, pane, 'STOCK-ZSH-REAL-OK')
    cli(cli_env, pane, 'send-text', '--no-paste', '\x1bq')
    p.wait(timeout=10)
    assert [f['PID'] for f in inspect()] == pids
    result = {'version': run([wez, '--version']).strip(), 'real_gui_started': True, 'separate_config_and_process': True, 'unicode_paste_visible': True, 'hide_reveal_and_move_preserve_pids': True, 'same_pids_after_reattach': True, 'unsaved_nvim_visible_after_reattach': True, 'stock_zsh_command_visible': True, 'human_font_and_authenticated_harness_check': 'not performed'}
    (root/'.dev/stage2-wezterm-result.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result))
except BaseException:
    log.flush()
    (root/'.dev/stage2-wezterm-gui.log').write_bytes((namespace/'gui.log').read_bytes())
    raise
finally:
    for p in processes:
        if p.poll() is None:
            p.terminate()
            try: p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill(); p.wait(timeout=5)
    if started:
        try: run([spike,'stop','-state',str(namespace/'host')])
        except Exception: pass
        deadline=time.monotonic()+5
        while (namespace/'host/host.lock').exists() and time.monotonic()<deadline: time.sleep(.1)
    log.close()
    if started and (namespace/'host/host.lock').exists():
        raise RuntimeError('Host shutdown did not finish; preserve namespace for inspection: '+str(namespace))
    shutil.rmtree(namespace)
