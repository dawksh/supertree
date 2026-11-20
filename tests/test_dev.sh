#!/usr/bin/env bash
set -euo pipefail

task_repo=$(cd "$(dirname "$0")/.." && pwd)
task_tmux=$(command -v tmux || true)
[ -n "$task_tmux" ] || { printf 'skip: dev sandbox integration needs tmux\n'; exit 0; }
task_tmp=$(mktemp -d)
task_checkout="$task_tmp/checkout with #'quote"
task_user="$task_tmp/user"
task_tools="$task_tmp/tools"
cleanup() {
  [ -z "${task_control_pid:-}" ] || kill "$task_control_pid" >/dev/null 2>&1 || true
  "$task_checkout/dev" stop >/dev/null 2>&1 || true
  rm -rf "$task_tmp"
}
trap cleanup EXIT
mkdir -p "$task_checkout/bin" "$task_checkout/tmux" "$task_user/.local/bin" "$task_tools"
task_checkout=$(cd "$task_checkout" && pwd -P)
cp "$task_repo/dev" "$task_checkout/dev"
cp "$task_repo/bin/st" "$task_checkout/bin/st"
cp -R "$task_repo/bin/st-lib" "$task_checkout/bin/st-lib"
cp "$task_repo/tmux/supertree.conf" "$task_checkout/tmux/supertree.conf"

export HOME="$task_user"
export PATH="$task_tools:$PATH"
export ST_DEV_GO_LOG="$task_tmp/builds"
export ST_DEV_INSTALLED_LOG="$task_tmp/installed-used"
# Simulate a developer who already has a release installed and inherited config.
export ST_CONFIG="$task_user/installed-config" ST_STATE="$task_user/installed-state"
export ST_WORKTREE_ROOT="$task_user/installed-trees" ST_WINDOWS=agent ST_HARNESS=claude
export ST_NEXT_SOCKET=/tmp/installed-core.sock
printf 'installed tmux config\n' > "$task_user/.tmux.conf"
printf 'installed st config\n' > "$task_user/installed-config"
cat > "$task_user/.local/bin/st" <<'INSTALLED'
#!/bin/sh
: > "$ST_DEV_INSTALLED_LOG"
exit 91
INSTALLED
cp "$task_user/.local/bin/st" "$task_user/.local/bin/tmux"
chmod +x "$task_user/.local/bin/st" "$task_user/.local/bin/tmux"
cat > "$task_tools/go" <<'GO'
#!/bin/sh
printf '%s\n' "$*" >> "$ST_DEV_GO_LOG"
GO
chmod +x "$task_tools/go"
# Avoid depending on Go in the shell-only CI/release checks. Go behavior has its
# own tests; this client reports the environment supplied by the real launcher.
cat > "$task_checkout/bin/st-next" <<'CLIENT'
#!/bin/sh
printf '%s\n' "$ST_STATE" "$ST_CONFIG" "$ST_WORKTREE_ROOT" "$ST_NEXT_TMUX_SOCKET" "${ST_NEXT_SOCKET:-default-core}" "$@"
CLIENT
chmod +x "$task_checkout/bin/st-next"

# The caller's directory must survive launching a checkout at another path.
cd "$task_tmp"
"$task_checkout/dev" next commands --json > "$task_tmp/result"
grep -Fx "$task_checkout/.dev/state" "$task_tmp/result" >/dev/null
grep -Fx "$task_checkout/.dev/config" "$task_tmp/result" >/dev/null
grep -Fx "$task_checkout/.dev/worktrees" "$task_tmp/result" >/dev/null
grep -Fx 'default-core' "$task_tmp/result" >/dev/null
[ "$(wc -l < "$ST_DEV_GO_LOG" | tr -d ' ')" = 1 ]
grep -Fx "ST_WINDOWS='agent vim shell'" "$task_checkout/.dev/config" >/dev/null
[ ! -e "$task_user/installed-state" ]
[ ! -e "$task_user/installed-trees" ]
[ -z "$(find "$task_checkout/.dev" -type l -print)" ]

# Launch real local terminals without an attached client or the optional sidebar.
printf 'ST_SIDEBAR=0\n' >> "$task_checkout/.dev/config"
"$task_checkout/dev" home </dev/null >/dev/null 2>&1 || true
"$task_checkout/dev" tmux has-session -t '=supertree'
"$task_checkout/dev" tmux list-keys -T root M-1 > "$task_tmp/key"
grep -F '/.dev/bin/st' "$task_tmp/key" >/dev/null
grep -F 'window 1' "$task_tmp/key" >/dev/null
! grep -F '~/.local/bin/st' "$task_tmp/key" >/dev/null
printf -v task_run 'st --version > %q' "$task_tmp/runtime-version"
"$task_checkout/dev" tmux run-shell "$task_run"
grep -Fx 'st dev' "$task_tmp/runtime-version" >/dev/null
[ ! -e "$ST_DEV_INSTALLED_LOG" ]
"$task_checkout/dev" tmux display-message -p -t '=supertree:shell' '#{pane_current_path}' > "$task_tmp/path"
[ "$(cd "$(cat "$task_tmp/path")" && pwd -P)" = "$(pwd -P)" ]

# Exercise the actual client key table, not just the text of list-keys. A
# control-mode client supplies an attached client without requiring a test TTY.
task_send_options=$("$task_checkout/dev" tmux list-commands | sed -n '/^send-keys /p')
if [[ "$task_send_options" = *K* && "$task_send_options" = *'-c target-client'* ]]; then
  task_shell=$("$task_checkout/dev" tmux display-message -p -t '=supertree:shell' '#{pane_id}')
  task_sidebar=$("$task_checkout/dev" tmux split-window -hb -d -t '=supertree:shell' -P -F '#{pane_id}' 'sleep 60')
  "$task_checkout/dev" tmux set-option -p -t "$task_sidebar" @st_sidebar 1
  mkfifo "$task_tmp/client-input"
  exec 3<> "$task_tmp/client-input"
  "$task_checkout/.dev/bin/tmux" -C attach-session -t '=supertree' <&3 > "$task_tmp/client-output" 2>&1 &
  task_control_pid=$!
  task_client=''
  for task_tick in $(seq 1 40); do
    task_client=$("$task_checkout/dev" tmux list-clients -F '#{client_name}')
    [ -z "$task_client" ] || break
    sleep 0.05
  done
  [ -n "$task_client" ] || { cat "$task_tmp/client-output" >&2; exit 1; }

  # A shell startup file or stale server can replace PATH after the launcher ran.
  "$task_checkout/dev" tmux set-environment -g PATH "$task_user/.local/bin:/usr/bin:/bin"
  "$task_checkout/dev" tmux select-pane -t "$task_shell"
  "$task_checkout/dev" tmux send-keys -K -c "$task_client" M-s
  task_active=''
  for task_tick in $(seq 1 40); do
    task_active=$("$task_checkout/dev" tmux display-message -p -t '=supertree:shell' '#{pane_id}')
    [ "$task_active" != "$task_sidebar" ] || break
    sleep 0.05
  done
  if [ "$task_active" != "$task_sidebar" ]; then
    printf 'FAIL: Option-S did not focus the sidebar through the client key table\n' >&2
    [ ! -e "$ST_DEV_INSTALLED_LOG" ] || printf 'FAIL: shortcut ran the installed command\n' >&2
    exit 1
  fi
  "$task_checkout/dev" tmux send-keys -K -c "$task_client" M-1
  for task_tick in $(seq 1 40); do
    task_active=$("$task_checkout/dev" tmux display-message -p -t '=supertree:shell' '#{pane_id}')
    [ "$task_active" != "$task_shell" ] || break
    sleep 0.05
  done
  [ "$task_active" = "$task_shell" ] || { printf 'FAIL: Option-1 did not return to the home shell\n' >&2; exit 1; }
  [ ! -e "$ST_DEV_INSTALLED_LOG" ]
  kill "$task_control_pid"
  wait "$task_control_pid" 2>/dev/null || true
  task_control_pid=''
  exec 3>&-
else
  printf 'skip: tmux has no send-keys -K client injection; remaining sandbox checks still run\n'
fi

# Commands from local background jobs reuse the build and stay on the same server.
task_count=$(wc -l < "$ST_DEV_GO_LOG" | tr -d ' ')
"$task_checkout/.dev/bin/st" next inspect --json > "$task_tmp/internal"
[ "$(wc -l < "$ST_DEV_GO_LOG" | tr -d ' ')" = "$task_count" ]
grep -Fx "$task_checkout/.dev/state" "$task_tmp/internal" >/dev/null
grep -Fx 'ST_SIDEBAR=0' "$task_checkout/.dev/config" >/dev/null
grep -Fx 'installed tmux config' "$task_user/.tmux.conf" >/dev/null
grep -Fx 'installed st config' "$task_user/installed-config" >/dev/null

# The manual entrypoint must open the familiar agent/editor/shell workspace.
# Fixtures stand in for interactive programs; never start a real provider here.
for task_program in codex nvim; do
  printf '#!/bin/sh\nexec /bin/sleep 60\n' > "$task_user/.local/bin/$task_program"
  chmod +x "$task_user/.local/bin/$task_program"
done
git -C "$task_checkout" init -q -b main
git -C "$task_checkout" -c user.name=Test -c user.email=test@example.com commit -q --allow-empty -m initial
cd "$task_checkout"
"$task_checkout/dev" open </dev/null >"$task_tmp/open.log" 2>&1 || true
task_tree=$("$task_checkout/dev" tmux list-sessions -F '#{session_name}' | sed '/^supertree$/d' | head -1)
[ -n "$task_tree" ] || { cat "$task_tmp/open.log" >&2; exit 1; }
"$task_checkout/dev" tmux list-windows -t "=$task_tree" -F '#{window_name}' > "$task_tmp/windows"
for task_window in codex nvim shell; do grep -Fx "$task_window" "$task_tmp/windows" >/dev/null; done
# A new layout setting adds a window when reopening; existing programs survive.
task_before=$("$task_checkout/dev" tmux list-panes -s -t "=$task_tree" -F '#{pane_id} #{pane_pid}')
printf "ST_WINDOWS='agent vim shell extra'\nST_TAB_extra='sleep 60'\n" >> "$task_checkout/.dev/config"
"$task_checkout/dev" open </dev/null >/dev/null 2>&1 || true
"$task_checkout/dev" tmux list-windows -t "=$task_tree" -F '#{window_name}' | grep -Fx extra >/dev/null
task_after=$("$task_checkout/dev" tmux list-panes -s -t "=$task_tree" -F '#{pane_id} #{pane_pid}')
while IFS= read -r task_pane; do grep -Fx "$task_pane" <<< "$task_after" >/dev/null; done <<< "$task_before"

"$task_checkout/dev" stop >/dev/null
if "$task_checkout/dev" tmux -N has-session 2>/dev/null; then
  printf 'FAIL: dev stop left the local session running\n' >&2
  exit 1
fi
[ -f "$task_checkout/.dev/config" ]
[ -d "$task_checkout/.dev/state" ]

printf 'ok: dev builds, familiar workspace, additive layout updates, isolated tmux, live keys, wrappers, config reuse, caller directory, and stop\n'
