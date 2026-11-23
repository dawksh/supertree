#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
command -v tmux >/dev/null || { printf 'skip: tmux unavailable\n'; exit 0; }
[ -n "${SUPERTREE_TEST_SANDBOX:-}" ] || {
  printf 'Run through bash scripts/test-local.sh -- bash %s\n' "$0" >&2; exit 1;
}
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
st_register_command() { :; }
unset NO_COLOR
export TERM=xterm-256color COLORTERM=truecolor
. "$ROOT/bin/st-lib/05-ui.sh"
. "$ROOT/bin/st-lib/45-chrome.sh"
. "$ROOT/bin/st-lib/50-tmux.sh"
export ST_SHELL=/bin/sh
configured_windows() { printf 'agent\nvim\nshell\n'; }

for spec in 'wide 160 48 on' 'medium 120 36 on' 'narrow 80 24 on' 'short 120 24 on' 'tiny 36 20 on'; do
  read -r sess width height rows <<< "$spec"
  tmux -f /dev/null new-session -d -s "$sess" -x "$width" -y "$height" -n codex 'sleep 60'
  tmux new-window -d -t "=$sess" -n nvim 'sleep 60'
  tmux new-window -d -t "=$sess" -n shell 'sleep 60'
  tmux set-option -t "=$sess:" @supertree_label 'supertree/fix-login'
  if [ "$sess" = wide ]; then
    # An existing workspace must lose its padding hooks without losing the
    # user's hooks or restarting any of its terminals.
    tmux set-option -t "=$sess:" status 3
    tmux set-hook -t "=$sess:" 'client-resized[730]' 'set-option status 3'
    tmux set-hook -t "=$sess:" 'client-resized[999]' 'set-option @user-resize yes'
  fi
  style_session "$sess"
  style_tab "=$sess:codex" 1
  style_tab "=$sess:nvim" 2
  style_tab "=$sess:shell" 3
  case $(tmux show-options -wv -t "=$sess:codex" window-style) in
    *'bg=#0f131b'*) ;; *) fail "$sess program background differs from workspace";;
  esac
  for option in pane-border-style pane-active-border-style; do
    case $(tmux show-options -wv -t "=$sess:codex" "$option") in
      *'bg=#0f131b'*) ;; *) fail "$sess divider background differs from program surface";;
    esac
  done
  [ "$(tmux show-options -v -t "=$sess:" status)" = "$rows" ] || fail "$sess strip height"
  strip=$(tmux show-options -v -t "=$sess:" 'status-format[0]')
  text=$(tmux display-message -p -t "=$sess:" "$strip" | sed 's/#\[[^]]*\]//g')
  case $sess in
    narrow|tiny) case $text in *'1 agent'*|*'2 vim'*) fail "$sess didn't collapse tabs";; esac;;
    *) case $text in *'1 agent'*'2 vim'*'3 shell'*) ;; *) fail "$sess missing logical tabs: $text";; esac;;
  esac
done
hooks=$(tmux show-hooks -t '=wide:')
case $hooks in *'client-resized[730]'*) fail 'old padding hook survived';; esac
case $hooks in *'client-resized[999]'*) ;; *) fail 'user resize hook was removed';; esac

# Styling and geometry changes cannot move focus or restart a terminal.
before=$(tmux list-panes -a -F '#{session_name} #{window_id} #{pane_id} #{pane_pid}')
active=$(tmux display-message -p -t '=medium:' '#{window_id} #{pane_id}')
tmux resize-window -t '=medium:codex' -x 80 -y 24
[ "$(tmux show-options -v -t '=medium:' status)" = on ] || fail 'resize did not compact strip'
tmux resize-window -t '=medium:codex' -x 120 -y 36
[ "$(tmux show-options -v -t '=medium:' status)" = on ] || fail 'wide resize added padding'
[ "$(tmux display-message -p -t '=medium:' '#{window_id} #{pane_id}')" = "$active" ] || fail 'resize changed focus'
[ "$(tmux list-panes -a -F '#{session_name} #{window_id} #{pane_id} #{pane_pid}')" = "$before" ] || fail 'chrome replaced a program'
[ "$(tmux show-options -v -t '=narrow:' status)" = on ] || fail 'resize changed another session'
ST_PROGRAM_THEME=native style_panes '=tiny:codex'
case $(tmux show-options -wv -t '=tiny:codex' window-style) in
  *'bg=default'*) ;; *) fail 'native mode overrode terminal background';;
esac
case $(tmux show-options -wv -t '=tiny:codex' pane-border-style) in
  *'bg=default'*) ;; *) fail 'native mode overrode divider background';;
esac

# Verify what tmux actually sends a terminal, not just the format source.
python3 - <<'PY'
import fcntl, os, pty, re, select, struct, subprocess, termios, time

def tmux(*args):
    return subprocess.run(['tmux', *args], check=True, capture_output=True, text=True).stdout

def draw(session, width, height):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', height, width, 0, 0))
    process = subprocess.Popen(['tmux', '-u', 'attach-session', '-t', session], stdin=slave,
                               stdout=slave, stderr=slave, env={**os.environ, 'TERM': 'xterm-256color'})
    os.close(slave)
    data = b''
    try:
        until = time.monotonic() + .8
        while time.monotonic() < until:
            if select.select([master], [], [], .05)[0]:
                data += os.read(master, 65536)
        # Colour changes can split repo/branch text across several SGR runs.
        raw = data.decode(errors='replace')
        assert '\x1b[48;2;15;19;27m' in raw, 'terminal body did not use workspace background'
        body_height = int(tmux('display-message', '-p', '-t', session, '#{window_height}').strip())
        assert body_height == height - 1, ('unused terminal rows', body_height, height)
        return re.sub(r'\x1b\[[0-9;]*m|\x1b\(B', '', raw)
    finally:
        # Detach only the client created above. Never send prefix keys into a program.
        for line in tmux('list-clients', '-F', '#{client_pid} #{client_name}').splitlines():
            pid, name = line.split(' ', 1)
            if int(pid) == process.pid:
                tmux('detach-client', '-t', name)
        process.wait(timeout=3)
        os.close(master)

wide = draw('wide', 160, 48)
assert all(word in wide for word in ('supertree/', 'fix-login', '1 agent', '2 vim', '3 shell', 'M-w worktrees', 'M-/ keys')), repr(wide)
assert '\x1b[H 1 agent' in wide, 'top strip did not start with the tabs'
tmux('set-option', '-g', '@st_last_key', 'worktrees')
assert 'worktrees M-w' in draw('wide', 160, 48), 'bottom bar did not show the last shortcut'
tmux('set-option', '-gu', '@st_last_key')
narrow = draw('narrow', 80, 24)
assert 'fix-login' in narrow and '1 agent' not in narrow and 'M-w' in narrow, repr(narrow)

# Labels are text even when they look like styles, formats, or shell jobs.
marker = os.path.join(os.environ['SUPERTREE_TEST_SANDBOX'], 'injected')
for label in ('r/#[fg=red]%H', 'r/#{session_name}', '仓库/修复登录', f'r/#(touch {marker})'):
    tmux('set-option', '-t', 'wide', '@supertree_label', label)
    output = draw('wide', 160, 48)
    if len(label) < 52:
        assert label in output, (label, repr(output))
    assert not os.path.exists(marker), 'label executed a shell command'
tmux('set-option', '-t', 'wide', '@supertree_label', 'r/a\x1b[31m\nbranch')
output = draw('wide', 160, 48)
assert 'a [31m branch' in output, repr(output)
print('ok: actual terminal frames and literal labels')
PY
printf 'ok: compact terminal coverage, old padding migration, logical tabs, and preserved programs/focus\n'
